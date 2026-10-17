package control

import (
	"math"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

var hourTestZone = time.FixedZone("CEST", 2*60*60)

// hourSite is the #1674 site with the ceiling on the clock hour's mean
// import, fed every second as main.go feeds it every tick. hourWh is the
// true import per clock hour, as state.db records it.
type hourSite struct {
	*evMeterSite
	hourWh map[time.Time]float64
	from   time.Time
}

func newHourSite(t *testing.T, ceilingW, houseW float64, start time.Time) *hourSite {
	t.Helper()
	h := &hourSite{hourWh: map[time.Time]float64{}, from: start}
	h.evMeterSite = newEVMeterSiteAt(t, ceilingW, houseW, start)
	s := h.evMeterSite
	s.onBoot = func() { s.state.importHourZone = hourTestZone }
	s.onBoot()
	s.beforeTick = func() {
		h.hourWh[importHourStart(s.now, hourTestZone)] += math.Max(0, s.houseW+s.evW) / 3600
		s.state.ObserveSiteImport(s.store)
	}
	return h
}

// restart boots Core again. With seed, it reads the hour's record first.
func (h *hourSite) restart(seed bool) {
	h.boot()
	if seed {
		start := importHourStart(h.now, hourTestZone)
		from := start
		if h.from.After(from) {
			from = h.from
		}
		h.state.SeedHourImport(h.now, h.hourWh[start], h.now.Sub(from))
	}
}

func (h *hourSite) plugIn() {
	h.unplugged = false
	h.ctrl.SetManualHold("garage", loadpoint.ManualHold{PowerW: 10350, PhaseMode: "3p", Persistent: true})
}

func (h *hourSite) meanW(hour time.Time) float64 { return h.hourWh[hour] }

func (h *hourSite) noStopAfterStart() {
	h.t.Helper()
	started := false
	for _, o := range h.offers {
		if o.w > 0 {
			started = true
		} else if started {
			h.t.Fatalf("the car stopped at %s: offers %v", o.at.In(hourTestZone).Format("15:04:05"), offerWatts(h.offers))
		}
	}
	if !started {
		h.t.Fatal("the car never started")
	}
}

func (h *hourSite) logChanges() {
	last := -1.0
	for _, o := range h.offers {
		if o.w != last {
			h.t.Logf("%s offer %.0f W", o.at.In(hourTestZone).Format("15:04:05"), o.w)
			last = o.w
		}
	}
	for hour, wh := range h.hourWh {
		h.t.Logf("hour %s mean %.0f W", hour.Format("15:04"), wh)
	}
}

func clock(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, hourTestZone)
}

// #1674, 23:45: a 7.5 kW ceiling, 0.9 kW of house load and the car at 9 A,
// 6.2 kW. On the hour's mean the charge runs steady, a step above what a
// per-second ceiling allowed, and never stops.
func TestImportCeilingHourMeanKeeps2345ChargeSteady(t *testing.T) {
	h := newHourSite(t, 7500, 900, clock(16, 22, 0))
	h.run(time.Hour)
	steadyFrom := h.now.Add(time.Minute)
	h.run(90 * time.Minute) // past 23:45 and midnight
	h.logChanges()
	h.noStopAfterStart()
	for _, o := range h.offersSince(steadyFrom) {
		if o.w != 6210 {
			t.Fatalf("offer %.0f W at %s, want a steady 6,210 W", o.w, o.at.In(hourTestZone).Format("15:04:05"))
		}
	}
	if h.evW != 6210 {
		t.Fatalf("car draws %.0f W, want 6,210 W", h.evW)
	}
	for _, hour := range []time.Time{clock(16, 22, 0), clock(16, 23, 0)} {
		if mean := h.meanW(hour); mean > 7500 {
			t.Fatalf("hour %s mean import %.0f W over the 7,500 W ceiling", hour.Format("15:04"), mean)
		}
	}
}

