package control

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

type evTimed struct {
	at time.Time
	w  float64
}

// evMeterSite runs one Easee-like 3p charger with Charge now against a
// simulated 25 A site. A new current takes chargerDelay to reach the car.
// Like Easee Cloud, the driver polls every 5 s and receives the draw as it
// was telemetryDelay ago. The meter reads the physical import on every
// one-second tick, and the loadpoint ticks right after it, as in main.go.
type evMeterSite struct {
	t                            *testing.T
	now                          time.Time
	store                        *telemetry.Store
	state                        *State
	mgr                          *loadpoint.Manager
	ctrl                         *loadpoint.Controller
	chargerDelay, telemetryDelay time.Duration
	houseW                       float64
	housePhaseA                  [3]float64
	carMaxW                      float64 // the car takes at most this
	carPaused, chargerIgnores    bool
	appliedW, evW                float64
	reportedW                    float64
	reportedAt, nextPoll         time.Time
	commands, draws              []evTimed
	offers                       []evTimed // every ev_set_current
	maxGridW                     float64
}

const evMeterSteps = "0,4140,4830,5520,6210,6900,7590,8280,8970,9660,10350"

func newEVMeterSite(t *testing.T, ceilingW, houseW float64) *evMeterSite {
	t.Helper()
	s := &evMeterSite{t: t, now: time.Now().Truncate(time.Second), store: telemetry.NewStore(),
		chargerDelay: 5 * time.Second, telemetryDelay: 10 * time.Second, carMaxW: math.Inf(1)}
	s.setHouse(houseW)
	s.state = NewState(0, 0, "meter")
	s.state.SiteFuseAmps, s.state.SiteFuseVoltage, s.state.SiteFusePhases, s.state.SiteFuseSafetyA = 25, 230, 3, 0.5
	s.state.PeakImportCeilingW = ceilingW
	s.state.clock = func() time.Time { return s.now }
	cfg := loadpoint.Config{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 10350, PhaseMode: "3p",
		AllowedStepsW: []float64{0, 4140, 4830, 5520, 6210, 6900, 7590, 8280, 8970, 9660, 10350}}
	s.mgr = loadpoint.NewManager()
	s.mgr.Load([]loadpoint.Config{cfg})
	s.ctrl = loadpoint.NewController(s.mgr,
		func(time.Time) (loadpoint.Directive, bool) { return loadpoint.Directive{}, false },
		func(string) (loadpoint.EVSample, bool) {
			return loadpoint.EVSample{Connected: true, RequestActive: true, PowerW: s.reportedW, PowerAt: s.reportedAt, SessionWhUnavailable: true}, true
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
				s.offers = append(s.offers, evTimed{s.now, cmd.PowerW})
				s.commands = append(s.commands, evTimed{s.now.Add(s.chargerDelay), cmd.PowerW})
			}
			return nil
		})
	s.ctrl.SetSiteFuse(loadpoint.SiteFuse{MaxAmps: 25, Voltage: 230, PhaseCnt: 3})
	s.ctrl.SetPerPhaseMeterAmps(func() (float64, float64, float64, bool) {
		a := s.phaseA()
		return a[0], a[1], a[2], true
	})
	s.ctrl.SetFuseEVMax(func() (float64, bool) {
		return EVFuseBudget(s.store, s.state, s.mgr.States(), s.mgr.Configs(), 17250)
	})
	s.ctrl.SetManualHold(cfg.ID, loadpoint.ManualHold{PowerW: 10350, PhaseMode: "3p", Persistent: true})
	s.draws = []evTimed{{s.now.Add(-time.Hour), 0}}
	s.report(0)
	return s
}

func (s *evMeterSite) setHouse(w float64) {
	s.houseW = w
	a := w / 690
	s.housePhaseA = [3]float64{a, a, a}
}

func (s *evMeterSite) phaseA() [3]float64 {
	a := s.housePhaseA
	for i := range a {
		a[i] += s.evW / 690
	}
	return a
}

func (s *evMeterSite) emit(driver string, kind telemetry.DerType, w float64, extra map[string]any, at time.Time) {
	d := map[string]any{"control_power_available": true, "control_power_observed_at": at.Format(time.RFC3339Nano)}
	for k, v := range extra {
		d[k] = v
	}
	data, _ := json.Marshal(d)
	s.store.Update(driver, kind, w, nil, data)
	s.store.Get(driver, kind).UpdatedAt = s.now // Receipt on the simulated clock.
	s.store.RecordDriverSuccess(driver)
}

// report delivers one poll. The receipt time is all the driver knows.
func (s *evMeterSite) report(w float64) {
	s.reportedW, s.reportedAt = w, s.now
	s.emit("easee", telemetry.DerEV, w, nil, s.now)
}

