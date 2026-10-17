package control

import (
	"math"

	"github.com/srcfl/ftw/go/internal/telemetry"
)

// clampEVBatteryHeadroom keeps optional battery charge out of room an EV
// still needs. It reads only the site meter and our own commands: within
// evSettle the meter may show any command sent, so a device's lowest recent
// command is its baseline and the rest of its range is still pending. No
// charger or battery reading takes part.
func clampEVBatteryHeadroom(targets []DispatchTarget, store *telemetry.Store, state *State, fuseMaxW float64) []DispatchTarget {
	if store == nil || state == nil || len(targets) == 0 || fuseMaxW <= 0 {
		return targets
	}
	now := state.now()
	// This tick's new offer is not in the meter yet; the lowest logged one is.
	offeredW, _, _, ok := state.evOffers.span(now)
	if !ok {
		offeredW = 0
	}
	var demandW float64
	phases := 3.0 // No phase placement promise for 1p or auto chargers.
	for _, demand := range state.evFuseDemands {
		demandW += demand.powerW
		if demand.phaseMode != "3p" {
			phases = 1
		}
	}
	if len(state.evFuseDemands) == 0 {
		return targets
	}
	needW := math.Max(0, demandW-offeredW)
	meter := store.Get(state.SiteMeterDriver, telemetry.DerMeter)
	if meter == nil || !finiteFuseValue(meter.RawW) || !finiteFuseValue(fuseMaxW) {
		return floorPositiveEVBatteryTargets(targets)
	}
	drivers := make(map[string]bool, len(targets))
	for driver := range state.DriverLimits {
		drivers[driver] = true
	}
	for driver := range state.batteryCommands {
		drivers[driver] = true
	}
	targeted := make(map[string]bool, len(targets))
	for _, target := range targets {
		if targeted[target.Driver] {
			return floorPositiveEVBatteryTargets(targets)
		}
		targeted[target.Driver], drivers[target.Driver] = true, true
	}
	baselines := make(map[string]float64, len(drivers))
	uppers := make(map[string]float64, len(drivers))
	for driver := range drivers {
		if low, high, _, sent := state.batteryCommands[driver].span(now); sent {
			baselines[driver], uppers[driver] = low, high
		} else {
			// Without a recent command it may be discharging and stop.
			baselines[driver] = -state.DriverLimits[driver].dischargeCap()
		}
	}
	spare := state.effectiveImportCeilingW(fuseMaxW) - meter.RawW - needW
	// During measured export overflow, the existing fuse pipeline may
	// require charge to reduce export. Magnitudes alone cannot label that
	// worst phase as import; leave this direction to the existing guard.
	if meter.RawW >= -state.effectiveExportCeilingW(fuseMaxW) && state.SiteFuseVoltage > 0 {
		if phaseSpare, known := evBatteryPhaseSpareW(meter, state, needW/(state.SiteFuseVoltage*phases)); known {
			spare = math.Min(spare, phaseSpare)
		}
	}
	out := capBatteryOffersToHeadroom(targets, baselines, uppers, spare)
	// Charge needed to bring measured export inside its hard ceiling is
	// not optional. Preserve that part of the already validated offer;
	// this stage never invents or increases an original battery command.
	var offeredCharge, finalCharge float64
	for i := range targets {
		offeredCharge += math.Max(0, targets[i].TargetW)
		finalCharge += math.Max(0, out[i].TargetW)
	}
	exportFloor := math.Min(offeredCharge, math.Max(0, -meter.RawW-state.effectiveExportCeilingW(fuseMaxW)))
	if exportFloor > finalCharge && offeredCharge > finalCharge {
		scale := (exportFloor - finalCharge) / (offeredCharge - finalCharge)
		for i := range out {
			if targets[i].TargetW > out[i].TargetW && targets[i].TargetW > 0 {
				out[i].TargetW += (targets[i].TargetW - out[i].TargetW) * scale
			}
		}
	}
	return out
}

