package main

import (
	"time"

	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/internal/sunpos"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

// sunDownZenithDeg is standard sunset: the sun's upper limb below the
// horizon, refraction included. No panel then gets direct sunlight.
const sunDownZenithDeg = 90.833

// historySun places the site for history. The zero value has no position,
// and then nothing is inferred.
type historySun struct {
	Lat, Lon float64
	Placed   bool
}

func historySunOf(lat, lon float64, ok bool) historySun {
	return historySun{Lat: lat, Lon: lon, Placed: ok}
}

func (s historySun) down(atMS int64) bool {
	return s.Placed && sunpos.At(time.UnixMilli(atMS), s.Lat, s.Lon).ZenithDeg > sunDownZenithDeg
}

// darkPV records an unknown PV source as 0 W while the sun is down at the
// site. That is physics, not interpolation, and the snapshot carries
// EstimatedLoad so it never reads as measured. Grid, battery and EV keep
// their own evidence; Load stays unknown for any cause but PV.
func darkPV(p *state.HistorySources, opts telemetry.ForecastOptions, sun historySun) bool {
	if p == nil || p.Missing&state.MissingPV == 0 || !sun.down(p.AtMS) {
		return false
	}
	p.PVW = 0
	p.Missing = p.Missing&^state.MissingPV | state.EstimatedLoad
	// historySources sets MissingLoad for the first failing source or for
	// site configuration. With every other source known and the
	// configuration valid, PV was that source.
	if opts.HouseholdInvalidReason == "" && p.Missing&(state.MissingGrid|state.MissingBattery|state.MissingEV|state.MissingV2X) == 0 {
		p.Missing &^= state.MissingLoad
	}
	return true
}

// sleepingPVDevices names PV devices the watchdog took offline. After sunset
// a silent inverter is asleep, not replaced, so it stays in the topology and
// its night is not cut out of the record.
func sleepingPVDevices(st *state.Store, tel *telemetry.Store, identity energyIdentityLookup) []string {
	if st == nil || tel == nil || identity == nil {
		return nil
	}
	known := st.CachedDevices()
	var out []string
	for _, r := range tel.ReadingsByType(telemetry.DerPV) {
		if h := tel.DriverHealth(r.Driver); h != nil && h.Status != telemetry.StatusOffline {
			continue
		}
		if id := confirmedEnergyDeviceID(identity(r.Driver), known); id != "" {
			out = append(out, string(state.AssetPV)+":"+id)
		}
	}
	return out
}