func (s *evMeterSite) cloudW() float64 {
	w, seen := 0.0, s.now.Add(-s.telemetryDelay)
	for _, d := range s.draws {
		if !d.at.After(seen) {
			w = d.w
		}
	}
	return w
}

func (s *evMeterSite) step() {
	s.now = s.now.Add(time.Second)
	for len(s.commands) > 0 && !s.commands[0].at.After(s.now) {
		if !s.chargerIgnores {
			s.appliedW = s.commands[0].w
		}
		s.commands = s.commands[1:]
	}
	draw := math.Min(s.appliedW, s.carMaxW)
	if s.carPaused {
		draw = 0
	}
	if draw != s.evW {
		s.evW = draw
		s.draws = append(s.draws, evTimed{s.now, draw})
	}
	if !s.now.Before(s.nextPoll) {
		s.report(s.cloudW())
		s.nextPoll = s.now.Add(5 * time.Second)
	}
	a := s.phaseA()
	grid := s.houseW + s.evW
	s.maxGridW = math.Max(s.maxGridW, grid)
	s.emit("meter", telemetry.DerMeter, grid, map[string]any{"l1_a": a[0], "l2_a": a[1], "l3_a": a[2]}, s.now)
	s.ctrl.TickWithDispatch(context.Background(), s.now, true)
	s.state.SetEVFuseDemands(s.ctrl.FuseDemands())
}

func (s *evMeterSite) run(d time.Duration) {
	for end := s.now.Add(d); s.now.Before(end); {
		s.step()
	}
}

func (s *evMeterSite) offer() float64 {
	if len(s.offers) == 0 {
		return 0
	}
	return s.offers[len(s.offers)-1].w
}

func (s *evMeterSite) offersSince(at time.Time) []float64 {
	var out []float64
	for _, o := range s.offers {
		if !o.at.Before(at) {
			out = append(out, o.w)
		}
	}
	return out
}

// steady brings the car to a constant draw with the telemetry caught up.
func (s *evMeterSite) steady(wantOffer float64) {
	s.t.Helper()
	s.run(3 * time.Minute)
	if s.offer() != wantOffer || s.evW != wantOffer || s.reportedW != wantOffer {
		s.t.Fatalf("setup: offer %.0f draw %.0f reported %.0f, want %.0f W", s.offer(), s.evW, s.reportedW, wantOffer)
	}
}

// #1674, 05:49:15: the import ceiling was off and the car drew 10,242 W at
// 15 A. One 0 W Easee sample arrived while Pixii read 20.45/17.05/15.87 A
// and 12,323 W on the 25 A fuse. Proof-based credit gave
// (25-1-20.45)x230x3 = 2,449.5 W, below the 4,140 W step, and stopped the
// charge. The meter shows room on every phase, so the offer must hold.
func TestEVHardLimitHoldsOfferThroughLoneZeroSample(t *testing.T) {
	s := newEVMeterSite(t, 0, 2081)
	s.carMaxW = 10242
	s.housePhaseA = [3]float64{20.45 - 10242.0/690, 17.05 - 10242.0/690, 15.87 - 10242.0/690}
	s.run(3 * time.Minute)
	if s.offer() != 10350 || s.evW != 10242 {
		t.Fatalf("setup: offer %.0f draw %.0f", s.offer(), s.evW)
	}
	if a := s.phaseA(); math.Abs(a[0]-20.45) > .01 || math.Abs(s.houseW+s.evW-12323) > .01 {
		t.Fatalf("setup: L1 %.2f A grid %.0f W", a[0], s.houseW+s.evW)
	}
	zeroAt := s.now
	s.report(0) // 05:49:13, the lone zero
	s.step()
	capW, _ := EVFuseBudget(s.store, s.state, s.mgr.States(), s.mgr.Configs(), 17250)
	s.report(10242) // 05:49:18
	s.run(time.Minute)
	if capW < 10350 {
		t.Fatalf("lone 0 W sample cut the hard cap to %.1f W", capW)
	}
	for _, w := range s.offersSince(zeroAt) {
		if w != 10350 {
			t.Fatalf("offer left 10,350 W after a lone 0 W sample: %v", s.offersSince(zeroAt))
		}
	}
}