// A quiet first half-hour leaves budget. The car plugged in at 00:30 runs
// at full power, above the ceiling, and the hour's mean stays under it.
func TestImportCeilingQuietHalfHourLetsCarRunAboveCeiling(t *testing.T) {
	h := newHourSite(t, 7500, 900, clock(17, 0, 0))
	h.unplugged = true
	h.run(30 * time.Minute)
	h.plugIn()
	h.run(30*time.Minute - time.Second) // to 00:59:59
	h.logChanges()
	h.noStopAfterStart()
	if h.evW != 10350 || h.houseW+h.evW <= 7500 {
		t.Fatalf("car draws %.0f W, grid %.0f W: want 10,350 W, above the ceiling", h.evW, h.houseW+h.evW)
	}
	h.step() // 01:00
	if mean := h.meanW(clock(17, 0, 0)); mean > 7500 {
		t.Fatalf("hour mean import %.0f W over the 7,500 W ceiling", mean)
	}
}

// A house step late in the hour, as at 02:19 in #1674 but larger, takes
// the car down a step. The hour's budget still holds, so the car does not
// stop: under a per-second 7.5 kW ceiling, 4 kW of house load leaves less
// than its 4.14 kW minimum.
func TestImportCeilingHouseStepLateInHourStepsCarDown(t *testing.T) {
	h := newHourSite(t, 7500, 900, clock(16, 22, 0))
	h.run(110 * time.Minute) // steady at 6,210 W from 23:00
	stepAt := h.now
	h.setHouse(4000)
	h.run(10*time.Minute - time.Second) // to 23:59:59
	h.logChanges()
	h.noStopAfterStart()
	got := h.offersSince(stepAt)
	if len(got) == 0 || got[len(got)-1].w >= 6210 {
		t.Fatalf("offers after the house step %v, want a step down", offerWatts(got))
	}
	h.step() // midnight
	if mean := h.meanW(clock(16, 23, 0)); mean > 7500 {
		t.Fatalf("hour mean import %.0f W over the 7,500 W ceiling", mean)
	}
}

// A Core restart mid-hour keeps the hour's import from state.db. A sauna
// spent most of the budget; without the record the box would count the
// first half-hour at the ceiling and let the car overrun the hour.
func TestImportCeilingRestartKeepsHoursImport(t *testing.T) {
	run := func(seed bool) (*hourSite, float64) {
		h := newHourSite(t, 7500, 9500, clock(16, 23, 0))
		h.run(25 * time.Minute)
		h.setHouse(900)
		h.run(5 * time.Minute)
		h.restart(seed)
		h.run(30*time.Minute - time.Second)
		h.step() // midnight
		return h, h.meanW(clock(16, 23, 0))
	}
	h, mean := run(true)
	h.logChanges()
	if mean > 7500 {
		t.Fatalf("with the record the hour's mean import is %.0f W, over the 7,500 W ceiling", mean)
	}
	if _, without := run(false); without <= 7500 {
		t.Fatalf("without the record the hour's mean is %.0f W; the test no longer needs the record", without)
	}
}

// The restart itself: the hour's import is the record, not zero, and the
// limits continue from it.
func TestImportCeilingSeedRestoresHoursImport(t *testing.T) {
	h := newHourSite(t, 7500, 9500, clock(16, 23, 0))
	h.run(25 * time.Minute)
	h.setHouse(900)
	h.run(5 * time.Minute)
	before := h.state.importHour.importedWh
	limitBefore, _, _ := h.state.importHourLimitsW()
	h.restart(true)
	h.step()
	after := h.state.importHour.importedWh
	if math.Abs(after-before) > 10 {
		t.Fatalf("hour's import %.0f Wh after the restart, %.0f Wh before", after, before)
	}
	limitAfter, _, _ := h.state.importHourLimitsW()
	if limitAfter >= 7500 || math.Abs(limitAfter-limitBefore) > 100 {
		t.Fatalf("limit %.0f W after the restart, %.0f W before", limitAfter, limitBefore)
	}
}

