//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type guardedAuthTestCache struct {
	APIKeyCache
	mu          sync.Mutex
	entry       *APIKeyAuthCacheEntry
	afterRead   func()
	beforeWrite func()
	subscribed  chan func(string)
}

func (c *guardedAuthTestCache) SubscribeAuthCacheInvalidation(ctx context.Context, handler func(string)) error {
	select {
	case c.subscribed <- handler:
	case <-ctx.Done():
		return ctx.Err()
	}
	NotifyAuthCacheSubscriptionReady(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func TestAuthInvalidationSubscriberProtectsL2OnlyInstances(t *testing.T) {
	cache := &guardedAuthTestCache{subscribed: make(chan func(string), 1)}
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60}}
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, cache, cfg)
	require.Nil(t, svc.authCacheL1)
	require.Nil(t, svc.authNegativeCacheL1)
	svc.StartAuthCacheInvalidationSubscriber(context.Background())
	t.Cleanup(svc.StopAuthCacheInvalidationSubscriber)
	var handler func(string)
	select {
	case handler = <-cache.subscribed:
	case <-time.After(time.Second):
		t.Fatal("L2-only instance did not subscribe")
	}
	cache.mu.Lock()
	cache.entry = &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{Version: apiKeyAuthSnapshotVersion}}
	cache.mu.Unlock()
	handler(svc.authCacheKey("revoked-credit"))
	_, err := cache.GetAuthCache(context.Background(), "revoked-credit")
	require.ErrorIs(t, err, redis.Nil)
}

func (c *guardedAuthTestCache) GetAuthCache(context.Context, string) (*APIKeyAuthCacheEntry, error) {
	c.mu.Lock()
	entry := c.entry
	c.mu.Unlock()
	if c.afterRead != nil {
		c.afterRead()
	}
	if entry == nil {
		return nil, redis.Nil
	}
	return entry, nil
}
func (c *guardedAuthTestCache) SetAuthCache(_ context.Context, _ string, e *APIKeyAuthCacheEntry, _ time.Duration) error {
	if c.beforeWrite != nil {
		c.beforeWrite()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entry = e
	return nil
}

func TestAuthInvalidationDiscardsInFlightRedisRead(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var reads atomic.Int32
	cache := &guardedAuthTestCache{afterRead: func() {
		if reads.Add(1) == 1 {
			close(started)
			<-release
		}
	}}
	repo := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*APIKey, error) {
		limit := 0.0
		return &APIKey{ID: 1, User: &User{ID: 7, Balance: -5, OverdraftLimit: &limit}}, nil
	}}
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60, L1Size: 64, L1TTLSeconds: 60}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg)
	t.Cleanup(svc.authCacheL1.Close)
	old := 10.0
	cache.entry = &APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), &APIKey{ID: 1, User: &User{ID: 7, Balance: -5, OverdraftLimit: &old}})}
	done := make(chan *APIKey, 1)
	errs := make(chan error, 1)
	go func() {
		key, err := svc.GetByKey(context.Background(), "redis-overdraft-key")
		done <- key
		errs <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("redis read did not start")
	}
	svc.InvalidateAuthCacheByKey(context.Background(), "redis-overdraft-key")
	once.Do(func() { close(release) })
	var key *APIKey
	select {
	case key = <-done:
	case <-time.After(time.Second):
		t.Fatal("lookup did not complete")
	}
	require.NoError(t, <-errs)
	require.Equal(t, 0.0, *key.User.OverdraftLimit)
	svc.authCacheL1.Wait()
	entry, ok := svc.authCacheL1.Get(svc.authCacheKey("redis-overdraft-key"))
	require.True(t, ok)
	require.Equal(t, 0.0, *entry.(*APIKeyAuthCacheEntry).Snapshot.User.OverdraftLimit)
}

