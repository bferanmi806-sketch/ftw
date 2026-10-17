package control

import (
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

// evHeadroomW is the shared grid balance for an EV pool. Power outside the
// pool stays in gridW. No unmeasured future battery relief earns headroom.
func evHeadroomW(ceiling, gridW, poolW float64) float64 {
	if !finiteFuseValue(ceiling) || !finiteFuseValue(gridW) || !finiteFuseValue(poolW) || ceiling <= 0 || poolW < 0 {
		return 0
	}
	return math.Max(0, ceiling-gridW+poolW)
}

func finiteFuseValue(w float64) bool { return !math.IsNaN(w) && !math.IsInf(w, 0) }

// evSettle is how long a charger may take to show a new current at the site
// meter; Easee Cloud needs about 5–15 s (#1674). Until then the meter may
// still contain the higher offer, so no overage is cut twice and no rise
// follows an unseen step.
const evSettle = 20 * time.Second

// evPhaseMarginA is the working margin below the breaker on each phase. One
// amp on the pool's phases is also the rise step and the restart margin.
const evPhaseMarginA = 1.0

// evMeterLimit is the managed EVs' common hard cap. The site meter measures
// the protected quantities, worst phase and total import, so it alone decides
// a cut. EV readings only gate a rise: a lone or late 0 W sample cannot stop
// a charge.
type evMeterLimit struct {
	set       bool
	capW      float64
	changedAt time.Time // last cut, rise or idle start level
	peakW     float64   // highest offer the meter may still contain
	peakAt    time.Time
}

type evPool struct {
	offerW, drawW, wantW float64
	drawKnown            bool
	minW, phases         float64
}

// EVFuseBudget returns the managed EVs' common hard cap, which the loadpoint
// controller divides between cars. Callers hold the control lock, and the
// shared freshness gate must already allow dispatch. An overage on the latest
// site reading cuts at once; inside the limits the cap holds, and it rises
// one step only when the meter shows room and the cars have taken the last
// step. An idle pool starts only with room for its minimum. Repeating a call
// on the same reading changes nothing.
func EVFuseBudget(store *telemetry.Store, state *State, points []loadpoint.State, configs []loadpoint.Config, fuseMaxW float64) (float64, bool) {
	if store == nil || state == nil || !finiteFuseValue(fuseMaxW) || fuseMaxW <= 0 {
		return 0, true
	}
	meter := store.Get(state.SiteMeterDriver, telemetry.DerMeter)
	if meter == nil || !finiteFuseValue(meter.RawW) {
		return 0, true
	}
	lim := &state.evLimit
	pool, plugged := evPoolFor(store, state, points, configs, fuseMaxW)
	if !plugged {
		*lim = evMeterLimit{}
		return 0, true
	}
	state.stepEVMeterLimit(store, meter, pool, fuseMaxW)
	return lim.capW, true
}

func (s *State) stepEVMeterLimit(store *telemetry.Store, meter *telemetry.DerReading, pool evPool, fuseMaxW float64) {
	lim, now := &s.evLimit, s.now()
	if !lim.set || pool.offerW >= lim.peakW || now.Sub(lim.peakAt) >= evSettle {
		lim.peakW, lim.peakAt = pool.offerW, now
	}
	prev := math.Inf(1)
	if lim.set {
		prev = lim.capW
	}
	lim.set = true
	roomW, measured, worstA := s.evMeterRoomW(meter, pool.phases, fuseMaxW)
	if roomW < 0 {
		// The meter may still contain up to peakW of EV power. Taking the
		// overage from that peak cuts each measured watt once.
		lim.capW = math.Min(prev, math.Max(0, lim.peakW+roomW))
		if lim.capW < prev {
			lim.changedAt = now
			slog.Info("EV hard limit: site meter over limit", "grid_w", meter.RawW, "worst_phase_a", worstA,
				"overage_w", -roomW, "offer_w", lim.peakW, "cap_w", lim.capW)
		}
		return
	}
	// An increase needs measured room left after outstanding battery orders.
	room := 0.0
	if measured {
		room = math.Max(0, roomW-s.pendingBatteryIncreaseW(store, meter))
	}
	step := evPhaseMarginA * s.SiteFuseVoltage * pool.phases
	if pool.offerW <= 0 {
		lim.capW, lim.changedAt = 0, now
		if room >= pool.minW+step {
			lim.capW = room
		}
		return
	}
	// Keeping the current offer is never a cut. Headroom above it stays
	// within what this reading measured.
	lim.capW = math.Min(prev, math.Max(pool.offerW, pool.drawW)+room)
	if math.IsInf(prev, 1) {
		lim.changedAt = now // The first reading sets the cap; a rise waits.
		return
	}
	if lim.capW < pool.wantW && room >= step && now.Sub(lim.changedAt) >= evSettle &&
		(!pool.drawKnown || pool.drawW >= pool.offerW-step) {
		lim.capW += step
		lim.changedAt = now
	}
}

// evMeterRoomW is the measured room below the nearer limit, total import or
// worst phase, in EV watts. Negative is an overage. measured is false when
// the meter reports phase currents that cannot be read.
func (s *State) evMeterRoomW(meter *telemetry.DerReading, phases, fuseMaxW float64) (roomW float64, measured bool, worstA float64) {
	roomW = s.effectiveImportCeilingW(fuseMaxW) - meter.RawW
	worstA, present, valid := worstPhaseA(meter, s)
	if !present {
		return roomW, true, 0
	}
	if !valid {
		return roomW, false, 0
	}
	return math.Min(roomW, (s.SiteFuseAmps-evPhaseMarginA-worstA)*s.SiteFuseVoltage*phases), true, worstA
}

// worstPhaseA reads the site's configured phases. present is false when the
// meter reports no phase currents; valid is false when they cannot be used.
func worstPhaseA(meter *telemetry.DerReading, state *State) (worstA float64, present, valid bool) {
	// DER data uses snake case; separate fields retain absent-vs-zero.
	var data struct {
		L1 *float64 `json:"l1_a"`
		L2 *float64 `json:"l2_a"`
		L3 *float64 `json:"l3_a"`
	}
	if json.Unmarshal(meter.Data, &data) != nil || (data.L1 == nil && data.L2 == nil && data.L3 == nil) {
		return 0, false, false
	}
	if !finiteFuseValue(state.SiteFuseAmps) || !finiteFuseValue(state.SiteFuseVoltage) || state.SiteFuseAmps <= 0 || state.SiteFuseVoltage <= 0 || state.SiteFusePhases < 1 || state.SiteFusePhases > 3 {
		return 0, true, false
	}
	for _, amps := range []*float64{data.L1, data.L2, data.L3}[:state.SiteFusePhases] {
		if amps == nil || !finiteFuseValue(*amps) {
			return 0, true, false
		}
		worstA = math.Max(worstA, math.Abs(*amps))
	}
	return worstA, true, true
}

func (s *State) pendingBatteryIncreaseW(store *telemetry.Store, meter *telemetry.DerReading) float64 {
	now := s.now()
	siteAt := meter.UpdatedAt
	if current, ok := qualifiedFuseObservation(meter, now); ok {
		siteAt = current.Seen()
	}
	windows := store.ControlWindows(now.Add(-telemetry.ControlWindowDuration), now)
	return pendingBatteryIncreaseAtLatestSite(pendingBatterySeriesForFuse(store, s, windows, now), siteAt)
}

// evPoolFor sums the plugged managed chargers. Before the first command the
// cars' readings, or else the fuse, bound what the meter contains.
func evPoolFor(store *telemetry.Store, state *State, points []loadpoint.State, configs []loadpoint.Config, fuseMaxW float64) (evPool, bool) {
	byID := make(map[string]loadpoint.Config, len(configs))
	for _, cfg := range configs {
		byID[cfg.ID] = cfg
	}
	pool := evPool{phases: 3, wantW: math.Inf(1), drawKnown: true}
	offerKnown := true
	seen := map[string]bool{}
	now := state.now()
	for _, point := range points {
		if !point.PluggedIn {
			continue
		}
		cfg := byID[point.ID]
		// Empty is the declared three-phase default. The controller's own
		// gate covers runtime auto/manual phase changes.
		if cfg.PhaseMode != "" && cfg.PhaseMode != "3p" {
			pool.phases = 1
		}
		if cfg.MinChargeW > 0 && (pool.minW == 0 || cfg.MinChargeW < pool.minW) {
			pool.minW = cfg.MinChargeW
		}
		offerKnown = offerKnown && point.CommandedKnown
		if seen[point.DriverName] {
			continue
		}
		seen[point.DriverName] = true
		r := store.Get(point.DriverName, telemetry.DerEV)
		h := store.DriverHealth(point.DriverName)
		if current, ok := qualifiedFuseObservation(r, now); ok && h != nil && h.TelemetryLive() {
			pool.drawW += math.Max(0, current.PowerW)
		} else {
			pool.drawKnown = false
		}
	}
	if len(seen) == 0 {
		return pool, false
	}
	pool.offerW = loadpoint.CommandedEVPowerW(points)
	if !offerKnown {
		pool.offerW = fuseMaxW
		if pool.drawKnown {
			pool.offerW = pool.drawW
		}
	}
	if state.evFuseDemandsSet {
		pool.wantW = 0
		for _, demand := range state.evFuseDemands {
			pool.wantW += demand.powerW
		}
	}
	if state.SiteFusePhases >= 1 && float64(state.SiteFusePhases) < pool.phases {
		pool.phases = float64(state.SiteFusePhases)
	}
	return pool, true
}

func qualifiedFuseObservation(r *telemetry.DerReading, now time.Time) (telemetry.ControlObservation, bool) {
	if r == nil || !finiteFuseValue(r.RawW) || r.UpdatedAt.After(now) || now.Sub(r.UpdatedAt) > telemetry.ControlPowerMaxAge(r.Data) {
		return telemetry.ControlObservation{}, false
	}
	p, ok := telemetry.ControlPowerObservation(r.RawW, r.Data, r.UpdatedAt)
	return p, ok && !p.Seen().After(now) && now.Sub(p.Seen()) <= telemetry.ControlWindowDuration
}

func publishBatterylessEVFuseBudget(store *telemetry.Store, state *State, fuseMaxW float64) {
	meter := store.Get(state.SiteMeterDriver, telemetry.DerMeter)
	state.FuseEVMaxW, state.FuseSaturated = 0, false
	demand := math.Max(state.EVChargingW, scheduledEVPowerW(state))
	if meter == nil || demand <= 0 {
		return
	}
	cap := evHeadroomW(state.phaseImportCeilingW(fuseMaxW, meter), math.Max(meter.RawW, meter.SmoothedW), state.EVChargingW)
	state.FuseEVMaxW = math.Min(demand, cap)
	state.FuseSaturated = demand > cap
}