// #1674, 23:45: a 7.5 kW import ceiling, the car at 9 A beside 0.9 kW of
// house load, and Easee telemetry 5-15 s late. The charge started and
// stopped every 30 s. It must start once and stay inside the ceiling.
func TestEVHardLimitStartsOnceUnderImportCeilingWithLateTelemetry(t *testing.T) {
	for _, delay := range []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			s := newEVMeterSite(t, 7500, 900)
			s.telemetryDelay = delay
			s.run(5 * time.Minute)
			started := false
			for _, o := range s.offers {
				if o.w > 0 {
					started = true
				} else if started {
					t.Fatalf("start/stop cycle: offers %v", s.offersSince(time.Time{}))
				}
			}
			if !started || s.offer() != 6210 {
				t.Fatalf("offer %.0f W, want a steady 6,210 W", s.offer())
			}
			if s.maxGridW > 7500 {
				t.Fatalf("grid reached %.0f W above the 7,500 W ceiling", s.maxGridW)
			}
		})
	}
}

// A real house step pushes the grid to 8.15 kW under the 7.5 kW ceiling.
// The offer steps down by the measured overage, once, and never stops.
func TestEVHardLimitHouseStepStepsDownOnce(t *testing.T) {
	s := newEVMeterSite(t, 7500, 900)
	s.steady(6210)
	stepAt := s.now
	s.setHouse(1940) // 1,940 + 6,210 = 8,150 W
	s.run(evSettle + 10*time.Second)
	got := s.offersSince(stepAt)
	for _, w := range got {
		if w != 6210 && w != 5520 {
			t.Fatalf("house step gave offers %v, want one step to 5,520 W", got)
		}
	}
	if s.offer() != 5520 || s.houseW+s.evW > 7500 {
		t.Fatalf("offer %.0f W grid %.0f W after the step", s.offer(), s.houseW+s.evW)
	}
}

// The car pauses by itself, the house rises into the room it left, and the
// car resumes. Nothing stopped it while paused; the next meter reading cuts.
func TestEVHardLimitCutsOnNextReadingWhenCarResumesIntoHouseLoad(t *testing.T) {
	s := newEVMeterSite(t, 7500, 900)
	s.steady(6210)
	s.carPaused = true
	s.run(30 * time.Second)
	s.setHouse(5000)
	s.run(30 * time.Second)
	offered := s.offer()
	if offered < 6210 {
		t.Fatalf("paused car lost its offer: %.0f W", offered)
	}
	s.carPaused = false
	s.step() // The resumed draw is in this reading.
	if over := s.houseW + s.evW - 7500; over <= 0 || s.offer() > math.Max(0, offered-over) {
		t.Fatalf("overage %.0f W left offer %.0f W after the first reading", over, s.offer())
	}
}

// After a cut the cap rises one step per settle time, and only while the
// car takes what it was offered. A paused car does not wind it up.
func TestEVHardLimitRisesOneSeenStepAtATime(t *testing.T) {
	s := newEVMeterSite(t, 7500, 900)
	s.steady(6210)
	s.carPaused = true
	s.run(s.telemetryDelay + 5*time.Second) // The cloud now reports 0 W.
	pausedOffer := s.offer()
	s.setHouse(2600) // Room for the paused car only: no cut.
	s.run(2 * time.Minute)
	if s.offer() != pausedOffer {
		t.Fatalf("cap moved %.0f -> %.0f W while the car reported 0 W", pausedOffer, s.offer())
	}
	s.carPaused = false
	s.run(time.Minute) // 2,600 + 6,210 = 8,810 W: cut to 4,830 W
	if s.offer() != 4830 {
		t.Fatalf("offer %.0f W after a 1,310 W overage", s.offer())
	}
	s.setHouse(900)
	recoveredAt := s.now
	s.run(3 * time.Minute)
	got := s.offersSince(recoveredAt)
	for i := 1; i < len(got); i++ {
		if got[i]-got[i-1] > 690 {
			t.Fatalf("cap rose more than one step at once: %v", got)
		}
	}
	if s.offer() != 6210 || s.houseW+s.evW > 7500 {
		t.Fatalf("recovered to %.0f W at %.0f W grid", s.offer(), s.houseW+s.evW)
	}
}

// A charger that ignores a cut still gets the next one once its old
// current should have reached the meter.
func TestEVHardLimitCutsAgainAfterSettleWhenChargerIgnores(t *testing.T) {
	s := newEVMeterSite(t, 7500, 900)
	s.steady(6210)
	s.chargerIgnores = true
	stepAt := s.now
	s.setHouse(1940)
	s.run(evSettle - 2*time.Second)
	if s.offer() != 5520 {
		t.Fatalf("offers %v, want one cut within the settle time", s.offersSince(stepAt))
	}
	s.run(4 * time.Second)
	if s.offer() >= 5520 {
		t.Fatalf("ignored cut was not repeated: %v", s.offersSince(stepAt))
	}
}

