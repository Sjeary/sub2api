package handler

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type wsOverdraftUserRepo struct {
	service.UserRepository
	mu   sync.Mutex
	user service.User
}

func TestWebSocketOverdraftRejectionDoesNotPenalizeUpstream(t *testing.T) {
	for _, cause := range []error{service.ErrInsufficientBalance, service.ErrBillingServiceUnavailable} {
		err := service.NewOpenAIWSClientCloseError(websocket.StatusPolicyViolation, "billing check failed", fmt.Errorf("%w: %w", errOpenAIWSBalanceAdmission, cause))
		require.False(t, shouldReportOpenAIWSProxyAccountFailure(err))
	}
}

func (r *wsOverdraftUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user := r.user
	return &user, nil
}

func TestWebSocketOverdraftChecksSubsequentTurns(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeDedicated} {
		for _, tc := range []struct {
			name           string
			balance, limit float64
			reject         bool
		}{
			{"credit exhausted", -10, 10, true},
			{"credit revoked", -1, 0, true},
			{"credit remains", -5, 10, false},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				repo := &wsOverdraftUserRepo{user: service.User{ID: 1701, Status: service.StatusActive, Balance: 1}}
				runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
					firstPayload:  `{"type":"response.create","model":"gpt-5.4","stream":false}`,
					secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":false}`,
					ingressMode:   mode, billingUserRepo: repo, billingSiteLimit: 10,
					secondTurnCloseExpected: tc.reject, secondTurnCloseReason: "billing check failed",
					afterFirstUpstreamRequest: func(*service.ChannelService) error {
						repo.mu.Lock()
						defer repo.mu.Unlock()
						repo.user.Balance = tc.balance
						limit := tc.limit
						repo.user.OverdraftLimit = &limit
						return nil
					},
				})
			})
		}
	}
}
