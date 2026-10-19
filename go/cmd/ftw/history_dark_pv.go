package main

import (
	"time"

	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/internal/sunpos"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

// Sun positions as zenith angles. After sunset (upper limb below the horizon,
// refraction included) no panel gets direct sunlight, but diffuse light
// remains until civil dusk, when the sun is 6° down.
const (
	sunsetZenithDeg    = 90.833
	civilDuskZenithDeg = 96
)

// historySun places the site for history. The zero value has no position,
// and then nothing is inferred.
type historySun struct {
	Lat, Lon float64
	Placed   bool
}

func historySunOf(lat, lon float64, ok bool) historySun {
	return historySun{Lat: lat, Lon: lon, Placed: ok}
}

func (s historySun) zenith(atMS int64) (float64, bool) {
	if !s.Placed {
		return 0, false
	}
	return sunpos.At(time.UnixMilli(atMS), s.Lat, s.Lon).ZenithDeg, true
}

// darkPV records an unknown PV source as 0 W while the sun is down at the
// site: physics, not interpolation. Past civil dusk the 0 W is as good as a
// reading and carries no marker, so a night with a sleeping inverter also
// trains the load models; no separate bit records it. In twilight the
// snapshot carries EstimatedLoad: Home use counts it, training does not.
// Grid, battery and EV keep their own evidence; Load stays unknown for any
// cause but PV.
func darkPV(p *state.HistorySources, opts telemetry.ForecastOptions, sun historySun) bool {
	if p == nil || p.Missing&state.MissingPV == 0 {
		return false
	}
	z, placed := sun.zenith(p.AtMS)
	if !placed || z <= sunsetZenithDeg {
		return false
	}
	p.PVW = 0
	p.Missing &^= state.MissingPV
	if z <= civilDuskZenithDeg {
		p.Missing |= state.EstimatedLoad
	}
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
