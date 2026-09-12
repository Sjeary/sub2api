//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *UserRepoSuite) TestDebtBalance_PartialRepaymentPreservesDebt() {
	user := s.mustCreateUser(&service.User{Email: "overdraft-repay@example.test", Balance: -10})
	change, err := s.repo.AdjustBalance(s.ctx, user.ID, 3)
	s.Require().NoError(err)
	s.Require().Equal(-10.0, change.Old)
	s.Require().Equal(-7.0, change.New)
	s.Require().NoError(s.repo.UpdateBalance(s.ctx, user.ID, 2))
	current, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal(-5.0, current.Balance)
	_, err = s.repo.AdjustBalance(s.ctx, user.ID, -1)
	s.Require().ErrorIs(err, service.ErrBalanceNegative)
}

func (s *UserRepoSuite) TestDebtBalance_NegativeRedeemPreservesDebt() {
	user := s.mustCreateUser(&service.User{Email: "overdraft-negative-redeem@example.test", Balance: -10})
	s.Require().NoError(s.repo.ApplyRedeemBalanceAdjustment(s.ctx, user.ID, -3.5))
	current, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal(-10.0, current.Balance)
}
