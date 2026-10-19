package state

import (
	"testing"
	"time"
)

// Two hours of a 1000 W house, sampled every 5 s, with one disturbance one
// hour in. Observed on v0.154.0-beta.1 (19 October 2026): each short
// disturbance made the whole day's Home use unknown.
func TestSourceLoadDaySurvivesShortGap(t *testing.T) {
	const tick, hour = int64(5000), int64(3600000)
	for _, c := range []struct {
		name string
		// stopMS is a process stop starting one hour in.
		stopMS int64
		// receipt alters the snapshot off ms after the first hour.
		receipt func(off int64, v *HistorySources)
		known   bool
		// importWh is checked when set; the recorded grid must survive.
		loadWh, importWh float64
	}{
		{name: "no disturbance", known: true, loadWh: 2000, importWh: 2000},
		{name: "20 s restart", stopMS: 20000, known: true, loadWh: 2000, importWh: 2000},
		{name: "20 s restart, EV and its device back after 30 s", stopMS: 20000, receipt: func(off int64, v *HistorySources) {
			if off <= 50000 {
				v.Missing, v.Topology = MissingEV, "grid"
			}
		}, known: true, loadWh: 2000, importWh: 2000},
		{name: "EV unknown 15 s", receipt: unknownFor(MissingEV, 15000), known: true, loadWh: 2000, importWh: 2000},
		// The unknown charger holds its last value; the meter keeps its own
		// receipts. The household carries the 2 kW for 15 s, nothing else.
		{name: "EV starts while unknown", receipt: func(off int64, v *HistorySources) {
			if off > 0 {
				v.GridW, v.EVW = 3000, 2000
			}
			if off > 0 && off <= 15000 {
				v.Missing, v.EVW = MissingEV, 0
			}
		}, known: true, loadWh: 2000 + 2000*15.0/3600, importWh: 1000 + 1000*5.0/3600 + 3000*3595.0/3600},
		{name: "PV unknown 60 s", receipt: unknownFor(MissingPV, 60000), known: true, loadWh: 2000, importWh: 2000},
		{name: "PV unknown 145 s", receipt: unknownFor(MissingPV, 145000), importWh: 2000},
		{name: "EV unknown 5 min", receipt: unknownFor(MissingEV, 300000), importWh: 2000},
		{name: "3 min restart", stopMS: 180000},
		{name: "meter replaced", receipt: func(off int64, v *HistorySources) {
			if off > 0 {
				v.Topology = "new meter+charger"
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := sourceStore(t)
			base := time.Now().UTC().Truncate(24*time.Hour).UnixMilli() - 24*hour
			for at := base; at <= base+2*hour; at += tick {
				off := at - base - hour
				if c.stopMS > 0 && off > 0 && off < c.stopMS {
					continue
				}
				v := HistorySources{GridW: 1000}
				if c.receipt != nil && off >= 0 {
					c.receipt(off, &v)
				}
				sourceTick(t, s, at, v)
				if c.stopMS > 0 && off == 0 {
					s = reopenSourceStore(t, s)
				}
			}
			d, err := s.DailyEnergy(base, base+2*hour)
			if err != nil {
				t.Fatal(err)
			}
			if c.importWh != 0 {
				near(t, "import Wh", d.ImportWh, c.importWh)
			}
			if got := d.Missing&MissingLoad == 0; got != c.known {
				t.Fatalf("Home use known=%v, want %v (quality %s)", got, c.known, d.Missing.Quality())
			}
			if !c.known {
				return
			}
			if q := d.Missing.Quality(); q != "estimated" {
				t.Fatalf("quality %s, want estimated", q)
			}
			if d.Missing&MissingPV != 0 {
				t.Fatal("PV total became unknown")
			}
			if diff := d.LoadWh - c.loadWh; diff > 1e-6 || diff < -1e-6 {
				t.Fatalf("Home use %v Wh, want %v", d.LoadWh, c.loadWh)
			}
		})
	}
}

// A bridge that closes after midnight must still land in the closed day, so
// the day waits for it before it becomes final.
func TestSourceBridgeHoldsDayOpenUntilItCloses(t *testing.T) {
	s := sourceStore(t)
	midnight := time.Now().UTC().Truncate(24 * time.Hour).UnixMilli()
	for at := midnight - 60000; at <= midnight+10000; at += 5000 {
		v := HistorySources{GridW: 1000}
		if at > midnight-10000 && at < midnight+10000 {
			v.Missing = MissingEV
		}
		sourceTick(t, s, at, v)
		through, _, err := s.HistorySourceProgress(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if at < midnight+10000 && through > midnight-10000 {
			t.Fatal("day closed before its bridge")
		}
	}
	through, _, err := s.HistorySourceProgress(t.Context())
	if err != nil || through != midnight+10000 {
		t.Fatal("bridge not committed", through, err)
	}
	d, err := s.DailyEnergy(midnight-60000, midnight)
	if err != nil || d.Missing&MissingLoad != 0 {
		t.Fatal("closed day lost its Home use", err)
	}
	near(t, "closed day Wh", d.LoadWh, 1000.0/60)
}

func unknownFor(flag HistoryMissing, ms int64) func(int64, *HistorySources) {
	return func(off int64, v *HistorySources) {
		if off > 0 && off <= ms {
			v.Missing = flag
		}
	}
}

func reopenSourceStore(t *testing.T, s *Store) *Store {
	t.Helper()
	path := s.mainDBPath
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.EnableHistoryAggregation(); err != nil {
		t.Fatal(err)
	}
	return s
}
