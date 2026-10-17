package control

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

func TestJoinedEVWaitsForMeasuredBatteryBackoffThenResumes(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	const houseW, desiredEVW = 5000.0, 10350.0
	actualBattery, actualEV := 8000.0, 0.0
	config := loadpoint.Config{ID: "garage", DriverName: "charger", MinChargeW: 4140, MaxChargeW: desiredEVW,
		PhaseMode: "3p", AllowedStepsW: []float64{0, 4140, 4830, 5520, 6210, 6900, 7590, 8280, 8970, 9660, 10350}}
	mgr := loadpoint.NewManager()
	mgr.Load([]loadpoint.Config{config})
	store := telemetry.NewStore()
	dir := SlotDirective{SlotStart: now, SlotEnd: now.Add(15 * time.Minute), BatteryEnergyWh: 2500, Strategy: "arbitrage"}
	st := newStateWithEnergyDispatch(dir, "meter")
	st.clock = func() time.Time { return now }
	st.SiteFuseAmps, st.SiteFuseVoltage, st.SiteFusePhases, st.SiteFuseSafetyA = 25, 230, 3, 1
	st.SlewEnabled = false
	st.DriverLimits = map[string]PowerLimits{"pixii": {MaxChargeW: 10000, MaxDischargeW: 10000}}
	var sampledPhaseA float64
	controller := loadpoint.NewController(mgr,
		func(time.Time) (loadpoint.Directive, bool) { return loadpoint.Directive{}, false },
		func(string) (loadpoint.EVSample, bool) {
			return loadpoint.EVSample{Connected: true, RequestActive: true, PowerW: actualEV, PowerAt: now, SessionWhUnavailable: true}, true
		},
		func(_ context.Context, _ string, payload []byte) error {
			var cmd struct {
				Action string  `json:"action"`
				PowerW float64 `json:"power_w"`
			}
			if err := json.Unmarshal(payload, &cmd); err != nil {
				return err
			}
			if cmd.Action == "ev_set_current" {
				actualEV = cmd.PowerW
				// The physical battery may ignore a reduction. Check this at
				// the EV send, before a later battery command can conceal it.
				if phase := (houseW + actualBattery + actualEV) / 690; phase > 24.001 {
					t.Fatalf("EV spent unmeasured battery relief: %.1f A", phase)
				}
			}
			return nil
		})
	controller.SetSiteFuse(loadpoint.SiteFuse{MaxAmps: 25, Voltage: 230, PhaseCnt: 3})
	controller.SetManualHold(config.ID, loadpoint.ManualHold{PowerW: desiredEVW, PhaseMode: "3p", Persistent: true})
	controller.SetPerPhaseMeterAmps(func() (float64, float64, float64, bool) { return sampledPhaseA, sampledPhaseA, sampledPhaseA, true })
	controller.SetFuseEVMax(func() (float64, bool) { return EVFuseBudget(store, st, mgr.States(), mgr.Configs(), 17250) })
	emit := func(driver string, kind telemetry.DerType, watts float64) {
		d := map[string]any{"control_power_available": true, "control_power_observed_at": now.Format(time.RFC3339Nano)}
		if kind == telemetry.DerMeter {
			d["l1_a"], d["l2_a"], d["l3_a"] = sampledPhaseA, sampledPhaseA, sampledPhaseA
		}
		data, _ := json.Marshal(d)
		soc := .5
		store.Update(driver, kind, watts, &soc, data)
		store.Get(driver, kind).UpdatedAt = now
		store.RecordDriverSuccess(driver)
	}
	var obeyedAt, resumedAt time.Time
	for tick := 0; tick < 30; tick++ {
		now = now.Add(5 * time.Second)
		if tick == 3 {
			actualBattery, obeyedAt = 0, now // Only now does the battery obey.
		}
		sampledPhaseA = (houseW + actualBattery + actualEV) / 690
		emit("pixii", telemetry.DerBattery, actualBattery)
		emit(config.DriverName, telemetry.DerEV, actualEV)
		emit("meter", telemetry.DerMeter, houseW+actualBattery+actualEV)
		controller.TickWithDispatch(context.Background(), now, true)
		st.EVCommandedW = loadpoint.CommandedEVPowerW(mgr.States())
		st.SetEVFuseDemands(controller.FuseDemands())
		targets := ComputeDispatch(store, st, caps(map[string]float64{"pixii": 15000}), 17250)
		if len(targets) != 1 || targets[0].TargetW > 1210.001 {
			t.Fatalf("tick %d: battery did not yield to the EV: %+v", tick, targets)
		}
		if actualEV > 0 && resumedAt.IsZero() {
			resumedAt = now
		}
		if !obeyedAt.IsZero() {
			actualBattery = math.Max(0, targets[0].TargetW)
		}
		if (houseW+actualBattery+actualEV)/690 > 24.001 {
			t.Fatalf("tick %d: battery took the EV's headroom: EV %.0f battery %.0f W", tick, actualEV, actualBattery)
		}
	}
	if resumedAt.IsZero() || resumedAt.Sub(obeyedAt) < evRestartDwell {
		t.Fatalf("EV resumed at %v, battery obeyed at %v: want a %v dwell on measured room", resumedAt, obeyedAt, evRestartDwell)
	}
	if actualEV < 9660 {
		t.Fatalf("EV stuck at %.0f W after recovery", actualEV)
	}
}