func newImportHourState(now *time.Time) (*State, *telemetry.Store) {
	st := NewState(0, 0, "meter")
	st.SiteFuseAmps, st.SiteFuseVoltage, st.SiteFusePhases, st.SiteFuseSafetyA = 25, 230, 3, 0.5
	st.PeakImportCeilingW = 7500
	st.importHourZone = hourTestZone
	st.clock = func() time.Time { return *now }
	return st, telemetry.NewStore()
}

func feedMeter(st *State, store *telemetry.Store, now *time.Time, at time.Time, w float64) {
	*now = at
	store.Update("meter", telemetry.DerMeter, w, nil, nil)
	store.Get("meter", telemetry.DerMeter).UpdatedAt = at
	st.ObserveSiteImport(store)
}

// A meter gap never makes room: the time unseen counts at no less than the
// rate the hour had when it began, and the reading that ends it moves no
// limit up, even when it shows the house far lower.
func TestImportCeilingMeterGapNeverRaisesLimits(t *testing.T) {
	var now time.Time
	st, store := newImportHourState(&now)
	at := clock(16, 23, 0)
	for ; at.Before(clock(16, 23, 40)); at = at.Add(2 * time.Second) {
		feedMeter(st, store, &now, at, 6420)
	}
	rate, imported := st.hourRateW(now), st.importHour.importedWh
	limit, rise, _ := st.importHourLimitsW()
	if rise <= 7500 {
		t.Fatalf("setup: rise %.0f W, want room above the ceiling", rise)
	}
	gapEnd := now.Add(45 * time.Second)
	feedMeter(st, store, &now, gapEnd, 900)
	if got := st.importHour.importedWh - imported; got < rate*45/3600 {
		t.Fatalf("the gap counted %.1f Wh, under the hour's rate %.0f W for 45 s", got, rate)
	}
	if st.hourRateW(now) > rate+1e-6 {
		t.Fatalf("the gap raised the hour's rate from %.0f to %.0f W", rate, st.hourRateW(now))
	}
	if l, r, _ := st.importHourLimitsW(); l > limit+1e-6 || r > rise+1e-6 {
		t.Fatalf("limits %.0f/%.0f W after the gap, %.0f/%.0f W before", l, r, limit, rise)
	}
	// Steady readings at the same low import would have raised the rate.
	steady, _ := newImportHourState(&now)
	steadyStore := telemetry.NewStore()
	for at := clock(16, 23, 0); !at.After(gapEnd); at = at.Add(2 * time.Second) {
		w := 6420.0
		if at.After(clock(16, 23, 40)) {
			w = 900
		}
		feedMeter(steady, steadyStore, &now, at, w)
	}
	if steady.hourRateW(now) <= rate {
		t.Fatalf("setup: steady readings left the rate at %.0f W", steady.hourRateW(now))
	}
}

// A gap across the hour's end: the new hour starts at the ceiling and counts
// its unseen start at no less than the ceiling.
func TestImportCeilingGapAcrossHourCountsNewHourAtCeiling(t *testing.T) {
	var now time.Time
	st, store := newImportHourState(&now)
	for at := clock(16, 23, 30); at.Before(clock(16, 23, 59)); at = at.Add(2 * time.Second) {
		feedMeter(st, store, &now, at, 900)
	}
	feedMeter(st, store, &now, clock(16, 23, 59).Add(50*time.Second), 900)
	feedMeter(st, store, &now, clock(17, 0, 0).Add(30*time.Second), 900)
	if !st.importHour.start.Equal(clock(17, 0, 0)) {
		t.Fatalf("hour %v, want midnight", st.importHour.start)
	}
	if got := st.importHour.importedWh; got < 7500*30/3600.0 {
		t.Fatalf("the new hour's unseen 30 s counted %.1f Wh, under the ceiling", got)
	}
	if l, r, _ := st.importHourLimitsW(); l > 7500+1e-6 || r > 7500+1e-6 {
		t.Fatalf("new hour's limits %.0f/%.0f W, above the ceiling", l, r)
	}
}

