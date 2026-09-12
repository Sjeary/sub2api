package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type overdraftAdminStub struct {
	service.AdminService
	input *service.UpdateUserInput
}

func (s *overdraftAdminStub) UpdateUser(_ context.Context, id int64, input *service.UpdateUserInput) (*service.User, error) {
	s.input = input
	return &service.User{ID: id, OverdraftLimit: input.OverdraftLimit}, nil
}

func TestUserOverdraftUpdateJSONContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		body     string
		provided bool
		value    *float64
		status   int
	}{
		{`{}`, false, nil, 200},
		{`{"overdraft_limit":null}`, true, nil, 200},
		{`{"overdraft_limit":0}`, true, new(float64(0)), 200},
		{`{"overdraft_limit":10}`, true, new(float64(10)), 200},
		{`{"overdraft_limit":"10"}`, false, nil, 400},
		{`{"overdraft_limit":true}`, false, nil, 400},
	} {
		t.Run(tc.body, func(t *testing.T) {
			stub := &overdraftAdminStub{}
			handler := NewUserHandler(stub, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.PUT("/users/:id", handler.Update)
			req := httptest.NewRequest(http.MethodPut, "/users/1", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == 200 {
				require.NotNil(t, stub.input)
				require.Equal(t, tc.provided, stub.input.OverdraftLimitSet)
				require.Equal(t, tc.value, stub.input.OverdraftLimit)
			} else {
				require.Nil(t, stub.input)
			}
		})
	}
}
