package loadpoint

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"testing"
	"time"
)

func TestPhaseVerdict(t *testing.T) {
	base := [3]float64{1.6, 1.7, 2.3}
	plus := func(a, b, c float64) [3]float64 { return [3]float64{base[0] + a, base[1] + b, base[2] + c} }
	for _, c := range []struct {
		name  string
		amps  [3]float64
		drawA float64
		want  int
	}{
		{"one-phase car on L1", plus(7, 0, 0), 7, 1},
		{"one-phase car on L3", plus(0, 0.3, 7), 7, 3},
		{"car limited below the order", plus(5, 0, 0), 5, 1},
		{"three-phase car", plus(7, 7, 7), 21, -1},
		{"two-phase car", plus(7, 7, 0), 14, 0},
		{"kettle while the car draws nothing", plus(9, 0, 0), 0, 0},
		{"car and kettle on one phase", plus(16, 0, 0), 7, 0},
		{"not applied yet", plus(0, 0, 0), 0, 0},
	} {
		if got := phaseVerdict(c.amps, base, 7, c.drawA); got != c.want {
			t.Errorf("%s: verdict %d, want %d", c.name, got, c.want)
		}
	}
}

// The meter marks the session's car: a three-phase order rises by 7 A a
// phase, and 40 s later only L1 rises by 7 A while the charger reads 1.6 kW.
// A three-phase verdict clears it, and the next car starts unknown.
func TestOnePhaseCarMarkedFromTheMeter(t *testing.T) {
	m := NewManager()
	m.Load([]Config{{ID: "garage", DriverName: "easee", MinChargeW: 1600, MaxChargeW: 11100}})
	m.Observe("garage", true, 0, 0, true)
	start := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	base := [3]float64{1.6, 1.7, 2.3}
	order := func(w float64) { m.SetCommanded("garage", w, "plan"); m.setCommandedPhases("garage", 3) }
	run := func(from, to time.Duration, amps func(time.Duration) [3]float64, drawW float64) {
		for d := from; d <= to; d += 5 * time.Second {
			at := start.Add(d)
			m.observePhases("garage", SitePhases{A: amps(d), At: at}, drawW/230, 230, time.Minute, at)
		}
	}
	state := func() State { st, _ := m.State("garage"); return st }
	still := func(time.Duration) [3]float64 { return base }

	order(0)
	run(0, 10*time.Second, still, 0)
	order(4830)
	onL1 := func(d time.Duration) [3]float64 {
		if d < 55*time.Second {
			return base
		}
		return [3]float64{base[0] + 7, base[1], base[2]}
	}
	run(15*time.Second, 80*time.Second, onL1, 1610)
	if st := state(); st.CarPhases != 0 {
		t.Fatal("marked before the meter showed the rise for 30 s")
	}
	run(85*time.Second, 90*time.Second, onL1, 1610)
	if st := state(); st.CarPhases != 1 || st.CarPhase != 1 {
		t.Fatalf("after 35 s on L1 alone: car phases %d on L%d", st.CarPhases, st.CarPhase)
	}
	// A later rise that shows on all three phases is a three-phase car.
	order(11040)
	allThree := func(d time.Duration) [3]float64 {
		if d < 140*time.Second {
			return [3]float64{base[0] + 7, base[1], base[2]}
		}
		return [3]float64{base[0] + 16, base[1] + 9, base[2] + 9}
	}
	run(95*time.Second, 200*time.Second, allThree, 11040)
	if st := state(); st.CarPhases != 0 {
		t.Fatalf("a rise on all three phases left the mark on L%d", st.CarPhase)
	}
	order(4830)
	run(205*time.Second, 210*time.Second, still, 0)
	order(11040)
	run(215*time.Second, 300*time.Second, onL1, 1610)
	m.Observe("garage", false, 0, 0, true)
	m.Observe("garage", true, 0, 0, true)
	if st := state(); st.CarPhases != 0 {
		t.Fatal("the next car would inherit the mark")
	}
}

// Reviewer's P4: a one-phase car on the field table. Without the mark the
// planner got [0, 4800] and the car drew 1.6 kW of a 4.8 kW order. Marked,
// the planner gets the car's one-phase levels up to 3.7 kW.
func TestOnePhaseCarPlansItsOwnLevels(t *testing.T) {
	site := SiteFuse{MaxAmps: 20, Voltage: 230, PhaseCnt: 3}
	cfg := Config{MinChargeW: 1600, MaxChargeW: 11100, PhaseMode: "auto", AllowedStepsW: easeeFieldSteps}
	st := State{MinChargeW: 1600, MaxChargeW: 11100, CarMaxW: 3700}
	_, maxW, steps := LimitPower(st, OnePhaseCarSteps(cfg, site))
	if maxW < 3680 || maxW > 3700 || steps[len(steps)-1] != 3680 || !slices.Contains(steps, 1380) {
		t.Fatalf("one-phase car plan: max %.0f W, steps %v", maxW, steps)
	}
	if got := OnePhaseCarMaxW(Config{MaxChargeW: 11100}, SiteFuse{MaxAmps: 10, Voltage: 230, PhaseCnt: 3}); got != 2300 {
		t.Fatalf("a 10 A fuse holds a one-phase car to %.0f W, want 2300 W", got)
	}
}

// Marked, a plan slot at the car's 3.7 kW goes out as a three-phase order
// of 16 A a phase, and the command says the car should draw a third of it.
func TestOnePhaseCarOrderNamesItsCurrent(t *testing.T) {
	cfg := Config{ID: "garage", DriverName: "easee", MinChargeW: 1600, MaxChargeW: 11100, PhaseMode: "auto", AllowedStepsW: easeeFieldSteps}
	slot := time.Now().Add(-time.Second)
	dir := &Directive{SlotStart: slot, SlotEnd: slot.Add(time.Hour), LoadpointEnergyWh: map[string]float64{cfg.ID: 3680}}
	var payload map[string]any
	send := func(_ context.Context, _ string, p []byte) error { return json.Unmarshal(p, &payload) }
	samples := map[string]EVSample{cfg.DriverName: {Connected: true, RequestActive: true}}
	c := NewController(NewManager(), PlanFunc(func(time.Time) (Directive, bool) { return *dir, true }),
		TelemetryFunc(func(d string) (EVSample, bool) { s, ok := samples[d]; return s, ok }), send)
	c.manager.Load([]Config{cfg})
	c.SetSiteFuse(SiteFuse{MaxAmps: 20, Voltage: 230, PhaseCnt: 3})
	c.manager.Observe(cfg.ID, true, 0, 0, true)
	c.manager.SetSchedule(cfg.ID, Schedule{SoC: .8})
	c.manager.byID[cfg.ID].carPhases.phase = 1
	c.Tick(context.Background(), time.Now())
	power, _ := payload["power_w"].(float64)
	expected, _ := payload["expected_w"].(float64)
	if payload["phase_mode"] != "3p" || power != 11100 || math.Abs(expected-3700) > 1 {
		t.Fatalf("sent %v", payload)
	}
}
