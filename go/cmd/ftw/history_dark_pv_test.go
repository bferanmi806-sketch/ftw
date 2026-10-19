package main

import (
	"context"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/config"
	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

// Göran's box, 19 October 2026: SMA was silent for 145 s at 02:22 local time,
// 0 W on both sides, and the day's Home use became unknown.
func TestHistorySunIsDownAtNightOnly(t *testing.T) {
	stockholm := time.FixedZone("CEST", 2*3600)
	sun := historySun{Lat: 59.6, Lon: 16.5, Placed: true}
	for _, c := range []struct {
		at   time.Time
		sun  historySun
		down bool
	}{
		{time.Date(2026, 10, 19, 2, 22, 0, 0, stockholm), sun, true},
		{time.Date(2026, 10, 19, 12, 0, 0, 0, stockholm), sun, false},
		// Sunset in Västerås on 19 October is at 17:38.
		{time.Date(2026, 10, 19, 17, 30, 0, 0, stockholm), sun, false},
		{time.Date(2026, 10, 19, 17, 45, 0, 0, stockholm), sun, true},
		{time.Date(2026, 10, 19, 2, 22, 0, 0, stockholm), historySun{}, false},
	} {
		if got := c.sun.down(c.at.UnixMilli()); got != c.down {
			t.Errorf("%v placed=%v: down=%v, want %v", c.at, c.sun.Placed, got, c.down)
		}
	}
	for _, c := range []struct {
		lat, lon float64
		ok       bool
	}{{0, 0, false}, {unsetPinLat, unsetPinLon, false}, {59.6, 16.5, true}} {
		if got := historySunOf(siteLocation(&config.Config{Weather: &config.Weather{Latitude: c.lat, Longitude: c.lon}})).Placed; got != c.ok {
			t.Errorf("pin %v,%v placed=%v, want %v", c.lat, c.lon, got, c.ok)
		}
	}
}

// Two hours of a 1000 W house sampled every 5 s; PV is unknown for 5 minutes
// one hour in. On the equator the window is local midnight or local noon in
// every season.
func TestUnknownPVAfterSunsetKeepsHomeUse(t *testing.T) {
	const tick, hour = int64(5000), int64(3600000)
	midnight := historySun{Lat: 1, Lon: -15, Placed: true}
	noon := historySun{Lat: 1, Lon: 165, Placed: true}
	for _, c := range []struct {
		name  string
		sun   historySun
		pvW   float64
		opts  telemetry.ForecastOptions
		other state.HistoryMissing
		known bool
	}{
		{name: "night", sun: midnight, known: true},
		{name: "day", sun: noon, pvW: -2000},
		{name: "night without a position"},
		{name: "night with an unmeasured PV array", sun: midnight, opts: telemetry.ForecastOptions{HouseholdInvalidReason: "configured_pv_without_measurement_source"}},
		{name: "night with the battery also unknown", sun: midnight, other: state.MissingBattery},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.EnableHistoryAggregation(); err != nil {
				t.Fatal(err)
			}
			base := time.Now().UTC().Truncate(24*time.Hour).UnixMilli() - 24*hour
			for at := base; at <= base+2*hour; at += tick {
				off := at - base - hour
				r := telemetry.ForecastReading{
					At:    time.UnixMilli(at),
					Known: map[telemetry.DerType]bool{telemetry.DerMeter: true, telemetry.DerPV: true, telemetry.DerBattery: true, telemetry.DerEV: true, telemetry.DerV2X: true},
					Sources: map[string]telemetry.ForecastSource{
						"grid:meter": {Flow: telemetry.ForecastFlow{Driver: "grid", DerType: telemetry.DerMeter}},
						"sma:pv":     {Flow: telemetry.ForecastFlow{Driver: "sma", DerType: telemetry.DerPV}},
					},
					GridW: 1000 + c.pvW, PVW: c.pvW,
				}
				if off > 0 && off <= 300000 {
					r.Known[telemetry.DerPV], r.Reason = false, "offline:sma:pv"
					if c.other == state.MissingBattery {
						r.Known[telemetry.DerBattery] = false
					}
				}
				v := historySources(r)
				darkPV(v, c.opts, c.sun)
				if err := st.RecordTickWithEnergy(state.HistoryPoint{TsMs: at, Sources: v}, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.FlushHistory(context.Background()); err != nil {
				t.Fatal(err)
			}
			d, err := st.DailyEnergy(base, base+2*hour)
			if err != nil {
				t.Fatal(err)
			}
			if got := d.Missing&state.MissingLoad == 0; got != c.known {
				t.Fatalf("Home use known=%v, want %v (quality %s)", got, c.known, d.Missing.Quality())
			}
			if c.pvW == 0 && math.Abs(d.ImportWh-2000) > 1e-6 || c.pvW != 0 && math.Abs(d.ExportWh-2000) > 1e-6 {
				t.Fatalf("grid import %v, export %v Wh: the meter's own record changed", d.ImportWh, d.ExportWh)
			}
			if !c.known {
				return
			}
			if q := d.Missing.Quality(); q != "estimated" {
				t.Fatalf("quality %s, want estimated", q)
			}
			if d.Missing&state.MissingPV != 0 || d.PVWh != 0 {
				t.Fatalf("PV %v Wh, missing %v; want a known 0 Wh after sunset", d.PVWh, d.Missing&state.MissingPV != 0)
			}
			if math.Abs(d.LoadWh-2000) > 1e-6 {
				t.Fatalf("Home use %v Wh, want 2000", d.LoadWh)
			}
		})
	}
}

// The watchdog drops an offline driver's device from the energy record. After
// sunset the sleeping inverter keeps its place in the history topology, so the
// writer sees one topology and does not cut the night out.
func TestSleepingInverterKeepsItsDeviceAfterSunset(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.RegisterDevice(state.Device{DriverName: "sma", Make: "SMA", Serial: "PV-7"}); err != nil {
		t.Fatal(err)
	}
	identity := testEnergyIdentity(st)
	tel := telemetry.NewStore()
	tel.Update("sma", telemetry.DerPV, 0, nil, nil)
	tel.RecordDriverSuccess("sma")
	pvDevices := func(observations []state.EnergyObservation) []string {
		var out []string
		for _, o := range observations {
			if o.AssetKind == state.AssetPV && o.DeviceID != "" {
				out = append(out, string(o.AssetKind)+":"+o.DeviceID)
			}
		}
		return out
	}
	awake := pvDevices(buildEnergyObservations(st, tel, tickPersistControl{}, state.HistoryPoint{}, identity))
	if len(awake) != 1 || len(sleepingPVDevices(st, tel, identity)) != 0 {
		t.Fatalf("awake inverter devices %v, sleeping %v", awake, sleepingPVDevices(st, tel, identity))
	}
	tel.DriverHealthMut("sma").SetOffline()
	if got := pvDevices(buildEnergyObservations(st, tel, tickPersistControl{}, state.HistoryPoint{}, identity)); len(got) != 0 {
		t.Fatalf("offline inverter still observed: %v", got)
	}
	if got := sleepingPVDevices(st, tel, identity); !slices.Equal(got, awake) {
		t.Fatalf("sleeping inverter devices %v, want %v", got, awake)
	}
}
