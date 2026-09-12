//go:build unit

package service

import (
	"context"
	"encoding/json"
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
