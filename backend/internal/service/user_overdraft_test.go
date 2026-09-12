//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOverdraftBillingEligibility(t *testing.T) {
	zero, custom := 0.0, 10.0
	for _, tc := range []struct {
		name          string
		balance, site float64
		override      *float64
		allowed       bool
	}{
		{"default exhausted", 0, 0, nil, false},
		{"inherit site", -4, 5, nil, true},
		{"site exhausted", -5, 5, nil, false},
		{"explicit zero", -1, 5, &zero, false},
		{"user overrides site", -7, 5, &custom, true},
		{"user exhausted", -10, 5, &custom, false},
		{"user below limit", -11, 5, &custom, false},
		{"positive balance", 1, 5, &zero, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &balanceEligibilityCacheStub{balance: tc.balance}
			cfg := &config.Config{}
			cfg.Billing.DefaultOverdraftLimit = tc.site
			svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(svc.Stop)
			err := svc.CheckBillingEligibility(context.Background(), &User{ID: 1, OverdraftLimit: tc.override}, nil, nil, nil, "")
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrInsufficientBalance)
			}
		})
	}
}

type nextTurnOverdraftRepo struct {
	UserRepository
	user  *User
	err   error
	calls int
}

func (r *nextTurnOverdraftRepo) GetByID(context.Context, int64) (*User, error) {
	r.calls++
	return r.user, r.err
}

func TestOverdraftNextTurnReadsLatestLedger(t *testing.T) {
	limit := 10.0
	repo := &nextTurnOverdraftRepo{user: &User{ID: 1, Balance: -8, OverdraftLimit: &limit}}
	cache := &balanceEligibilityCacheStub{balance: 100}
	cfg := &config.Config{}
	cfg.Billing.DefaultOverdraftLimit = 20
	svc := NewBillingCacheService(cache, repo, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)
	require.NoError(t, svc.CheckBalanceForNextTurn(context.Background(), 1, nil, nil))
	zero := 0.0
	repo.user.OverdraftLimit = &zero
	require.ErrorIs(t, svc.CheckBalanceForNextTurn(context.Background(), 1, nil, nil), ErrInsufficientBalance)
	repo.user.OverdraftLimit = nil
	require.NoError(t, svc.CheckBalanceForNextTurn(context.Background(), 1, nil, nil))
	repo.user.Balance = -20
	require.ErrorIs(t, svc.CheckBalanceForNextTurn(context.Background(), 1, nil, nil), ErrInsufficientBalance)
	repo.err = errors.New("database unavailable")
	require.ErrorIs(t, svc.CheckBalanceForNextTurn(context.Background(), 1, nil, nil), ErrBillingServiceUnavailable)
	repo.err = nil
	repo.user = nil
	require.ErrorIs(t, svc.CheckBalanceForNextTurn(context.Background(), 1, nil, nil), ErrBillingServiceUnavailable)
	require.Equal(t, 6, repo.calls)
}

func TestOverdraftNextTurnPreservesOtherBillingModes(t *testing.T) {
	repo := &nextTurnOverdraftRepo{err: errors.New("must not read balance")}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := NewBillingCacheService(nil, repo, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)
	require.NoError(t, svc.CheckBalanceForNextTurn(context.Background(), 1, nil, nil))
	cfg.RunMode = config.RunModeStandard
	group := &Group{SubscriptionType: SubscriptionTypeSubscription}
	require.NoError(t, svc.CheckBalanceForNextTurn(context.Background(), 1, group, &UserSubscription{}))
	require.Zero(t, repo.calls)
	// Missing subscriptions follow the existing balance fallback.
	require.ErrorIs(t, svc.CheckBalanceForNextTurn(context.Background(), 1, group, nil), ErrBillingServiceUnavailable)
}

func TestOverdraftReserveAndCacheInvalidation(t *testing.T) {
	cache := &balanceEligibilityCacheStub{balance: -9.99}
	cfg := &config.Config{}
	cfg.Billing.DefaultOverdraftLimit = 10
	cfg.Billing.MinimumBalanceReserve = 0.02
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)
	user := &User{ID: 1}
	require.ErrorIs(t, svc.CheckBillingEligibility(context.Background(), user, nil, nil, nil, ""), ErrInsufficientBalance)
	// Comparing the floor directly avoids cancellation in -9.99 + 10.
	cfg.Billing.MinimumBalanceReserve = 0.01
	require.NoError(t, svc.CheckBillingEligibility(context.Background(), user, nil, nil, nil, ""))
	newBalance := -10.5
	syncBalanceCacheAfterDeduction(context.Background(), &postUsageBillingParams{User: user, Cost: &CostBreakdown{ActualCost: 1}}, &billingDeps{billingCacheService: svc}, &UsageBillingApplyResult{NewBalance: &newBalance})
	require.EqualValues(t, 1, cache.invalidateCalls.Load())
	require.EqualValues(t, 0, cache.deductCalls.Load())
}

func TestOverdraftAdminUpdateAndReset(t *testing.T) {
	limit, zero := 10.0, 0.0
	for _, tc := range []struct {
		name        string
		input       UpdateUserInput
		expected    *float64
		invalidates bool
	}{
		{"set", UpdateUserInput{OverdraftLimit: &limit}, &limit, true},
		{"disable", UpdateUserInput{OverdraftLimit: &zero}, &zero, true},
		{"inherit", UpdateUserInput{OverdraftLimitSet: true}, nil, true},
		{"omitted", UpdateUserInput{}, &limit, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rpmUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 42, Balance: -3, OverdraftLimit: &limit}}}
			invalidator := &authCacheInvalidatorStub{}
			svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}
			user, err := svc.UpdateUser(context.Background(), 42, &tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, user.OverdraftLimit)
			require.Equal(t, -3.0, user.Balance)
			require.Equal(t, tc.invalidates, len(invalidator.userIDs) > 0)
		})
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1), 1e12} {
		svc := &adminServiceImpl{}
		_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{OverdraftLimit: &bad})
		require.Error(t, err)
		_, err = svc.CreateUser(context.Background(), &CreateUserInput{OverdraftLimit: &bad})
		require.Error(t, err)
	}
}

func TestOverdraftAuthSnapshotRoundTrip(t *testing.T) {
	zero, limit := 0.0, 10.0
	for _, override := range []*float64{nil, &zero, &limit} {
		svc := &APIKeyService{}
		snapshot := svc.snapshotFromAPIKey(context.Background(), &APIKey{ID: 2, User: &User{ID: 1, Balance: -3, OverdraftLimit: override}})
		body, err := json.Marshal(snapshot)
		require.NoError(t, err)
		var restored APIKeyAuthSnapshot
		require.NoError(t, json.Unmarshal(body, &restored))
		key := svc.snapshotToAPIKey("test-key", &restored)
		require.Equal(t, override, key.User.OverdraftLimit)
		require.Equal(t, -3.0, key.User.Balance)
	}
}
