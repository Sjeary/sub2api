package service

import "math"

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
