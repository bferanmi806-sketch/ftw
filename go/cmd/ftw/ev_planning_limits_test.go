package main

import (
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/config"
	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/internal/mpc"
)

func TestConfiguredEVLimitsReachPlanner(t *testing.T) {
	cfg := &config.Config{Fuse: config.Fuse{MaxAmps: 16, Phases: 3, Voltage: 230}, Loadpoints: []config.Loadpoint{{ID: "garage", PhaseMode: "1p"}}}
	st := loadpoint.State{ID: "garage", MinChargeW: 1380, MaxChargeW: 3680}
	_, _, steps := planningLimitsForLoadpoint(st, cfg)
	if len(steps) != 12 || steps[1] != 1380 || steps[len(steps)-1] != 3680 {
		t.Fatalf("1-phase limits lost on 3-phase site: %v", steps)
	}
	cfg.Loadpoints[0].PhaseMode = "3p"
	st.MinChargeW = 4140
	st.MaxChargeW = 11000
	_, _, steps = planningLimitsForLoadpoint(st, cfg)
	if steps[1] != 4140 || steps[len(steps)-1] != 10350 {
		t.Fatalf("3-phase limits lost: %v", steps)
	}
}

// Device-simulator rig s14: once the car was marked one-phase the plan had
// no charging for it. The limits main.go gives the planner must leave it a
// level to charge at, and the planner must use it.
func TestOnePhaseCarGetsAPlan(t *testing.T) {
	cfg := &config.Config{Fuse: config.Fuse{MaxAmps: 25, Phases: 3, Voltage: 230}, Loadpoints: []config.Loadpoint{{ID: "garage", PhaseMode: "auto"}}}
	st := loadpoint.State{ID: "garage", MinChargeW: 4140, MaxChargeW: 11000, CarPhases: 1}
	minW, maxW, steps := planningLimitsForLoadpoint(st, cfg)
	start := time.Now().Truncate(15 * time.Minute)
	slots := make([]mpc.Slot, 8)
	for i := range slots {
		slots[i] = mpc.Slot{StartMs: start.Add(time.Duration(i) * 15 * time.Minute).UnixMilli(), LenMin: 15, PriceOre: 100,
			Limits: mpc.PowerLimits{MaxImportW: 17250}}
	}
	p := mpc.Params{Mode: mpc.ModeArbitrage, CapacityWh: 10000, SoCMin: .1, SoCMax: .9, InitialSoC: .5, ChargeEfficiency: 1, DischargeEfficiency: 1,
		Loadpoint: &mpc.LoadpointSpec{ID: "garage", CapacityWh: 60000, Levels: 11, SoCMax: .55, InitialSoC: .5, TargetSoC: .55, TargetSlotIdx: 7,
			PluggedIn: true, ChargeEfficiency: 1, MinChargeW: minW, MaxChargeW: maxW, AllowedStepsW: steps}}
	plan := mpc.Optimize(slots, p)
	var wh float64
	for _, a := range plan.Actions {
		wh += a.LoadpointW * 0.25
	}
	if wh < 1380*0.25 {
		t.Fatalf("the plan charges the car %.0f Wh toward a 3,000 Wh goal (limits %.0f-%.0f W)", wh, minW, maxW)
	}
	if minW != 1380 || maxW != 3450 {
		t.Fatalf("one-phase car planned at %.0f-%.0f W, want 1380-3450 W", minW, maxW)
	}
}