// A reduction at one battery never funds an increase at another, and a lost
// discharge also needs room.
func capBatteryOffersToHeadroom(targets []DispatchTarget, baselines, uppers map[string]float64, spare float64) []DispatchTarget {
	if !finiteFuseValue(spare) {
		return floorPositiveEVBatteryTargets(targets)
	}
	byDriver := make(map[string]DispatchTarget, len(targets))
	allocationBases := make(map[string]float64, len(targets))
	var positiveBase, dischargeLoss, pendingExtra, pendingOutsidePositive float64
	for _, target := range targets {
		byDriver[target.Driver] = target
		baseline := baselines[target.Driver]
		if !finiteFuseValue(baseline) {
			return floorPositiveEVBatteryTargets(targets)
		}
		allocationBases[target.Driver] = math.Max(0, baseline)
		if upper, known := uppers[target.Driver]; known {
			allocationBases[target.Driver] = math.Max(allocationBases[target.Driver], upper)
		}
		if target.TargetW > 0 {
			positiveBase += math.Max(0, baseline)
		}
		dischargeLoss += math.Max(0, math.Min(0, target.TargetW)-math.Min(0, baseline))
	}
	for driver, upper := range uppers {
		floor := baselines[driver]
		target, targeted := byDriver[driver]
		if targeted {
			floor = math.Max(floor, math.Min(0, target.TargetW))
		}
		pending := math.Max(0, upper-floor)
		pendingExtra += pending
		if !targeted || target.TargetW <= 0 {
			pendingOutsidePositive += pending
		}
	}
	spare -= dischargeLoss
	out := append([]DispatchTarget(nil), targets...)
	var wantedIncrease float64
	for _, target := range out {
		if target.TargetW > 0 {
			wantedIncrease += math.Max(0, target.TargetW-allocationBases[target.Driver])
		}
	}
	increaseScale := 1.0
	if wantedIncrease > 0 {
		increaseScale = math.Min(1, math.Max(0, spare-pendingExtra)/wantedIncrease)
	}
	var positiveTotal float64
	for i := range out {
		if out[i].TargetW > 0 {
			baseline := allocationBases[out[i].Driver]
			capped := math.Min(out[i].TargetW, baseline) + math.Max(0, out[i].TargetW-baseline)*increaseScale
			if capped < out[i].TargetW {
				out[i].TargetW, out[i].Clamped = capped, true
			}
			positiveTotal += capped
		}
	}
	capW := math.Max(0, positiveBase+spare-pendingOutsidePositive)
	if positiveTotal > capW && positiveTotal > 0 {
		for i := range out {
			if out[i].TargetW > 0 {
				out[i].TargetW *= capW / positiveTotal
				out[i].Clamped = true
			}
		}
	}
	// A partial positive reduction also leaves its old higher order pending.
	// Bound every offer against the same snapshot of its siblings; one
	// commanded reduction must not fund another offer in this pass.
	covered := make([]float64, len(out))
	var coveredTotal float64
	for i, target := range out {
		if target.TargetW > 0 {
			covered[i] = math.Max(target.TargetW, math.Max(0, uppers[target.Driver]))
			coveredTotal += covered[i]
		}
	}
	for i := range out {
		if out[i].TargetW > 0 {
			room := math.Max(0, capW-(coveredTotal-covered[i]))
			if out[i].TargetW > room {
				out[i].TargetW, out[i].Clamped = room, true
			}
		}
	}
	return out
}

func evBatteryPhaseSpareW(meter *telemetry.DerReading, state *State, evIncreaseA float64) (float64, bool) {
	worst, present, valid := worstPhaseA(meter, state)
	if !present {
		return 0, false
	}
	if !valid {
		return math.Inf(-1), true
	}
	margin := math.Max(1, state.SiteFuseSafetyA) // Same working margin as the EV phase guard.
	return (state.SiteFuseAmps - margin - worst - evIncreaseA) * state.SiteFuseVoltage * float64(state.SiteFusePhases), true
}

func floorPositiveEVBatteryTargets(targets []DispatchTarget) []DispatchTarget {
	out := append([]DispatchTarget(nil), targets...)
	for i := range out {
		if out[i].TargetW > 0 {
			out[i].TargetW, out[i].Clamped = 0, true
		}
	}
	return out
}