func TestAuthInvalidationSerializesRedisWrite(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var writes atomic.Int32
	cache := &guardedAuthTestCache{beforeWrite: func() {
		if writes.Add(1) == 1 {
			close(started)
			<-release
		}
	}}
	var revoked atomic.Bool
	repo := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*APIKey, error) {
		limit := 10.0
		if revoked.Load() {
			limit = 0
		}
		return &APIKey{ID: 1, User: &User{ID: 7, Balance: -5, OverdraftLimit: &limit}}, nil
	}}
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg)
	done := make(chan error, 1)
	go func() { _, err := svc.GetByKey(context.Background(), "write-overdraft-key"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("redis write did not start")
	}
	revoked.Store(true)
	invalidated := make(chan struct{})
	go func() { svc.InvalidateAuthCacheByKey(context.Background(), "write-overdraft-key"); close(invalidated) }()
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("lookup did not complete")
	}
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not complete")
	}
	key, err := svc.GetByKey(context.Background(), "write-overdraft-key")
	require.NoError(t, err)
	require.Equal(t, 0.0, *key.User.OverdraftLimit)
}

func TestAuthInvalidationMessageRemovesLateSharedEntry(t *testing.T) {
	cache := &guardedAuthTestCache{}
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60}}
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, cache, cfg)
	old := 10.0
	cache.entry = &APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), &APIKey{ID: 1, User: &User{ID: 7, OverdraftLimit: &old}})}
	svc.invalidateLocalAuthCache(svc.authCacheKey("late-entry"))
	_, err := cache.GetAuthCache(context.Background(), "late-entry")
	require.ErrorIs(t, err, redis.Nil)
}

func TestAuthInvalidationReloadIsBounded(t *testing.T) {
	cache := &guardedAuthTestCache{}
	var svc *APIKeyService
	calls := 0
	repo := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*APIKey, error) {
		calls++
		svc.invalidateLocalAuthCache(svc.authCacheKey("changing-credit"))
		limit := 10.0
		return &APIKey{ID: 1, User: &User{ID: 7, OverdraftLimit: &limit}}, nil
	}}
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60}}
	svc = NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg)
	_, err := svc.GetByKey(context.Background(), "changing-credit")
	require.ErrorIs(t, err, ErrAPIKeyAuthOverloaded)
	require.Equal(t, 2, calls)
	_, err = cache.GetAuthCache(context.Background(), "changing-credit")
	require.ErrorIs(t, err, redis.Nil)
}
func (c *guardedAuthTestCache) DeleteAuthCache(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entry = nil
	return nil
}
func (c *guardedAuthTestCache) PublishAuthCacheInvalidation(context.Context, string) error {
	return nil
}

func TestAuthInvalidationDoesNotRestoreOldOverdraft(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "pubsub"}[remote], func(t *testing.T) {
			cache := &guardedAuthTestCache{}
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			var calls atomic.Int32
			repo := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*APIKey, error) {
				limit := 0.0
				if calls.Add(1) == 1 {
					limit = 10
					close(started)
					<-release
				}
				return &APIKey{ID: 1, User: &User{ID: 7, Status: StatusActive, Balance: -5, OverdraftLimit: &limit}}, nil
			}}
			cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60, Singleflight: true}}
			svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg)
			done := make(chan *APIKey, 1)
			errs := make(chan error, 1)
			go func() { key, err := svc.GetByKey(context.Background(), "test-overdraft-key"); done <- key; errs <- err }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("lookup not started")
			}
			if remote {
				svc.invalidateLocalAuthCache(svc.authCacheKey("test-overdraft-key"))
			} else {
				svc.InvalidateAuthCacheByKey(context.Background(), "test-overdraft-key")
			}
			once.Do(func() { close(release) })
			var key *APIKey
			select {
			case key = <-done:
			case <-time.After(time.Second):
				t.Fatal("lookup did not complete")
			}
			require.NoError(t, <-errs)
			require.NotNil(t, key)
			require.Equal(t, 0.0, *key.User.OverdraftLimit, "invalidated lookup must reload the revoked allowance")
			again, err := svc.GetByKey(context.Background(), "test-overdraft-key")
			require.NoError(t, err)
			require.Equal(t, 0.0, *again.User.OverdraftLimit, "the old allowance must not remain cached")
		})
	}
}
