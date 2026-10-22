package loadpoint

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestOnePhaseDraw(t *testing.T) {
	for _, c := range []struct {
		name  string
		drawW float64
		amps  [3]float64
		want  int
	}{
		{"field case: 3.13 kW on L1", 3130, [3]float64{14.4, .75, .75}, 1},
		{"on L3, the others exporting", 3130, [3]float64{-2, -2, 14}, 3},
		{"export hides part of the car's current", 3130, [3]float64{-2, -2, 12}, 0},
		{"three phases", 3130, [3]float64{5.3, 5.3, 5.3}, -1},
		{"too small to tell", 1500, [3]float64{7.5, .5, .5}, 0},
		{"one phase, house load on L2", 3130, [3]float64{14.4, 4.6, .7}, 0},
		{"three-phase minimum, heavy L1", 4140, [3]float64{19, 6.5, 6.5}, -1},
	} {
		if got := onePhaseDraw(c.drawW, 230, c.amps); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestOnePhaseHoldsForTheSession(t *testing.T) {
	m := NewManager()
	m.Load([]Config{{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11040}})
	m.Observe("garage", true, 3130, 0, true)
	start := time.Now()
	seen := func(phase int, from, to time.Duration, drawW float64) {
		for d := from; d <= to; d += 5 * time.Second {
			m.observeOnePhase("garage", phase, drawW, 7590, start.Add(d))
		}
	}
	state := func() State { st, _ := m.State("garage"); return st }

	seen(1, 0, 115*time.Second, 3130)
	if st := state(); st.OnePhase != 0 {
		t.Fatalf("one phase after under two minutes: %+v", st.OnePhase)
	}
	// A hole in the readings starts the run again.
	seen(1, 160*time.Second, 270*time.Second, 3130)
	if st := state(); st.OnePhase != 0 {
		t.Fatal("a run with a 45 s hole counted as two minutes")
	}
	seen(1, 275*time.Second, 285*time.Second, 3100)
	if st := state(); st.OnePhase != 1 || st.OnePhaseW != 3100 || st.OnePhaseOrderW != 7590 {
		t.Fatalf("after two minutes: phase %d at %.0f W, order %.0f W", st.OnePhase, st.OnePhaseW, st.OnePhaseOrderW)
	}
	seen(1, 290*time.Second, 290*time.Second, 3130)
	if st := state(); st.OnePhaseW != 3130 {
		t.Fatalf("rate kept at %.0f W, want the most delivered", st.OnePhaseW)
	}
	seen(-1, 295*time.Second, 410*time.Second, 9000)
	if st := state(); st.OnePhase != 1 {
		t.Fatal("cleared before two minutes of three-phase readings")
	}
	seen(-1, 415*time.Second, 415*time.Second, 9000)
	if st := state(); st.OnePhase != 0 || st.OnePhaseW != 0 {
		t.Fatal("two minutes of three-phase readings did not clear it")
	}
	seen(1, 420*time.Second, 540*time.Second, 3130)
	m.Observe("garage", false, 0, 0, true)
	if st := state(); st.OnePhase != 0 {
		t.Fatal("the next car would inherit the one-phase rate")
	}
}

// The plan counts what one phase delivers. The goal's need does too, so the
// box tells the household a goal is at risk from the same rate.
func TestLimitPowerCountsOnePhaseRate(t *testing.T) {
	steps := []float64{0, 4140, 4830, 5520, 6210, 6900, 7590, 8280, 8970, 9660, 10350, 11040}
	st := State{PluggedIn: true, MinChargeW: 4140, MaxChargeW: 11040, AllowedStepsW: steps, OnePhase: 1, OnePhaseW: 3130, OnePhaseOrderW: 7590}
	minW, maxW, out := LimitPower(st, steps)
	if minW != 3130 || maxW != 3130 || !slices.Equal(out, []float64{0, 3130}) {
		t.Fatalf("plan power %.0f..%.0f W, steps %v", minW, maxW, out)
	}
	st.TargetSoC, st.CurrentSoC, st.VehicleCapacityWh, st.TargetTime = .8, .3, 60000, time.Now().Add(8*time.Hour)
	if _, powerW, ok := DeadlineNeed(st); !ok || powerW != 3130 {
		t.Fatalf("goal need at %.0f W (ok %t), want 3130 W", powerW, ok)
	}
}

// A plan slot at the one-phase rate is 3,130 W. Snapped to the charger's
// three-phase steps that is 4,140 W, 6 A a phase, which a charger stuck on
// one phase turns into 1,380 W. The order that delivered the rate goes out
// instead, until the slot's energy is spent.
func TestOnePhaseRepeatsTheDeliveringOrder(t *testing.T) {
	steps := []float64{0, 4140, 4830, 5520, 6210, 6900, 7590, 8280, 8970, 9660, 10350, 11040}
	cfgs := []Config{{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11040, AllowedStepsW: steps}}
	slotStart := time.Now().Add(-time.Second)
	directive := &Directive{SlotStart: slotStart, SlotEnd: slotStart.Add(15 * time.Minute),
		LoadpointEnergyWh: map[string]float64{"garage": 3130.0 / 4}}
	samples := map[string]EVSample{"easee": {PowerW: 3130, Connected: true}}
	for _, c := range []struct {
		name  string
		phase int
		want  float64
	}{{"three phases", 0, 4140}, {"one phase", 1, 7590}} {
		sender := &fakeSender{}
		ctrl := newTestController(t, cfgs, directive, samples, sender)
		ctrl.manager.Observe("garage", true, 3130, 0, true)
		if c.phase > 0 {
			ctrl.manager.byID["garage"].onePhase = onePhaseState{phase: c.phase, rateW: 3130, orderW: 7590}
		}
		ctrl.Tick(context.Background(), time.Now())
		if len(sender.calls) != 1 || sender.calls[0].power != c.want {
			t.Fatalf("%s: sent %+v, want %.0f W", c.name, sender.calls, c.want)
		}
	}
}
