package loadpoint

import (
	"testing"
	"time"
)

// A charger with no step table, as the settings page writes a new one,
// takes any power from its minimum to its maximum. Surplus used to snap
// every order to its only step, 0 W, so the car never charged from
// surplus. It orders the surplus and still pauses below the minimum.
func TestSurplusWithoutAStepTableOrdersTheSurplus(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cfg := Config{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11000, PhaseMode: "3p", SurplusOnly: true}
	for _, tc := range []struct {
		surplusW, want float64
	}{{6000, 6000}, {2000, 0}} {
		c := NewController(NewManager(), nil, nil, nil)
		c.SetSiteSurplusForEV(func() (float64, bool) { return tc.surplusW, true })
		if got := c.computeSurplusCmd(now, cfg, cfg.MaxChargeW, nil); got != tc.want {
			t.Errorf("%.0f W of surplus: order %.0f W, want %.0f W", tc.surplusW, got, tc.want)
		}
	}
}