func evMeterBudgetFixture(t *testing.T, gridW float64, phaseA []float64, offerW float64, phase string) (*telemetry.Store, *State, []loadpoint.State, []loadpoint.Config) {
	t.Helper()
	store := telemetry.NewStore()
	data := map[string]any{}
	if phaseA != nil {
		data["l1_a"], data["l2_a"], data["l3_a"] = phaseA[0], phaseA[1], phaseA[2]
	}
	raw, _ := json.Marshal(data)
	store.Update("meter", telemetry.DerMeter, gridW, nil, raw)
	store.RecordDriverSuccess("meter")
	state := NewState(0, 0, "meter")
	state.SiteFuseAmps, state.SiteFuseVoltage, state.SiteFusePhases = 25, 230, 3
	points := []loadpoint.State{{ID: "car", DriverName: "charger", PluggedIn: true, CommandedKnown: true, CommandedW: offerW}}
	configs := []loadpoint.Config{{ID: "car", DriverName: "charger", PhaseMode: phase, MinChargeW: 1380, MaxChargeW: 13110}}
	return store, state, points, configs
}

// A known 3p car at 15 A beside 11 A of house load on L1: 26 A on a 24 A
// working limit. Two amps on three phases come off the offer at once.
func TestEVHardLimitTrimsPhaseOverageFromOffer(t *testing.T) {
	store, state, points, configs := evMeterBudgetFixture(t, 8280, []float64{26, 20, 20}, 10350, "3p")
	got, known := EVFuseBudget(store, state, points, configs, 17250)
	if !known || math.Abs(got-8970) > .01 {
		t.Fatalf("cap %.1f W, want 10,350 - 2 A x 690 = 8,970 W", got)
	}
}

// An overage larger than the offer less one step stops the car; a smaller
// one never does.
func TestEVHardLimitStopsOnlyWhenOverageExceedsOfferLessMinimum(t *testing.T) {
	store, state, points, configs := evMeterBudgetFixture(t, 30000, nil, 10350, "3p")
	if got, _ := EVFuseBudget(store, state, points, configs, 17250); got != 0 {
		t.Fatalf("13 kW overage left %.1f W", got)
	}
	store, state, points, configs = evMeterBudgetFixture(t, 17250+500, nil, 10350, "3p")
	if got, _ := EVFuseBudget(store, state, points, configs, 17250); math.Abs(got-9850) > .01 {
		t.Fatalf("500 W overage gave %.1f W, want 9,850 W", got)
	}
}

// An idle car starts only with room for its minimum and a margin step, and
// a single-phase or auto car may land its whole offer on the worst phase.
func TestEVHardLimitIdleStartNeedsRoomOnDeclaredPhases(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		worstA      float64
		want        float64
	}{
		{"3p_room", "3p", 5, (24 - 5) * 690},
		{"legacy_default", "", 5, (24 - 5) * 690},
		{"1p_room", "1p", 5, (24 - 5) * 230},
		{"auto_room", "auto", 5, (24 - 5) * 230},
		{"3p_below_minimum", "3p", 21.5, 0}, // 1,725 W < 1,380 + 690 W
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, state, points, configs := evMeterBudgetFixture(t, tc.worstA*690, []float64{tc.worstA, tc.worstA, tc.worstA}, 0, tc.phase)
			got, known := EVFuseBudget(store, state, points, configs, 17250)
			if !known || math.Abs(got-tc.want) > .01 {
				t.Fatalf("cap %.1f W, want %.1f W", got, tc.want)
			}
		})
	}
}

// Missing or unreadable EV telemetry is not a reason to cut, and before the
// first command a fresh reading bounds what the meter contains.
func TestEVHardLimitIgnoresEVTelemetryForCuts(t *testing.T) {
	for name, samples := range map[string][]string{
		"lone_zero":   {`{"control_power_available":true}`, `{"control_power_available":true}`},
		"unavailable": {`{"control_power_available":false}`},
	} {
		store, state, points, configs := evMeterBudgetFixture(t, 12323, []float64{20.45, 17.05, 15.87}, 10350, "3p")
		for i, data := range samples {
			w := 0.0
			if i < len(samples)-1 {
				w = 10242
			}
			store.Update("charger", telemetry.DerEV, w, nil, []byte(data))
			store.RecordDriverSuccess("charger")
		}
		if got, _ := EVFuseBudget(store, state, points, configs, 17250); got < 10350 {
			t.Fatalf("%s EV power cut the cap to %.1f W", name, got)
		}
	}
	store, state, points, configs := evMeterBudgetFixture(t, 12323, []float64{20.45, 17.05, 15.87}, 0, "3p")
	points[0].CommandedKnown = false
	store.Update("charger", telemetry.DerEV, 10242, nil, nil)
	store.RecordDriverSuccess("charger")
	if got, _ := EVFuseBudget(store, state, points, configs, 17250); got < 10242 {
		t.Fatalf("restart with the car charging cut it to %.1f W", got)
	}
}
