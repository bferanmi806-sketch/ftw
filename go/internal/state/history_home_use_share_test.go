package state

import (
	"testing"
	"time"
)

// Six hours of a 1000 W house, sampled every 10 s, with one disturbance three
// hours in. Before source history a day summed what it had; since #1741 any
// unknown time made the whole day's Home use unknown. At most 1 % unknown time
// (3.6 minutes here) keeps it as an estimate, with unknown time counted as
// nothing. Other flows keep their own unknowns.
func TestHomeUseSurvivesSmallUnknownShare(t *testing.T) {
	const tick, hour = int64(10000), int64(3600000)
	for _, c := range []struct {
		name string
		// skip leaves the writer without snapshots for this long.
		skip    int64
		missing HistoryMissing
		// missingMS keeps the flag on this long; the interval after it and
		// every partly unknown minute stay unknown too.
		missingMS int64
		gridW     float64
		known     bool
		loadWh    float64
	}{
		{name: "no disturbance", known: true, loadWh: 6000},
		{name: "battery unknown 2 min", missing: MissingBattery, missingMS: 120000, known: true, loadWh: 6000 - 3*1000.0/60},
		{name: "writer gap 3 min", skip: 180000, known: true, loadWh: 6000 - 3*1000.0/60},
		{name: "writer gap 5 min", skip: 300000},
		{name: "battery unknown 10 min", missing: MissingBattery, missingMS: 600000},
		{name: "negative known total", missing: MissingBattery, missingMS: 120000, gridW: -500},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := sourceStore(t)
			base := time.Now().UTC().Truncate(24*time.Hour).UnixMilli() - 24*hour
			grid := 1000.0
			if c.gridW != 0 {
				grid = c.gridW
			}
			for at := base; at <= base+6*hour; at += tick {
				off := at - base - 3*hour
				if off > 0 && off < c.skip {
					continue
				}
				v := HistorySources{GridW: grid}
				if off > 0 && off <= c.missingMS {
					v.Missing = c.missing
				}
				sourceTick(t, s, at, v)
			}
			d, err := s.DailyEnergy(base, base+6*hour)
			if err != nil {
				t.Fatal(err)
			}
			if c.gridW == 0 {
				near(t, "import Wh", d.ImportWh, 6000-1000*float64(c.skip)/3600000)
			}
			if got := d.Missing&MissingLoad == 0; got != c.known {
				t.Fatalf("Home use known=%v, want %v (quality %s)", got, c.known, d.Missing.Quality())
			}
			if (d.Missing&MissingBattery != 0) != (c.missing == MissingBattery) {
				t.Fatal("battery totals changed state")
			}
			if !c.known {
				return
			}
			if d.Missing&EstimatedLoad == 0 {
				t.Fatal("Home use not marked as an estimate")
			}
			near(t, "Home use Wh", d.LoadWh, c.loadWh)
		})
	}
}

// Days cached under the stricter rule are recounted once after the update.
// Days before source history and later cache entries are left alone.
func TestSourceDaysAreRecountedOnce(t *testing.T) {
	s := sourceStore(t)
	const hour = int64(3600000)
	base := time.Now().UTC().Truncate(24*time.Hour).UnixMilli() - 24*hour
	for at := base; at <= base+hour; at += 10000 {
		sourceTick(t, s, at, HistorySources{GridW: 1000})
	}
	day := time.UnixMilli(base).UTC().Format("2006-01-02")
	old := time.UnixMilli(base).UTC().AddDate(0, 0, -10).Format("2006-01-02")
	stale := DayEnergy{Missing: MissingLoad, Intervals: 1}
	for _, d := range []string{day, old} {
		if err := s.SaveDailyEnergyForSourceMode(d, stale, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.history.Exec(`DELETE FROM history_migrations WHERE name=?`, homeUseShareRecount); err != nil {
		t.Fatal(err)
	}
	reopen := func() {
		t.Helper()
		path := s.mainDBPath
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		if s, err = Open(path); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if err := s.EnableHistoryAggregation(); err != nil {
			t.Fatal(err)
		}
	}
	reopen()
	if _, present, err := s.LoadDailyEnergy(day); err != nil || present {
		t.Fatal("source day still cached under the stricter rule", err)
	}
	if _, present, err := s.LoadDailyEnergy(old); err != nil || !present {
		t.Fatal("day before source history was recounted", err)
	}
	if err := s.SaveDailyEnergyForSourceMode(day, stale, true); err != nil {
		t.Fatal(err)
	}
	reopen()
	if _, present, err := s.LoadDailyEnergy(day); err != nil || !present {
		t.Fatal("recount ran twice", err)
	}
}
