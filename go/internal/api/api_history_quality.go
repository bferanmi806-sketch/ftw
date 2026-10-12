package api

import (
	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/internal/telemetry"
	"strings"
)

func (s *Server) qualifyStatusHistory(resp map[string]any, siteMeter string) {
	if s.deps.HistoryMeasurementSources == nil {
		return
	}
	opts := s.deps.HistoryMeasurementSources()
	opts.UseWatchdogTimeout = true
	r := s.deps.Tel.ForecastMeasurementNow(siteMeter, opts)
	var missing state.HistoryMissing
	for kind, flag := range map[telemetry.DerType]state.HistoryMissing{telemetry.DerMeter: state.MissingGrid, telemetry.DerPV: state.MissingPV, telemetry.DerBattery: state.MissingBattery, telemetry.DerEV: state.MissingEV, telemetry.DerV2X: state.MissingV2X} {
		if !r.Known[kind] {
			missing |= flag
		}
	}
	if !r.Valid {
		missing |= state.MissingLoad
	}
	if opts.PVInvalidReason != "" {
		missing |= state.MissingPV
	}
	if r.Reason == "invalid_balance" || strings.HasPrefix(r.Reason, "duplicate_") || strings.HasPrefix(r.Reason, "invalid_sign:") || r.Reason == "time_skew" {
		missing |= state.BalanceMismatch
	}
	resp["grid_w"] = missing.Value(state.MissingGrid, r.GridW)
	resp["pv_w"] = missing.Value(state.MissingPV, r.PVW)
	resp["bat_w"] = missing.Value(state.MissingBattery, r.BatteryW)
	resp["ev_w"] = missing.Value(state.MissingEV, r.EVW)
	if missing&state.MissingEV != 0 {
		resp["ev_charging_w"] = nil
	}
	resp["v2x_w"] = missing.Value(state.MissingV2X, r.V2XW)
	resp["load_w"] = missing.Value(state.MissingLoad, r.HouseholdW)
	resp["history_quality"] = missing.Quality()
	resp["history_reason"] = r.Reason
	drivers, _ := resp["drivers"].(map[string]any)
	for _, source := range r.Sources {
		if d, ok := drivers[source.Flow.Driver].(map[string]any); ok {
			field := map[telemetry.DerType]string{telemetry.DerMeter: "meter_w", telemetry.DerPV: "pv_w", telemetry.DerBattery: "bat_w", telemetry.DerEV: "ev_w", telemetry.DerV2X: "v2x_w"}[source.Flow.DerType]
			if field != "" {
				d[field] = source.PowerW
			}
			if source.Flow.DerType == telemetry.DerBattery {
				d["soc"] = source.SoC
			}
		}
	}
	var count int
	var sum float64
	for _, source := range r.Sources {
		if source.Flow.DerType == telemetry.DerBattery {
			if source.SoC == nil {
				missing |= state.MissingSoC
			}
			count++
			if source.SoC != nil {
				sum += *source.SoC
			}
		}
	}
	if count == 0 {
		missing |= state.MissingSoC
	}
	if count > 0 {
		sum /= float64(count)
	}
	resp["bat_soc"] = missing.Value(state.MissingSoC, sum)
}
