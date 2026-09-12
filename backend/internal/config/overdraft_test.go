package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLoadDefaultOverdraftLimit(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  float64
		valid bool
	}{
		{"0", 0, true}, {"10.5", 10.5, true}, {"-1", 0, false}, {"NaN", 0, false}, {"+Inf", 0, false}, {"1000000000000", 0, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("BILLING_DEFAULT_OVERDRAFT_LIMIT", tc.value)
			cfg, err := Load()
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, tc.want, cfg.Billing.DefaultOverdraftLimit)
			} else {
				require.Error(t, err)
			}
		})
	}
}
