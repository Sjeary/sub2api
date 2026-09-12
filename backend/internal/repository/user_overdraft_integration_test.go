//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *APIKeyRepoSuite) TestGetByKeyForAuth_OverdraftOverride() {
	user := s.mustCreateUser("auth-overdraft@example.test")
	key := &service.APIKey{UserID: user.ID, Key: "sk-auth-overdraft", Name: "Overdraft", Status: service.StatusActive}
	s.Require().NoError(s.repo.Create(s.ctx, key))
	for _, limit := range []float64{10, 0} {
		_, err := s.client.User.UpdateOneID(user.ID).SetOverdraftLimit(limit).Save(s.ctx)
		s.Require().NoError(err)
		got, err := s.repo.GetByKeyForAuth(s.ctx, key.Key)
		s.Require().NoError(err)
		s.Require().NotNil(got.User.OverdraftLimit)
		s.Require().Equal(limit, *got.User.OverdraftLimit)
	}
}

func (s *UserRepoSuite) TestOverdraftLimit_PersistsOverrideAndInheritance() {
	user := s.mustCreateUser(&service.User{Email: "overdraft-setting@example.test", Balance: 20})
	current, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Nil(current.OverdraftLimit)
	for _, limit := range []float64{10, 0} {
		current.OverdraftLimit = &limit
		s.Require().NoError(s.repo.DeductBalance(s.ctx, user.ID, 1))
		s.Require().NoError(s.repo.Update(s.ctx, current, service.UserUpdateFields{OverdraftLimit: true}))
		current, err = s.repo.GetByID(s.ctx, user.ID)
		s.Require().NoError(err)
		s.Require().NotNil(current.OverdraftLimit)
		s.Require().Equal(limit, *current.OverdraftLimit)
	}
	s.Require().Equal(18.0, current.Balance)
	current.OverdraftLimit = nil
	s.Require().NoError(s.repo.Update(s.ctx, current, service.UserUpdateFields{OverdraftLimit: true}))
	current, err = s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Nil(current.OverdraftLimit)
	s.Require().Equal(18.0, current.Balance)
}
