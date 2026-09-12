//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

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

func TestOverdraftDatabaseConstraints(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@test.example", PasswordHash: "hash"})
	for _, value := range []string{"-0.01", "1000000000000", "NaN", "Infinity"} {
		t.Run(value, func(t *testing.T) {
			_, err := integrationDB.ExecContext(ctx, "UPDATE users SET overdraft_limit = $1::numeric WHERE id = $2", value, user.ID)
			require.Error(t, err)
		})
	}
	for _, value := range []any{nil, "0", "0.00000001", "999999999999"} {
		_, err := integrationDB.ExecContext(ctx, "UPDATE users SET overdraft_limit = $1::numeric WHERE id = $2", value, user.ID)
		require.NoError(t, err)
	}
}

func TestOverdraftConcurrentBillingAndRepayment(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	limit := 10.0
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@test.example", PasswordHash: "hash", Balance: -5, OverdraftLimit: &limit})
	_, err := client.User.UpdateOneID(user.ID).SetOverdraftLimit(limit).Save(ctx)
	require.NoError(t, err)
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "overdraft concurrency"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Type: service.AccountTypeAPIKey})
	billing := NewUsageBillingRepository(client, integrationDB)
	users := newUserRepositoryWithSQL(client, integrationDB)
	const count = 8
	var wg sync.WaitGroup
	errorsCh := make(chan error, count*3)
	start := make(chan struct{})
	for i := range count {
		requestID := uuid.NewString()
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := billing.Apply(ctx, &service.UsageBillingCommand{RequestID: requestID, APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: service.AccountTypeAPIKey, BalanceCost: 0.25})
				errorsCh <- err
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			change, err := users.AdjustBalance(ctx, user.ID, 0.1)
			if err == nil && change.New <= change.Old {
				err = fmt.Errorf("repayment %d did not increase balance", i)
			}
			errorsCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	current, err := users.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, -6.2, current.Balance, 1e-8, "each request bills once; all partial repayments are preserved")
	require.NotNil(t, current.OverdraftLimit)
	require.Equal(t, limit, *current.OverdraftLimit)
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
