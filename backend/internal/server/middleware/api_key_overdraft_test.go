package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthOverdraftAcrossProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	zero, custom := 0.0, 10.0
	for _, tc := range []struct {
		name          string
		balance, site float64
		override      *float64
		want          int
	}{
		{"disabled by default", 0, 0, nil, 403},
		{"inherit site", -4, 5, nil, 200},
		{"site exhausted", -5, 5, nil, 403},
		{"explicit zero", -1, 5, &zero, 403},
		{"user override", -7, 5, &custom, 200},
		{"user exhausted", -10, 5, &custom, 403},
	} {
		for _, google := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/standard", true: "/google"}[google], func(t *testing.T) {
				apiKeyService := newTestAPIKeyService(fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
					return &service.APIKey{ID: 1, Key: "test-key", Status: service.StatusActive, User: &service.User{ID: 1, Status: service.StatusActive, Balance: tc.balance, OverdraftLimit: tc.override}}, nil
				}})
				cfg := &config.Config{}
				cfg.Billing.DefaultOverdraftLimit = tc.site
				router := gin.New()
				if google {
					router.Use(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, cfg))
				} else {
					router.Use(apiKeyAuthWithSubscription(apiKeyService, nil, cfg))
				}
				router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })
				req := httptest.NewRequest(http.MethodGet, "/test", nil)
				req.Header.Set("Authorization", "Bearer test-key")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				require.Equal(t, tc.want, rec.Code, rec.Body.String())
			})
		}
	}
}
