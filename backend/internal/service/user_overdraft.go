package service

import (
	"context"
	"fmt"
	"math"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// EffectiveOverdraftLimit resolves an explicit user limit, including zero,
// before the site default. The limit controls admission, not final settlement.
func EffectiveOverdraftLimit(override *float64, siteDefault float64) float64 {
	limit := siteDefault
	if override != nil {
		limit = *override
	}
	if math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0 {
		return 0
	}
	return limit
}

// CheckBalanceForNextTurn reloads both balance and credit for a long-lived
// connection. Its handshake user snapshot and balance cache can be stale after
// a settlement or an administrator changes the user's allowance.
func (s *BillingCacheService) CheckBalanceForNextTurn(ctx context.Context, userID int64, group *Group, subscription *UserSubscription) error {
	if s != nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil
	}
	if group != nil && group.IsSubscriptionType() && subscription != nil {
		return nil
	}
	if s == nil || s.userRepo == nil {
		return ErrBillingServiceUnavailable
	}
	loadCtx, cancel := context.WithTimeout(ctx, balanceLoadTimeout)
	defer cancel()
	user, err := s.userRepo.GetByID(loadCtx, userID)
	if err != nil {
		return ErrBillingServiceUnavailable.WithCause(err)
	}
	if user == nil {
		return ErrBillingServiceUnavailable.WithCause(fmt.Errorf("user %d not found", userID))
	}
	if s.balanceBelowEligibilityThreshold(user.Balance, user.OverdraftLimit) {
		return ErrInsufficientBalance
	}
	return nil
}