// Whatever the EV guard may still allow, our battery orders keep the total
// inside 24 A, in either order of arrival.
func TestEVBatteryCommandOrdersStayWithinMeasuredHeadroom(t *testing.T) {
	rng := rand.New(rand.NewSource(1667))
	for sample := 0; sample < 300; sample++ {
		house := rng.Float64() * 5000
		first, second := rng.Float64()*4000, rng.Float64()*4000
		ev := rng.Float64() * 3000
		grid := house + first + second + ev
		requestedEV := rng.Float64() * 11040
		actualEV := math.Max(ev, math.Min(requestedEV, ev+16560-grid))
		amps := grid / 690
		store, state := evSplitFixture(t, grid, []float64{amps, amps, amps}, requestedEV, "3p")
		sent(state, "ev", ev)
		sent(state, "first", first)
		sent(state, "second", second)
		out := clampEVBatteryHeadroom([]DispatchTarget{{Driver: "first", TargetW: rng.Float64() * 10000}, {Driver: "second", TargetW: rng.Float64() * 10000}}, store, state, 17250)
		for _, order := range [][2]int{{0, 1}, {1, 0}} {
			batteries := [2]float64{first, second}
			for _, i := range order {
				// A battery may ignore a reduction while its sibling obeys.
				batteries[i] = math.Max(batteries[i], out[i].TargetW)
				if total := house + actualEV + batteries[0] + batteries[1]; total > 16560.001 {
					t.Fatalf("sample %d order %v: grid %.1f W exceeds 24 A; before %.1f W targets %+v", sample, order, total, grid, out)
				}
			}
		}
	}
}

// Battery orders the meter may not show yet, the EV cap and new battery
// orders share one physical budget.
func TestEVBatteryPendingAndNewOrdersShareOnePhysicalBudget(t *testing.T) {
	rng := rand.New(rand.NewSource(1668))
	for sample := 0; sample < 300; sample++ {
		house, first, second, ev := rng.Float64()*3000, rng.Float64()*2000, rng.Float64()*2000, rng.Float64()*2000
		grid := house + first + second + ev
		pendingFirst := first + rng.Float64()*(16560-grid)
		pendingSecond := second + rng.Float64()*(16560-grid-(pendingFirst-first))
		requestedEV := rng.Float64() * 11040
		amps := grid / 690
		store, state := evSplitFixture(t, grid, []float64{amps, amps, amps}, requestedEV, "3p")
		sent(state, "ev", ev)
		sent(state, "first", first, pendingFirst)
		sent(state, "second", second, pendingSecond)
		points := []loadpoint.State{{ID: "car", DriverName: "car", PluggedIn: true, CommandedKnown: true, CommandedW: ev}}
		capW, _ := EVFuseBudget(store, state, points, []loadpoint.Config{{ID: "car", DriverName: "car", PhaseMode: "3p"}}, 17250)
		actualEV := math.Max(ev, math.Min(requestedEV, capW))
		out := clampEVBatteryHeadroom([]DispatchTarget{{Driver: "first", TargetW: rng.Float64() * 10000}, {Driver: "second", TargetW: rng.Float64() * 10000}}, store, state, 17250)
		worst := house + actualEV + math.Max(pendingFirst, out[0].TargetW) + math.Max(pendingSecond, out[1].TargetW)
		if worst > 16560.001 {
			t.Fatalf("sample %d: old/new battery orders and EV reach %.1f W; pending %.1f/%.1f EV %.1f targets %+v", sample, worst, pendingFirst, pendingSecond, actualEV, out)
		}
	}
}