// Readings count toward the local clock hour they fall in. Before any
// reading of a new hour, its limits are the ceiling.
func TestImportCeilingCountsPerLocalClockHour(t *testing.T) {
	var now time.Time
	st, store := newImportHourState(&now)
	for at := clock(16, 23, 59); !at.After(clock(17, 0, 0).Add(time.Second)); at = at.Add(time.Second) {
		feedMeter(st, store, &now, at, 3600)
	}
	if got := st.importHour.importedWh; math.Abs(got-1) > 1e-9 {
		t.Fatalf("the new hour's first second counted %.3f Wh, want 1 Wh", got)
	}
	st.importHour.limitW, st.importHour.riseW = 20000, 20000
	now = clock(17, 1, 2) // no reading in this hour yet
	if l, r, _ := st.importHourLimitsW(); l != 7500 || r != 7500 {
		t.Fatalf("an unread hour's limits %.0f/%.0f W, want the ceiling", l, r)
	}
	if got := importHourStart(time.Date(2026, 10, 17, 10, 47, 3, 0, time.FixedZone("IST", 19800)), time.UTC); !got.Equal(time.Date(2026, 10, 17, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("UTC hour %v", got)
	}
	ist := time.FixedZone("IST", 19800)
	if got := importHourStart(time.Date(2026, 10, 17, 10, 47, 3, 0, ist), ist); !got.Equal(time.Date(2026, 10, 17, 10, 0, 0, 0, ist)) {
		t.Fatalf("local hour %v, want 10:00 IST", got)
	}
}

// A car that could run only on the hour's slack does not start: with 5 kW
// of house load under a 7.5 kW ceiling, its 4.14 kW minimum fits only late
// in each hour and would stop when the next one starts.
func TestImportCeilingNoStartOnSlackAlone(t *testing.T) {
	h := newHourSite(t, 7500, 5000, clock(16, 21, 0))
	h.run(3 * time.Hour)
	h.logChanges()
	for _, o := range h.offers {
		if o.w > 0 {
			t.Fatalf("the car started at %s on slack alone", o.at.In(hourTestZone).Format("15:04:05"))
		}
	}
}

// A reading after a clock jump of years counts only the hour it falls in.
func TestImportCeilingClockJumpCountsOnlyItsHour(t *testing.T) {
	var now time.Time
	st, store := newImportHourState(&now)
	feedMeter(st, store, &now, time.Date(1970, 1, 1, 0, 0, 2, 0, hourTestZone), 900)
	feedMeter(st, store, &now, clock(16, 23, 30), 900)
	if !st.importHour.start.Equal(clock(16, 23, 0)) {
		t.Fatalf("hour %v, want 23:00", st.importHour.start)
	}
	if got := st.importHour.importedWh; math.Abs(got-7500*0.5) > 1e-6 {
		t.Fatalf("the unseen half-hour counted %.1f Wh, want 3,750 Wh at the ceiling", got)
	}
}

// The status shows the budget left and the limit, and nothing while off.
func TestImportHourStatus(t *testing.T) {
	var now time.Time
	st, store := newImportHourState(&now)
	for at := clock(16, 23, 0); !at.After(clock(16, 23, 30)); at = at.Add(2 * time.Second) {
		feedMeter(st, store, &now, at, 3000)
	}
	left, limit, ok := st.ImportHourStatus(17250)
	if !ok || math.Abs(left-6000) > 2 || math.Abs(limit-12000) > 5 {
		t.Fatalf("status %v: left %.1f Wh limit %.1f W, want 6,000 Wh and 12,000 W", ok, left, limit)
	}
	st.PeakImportCeilingW = 0
	if _, _, ok := st.ImportHourStatus(17250); ok {
		t.Fatal("status reported with the ceiling off")
	}
}
