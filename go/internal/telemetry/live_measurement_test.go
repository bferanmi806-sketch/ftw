package telemetry

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLiveMeasurementUsesOneDisplayPolicyWithoutChangingHistory(t *testing.T) {
	s := NewStore()
	for _, driver := range []string{"meter", "battery"} {
		s.EnsureDriverHealth(driver)
	}
	at := time.Now().Add(-3 * time.Second)
	data, err := json.Marshal(map[string]any{"control_power_w": -2000, "control_power_available": true, "control_power_observed_at": at.Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal("invalid power evidence")
	}
	s.Update("meter", DerMeter, 1000, nil, nil)
	s.Update("battery", DerBattery, -2500, nil, data)
	s.DriverHealthMut("battery").WatchdogTimeoutOverride = 10 * time.Second
	opts := ForecastOptions{MaxAge: time.Second}
	live := s.LiveMeasurementNow("meter", opts)
	if !live.Valid || live.BatteryW != -2000 || live.HouseholdW != 3000 {
		t.Fatal("live policy did not use measured AC power and the driver cadence")
	}
	if opts.UseControlPower || opts.UseWatchdogTimeout {
		t.Fatal("live query mutated the caller's history policy")
	}
	raw := s.ForecastMeasurementNow("meter", ForecastOptions{})
	if !raw.Valid || raw.BatteryW != -2500 || raw.HouseholdW != 3500 {
		t.Fatal("live query changed raw forecast/history power")
	}
	s.DriverHealthMut("battery").WatchdogTimeoutOverride = 0
	if s.LiveMeasurementNow("meter", opts).Valid {
		t.Fatal("stale source clock ignored without its cadence override")
	}
}

func TestLiveMeasurementAllowsIndependentCurrentSourceTimes(t *testing.T) {
	for _, skew := range []time.Duration{time.Second, 30 * time.Second} {
		t.Run(skew.String(), func(t *testing.T) {
			s := NewStore()
			for _, driver := range []string{"meter", "solar", "other-solar"} {
				s.EnsureDriverHealth(driver)
			}
			s.Update("meter", DerMeter, 1000, nil, nil)
			s.Update("solar", DerPV, -500, nil, nil)
			data, err := json.Marshal(map[string]any{
				"control_power_observed_at": time.Now().Add(-45 * time.Second).Format(time.RFC3339Nano),
			})
			if err != nil {
				t.Fatal(err)
			}
			s.Update("other-solar", DerPV, -250, nil, data)
			opts := ForecastOptions{MaxAge: 90 * time.Second, MaxSkew: skew, UseControlPower: true}
			live := s.LiveMeasurementNow("meter", opts)
			if !live.Valid || !live.PVValid || live.HouseholdW != 1750 {
				t.Fatalf("current independent sources hid live load: valid=%t, pv_valid=%t, reason=%q", live.Valid, live.PVValid, live.Reason)
			}
			forecast := s.ForecastMeasurementNow("meter", opts)
			if forecast.Valid || forecast.PVValid || forecast.Reason != "time_skew" || forecast.PVReason != "pv_time_skew" {
				t.Fatal("live query relaxed the forecast's source alignment")
			}
			// Relaxing alignment does not turn an expired source into live data.
			opts.MaxAge = 40 * time.Second
			if s.LiveMeasurementNow("meter", opts).Valid {
				t.Fatal("an expired source was accepted as live")
			}
		})
	}
}

func nightSite(t *testing.T, lastPV float64) *Store {
	t.Helper()
	s := NewStore()
	for _, driver := range []string{"pixii", "growatt"} {
		s.EnsureDriverHealth(driver)
	}
	s.Update("pixii", DerMeter, 780, nil, nil)
	s.Update("pixii", DerBattery, 300, nil, nil)
	s.Update("growatt", DerPV, lastPV, nil, nil)
	s.DriverHealthMut("growatt").Status = StatusOffline
	return s
}

func TestLiveMeasurementOfflineDarkPVCountsAsZero(t *testing.T) {
	s := nightSite(t, -3)
	live := s.LiveMeasurementNow("pixii", ForecastOptions{})
	if !live.Valid || live.Known[DerPV] || !live.PVAssumedZero || live.PVW != 0 || live.HouseholdW != 480 {
		t.Fatalf("offline dark PV hid load: valid=%t reason=%q pv=%v load=%v", live.Valid, live.Reason, live.PVW, live.HouseholdW)
	}
	src := live.Sources["growatt:pv"]
	if !src.AssumedZero || src.PowerW == nil || *src.PowerW != 0 {
		t.Fatal("assumed-zero PV source not marked")
	}
}

func TestLiveMeasurementOfflineDarkPVWithDischargingBattery(t *testing.T) {
	s := nightSite(t, 0)
	s.Update("pixii", DerBattery, -500, nil, nil)
	live := s.LiveMeasurementNow("pixii", ForecastOptions{})
	if !live.Valid || live.HouseholdW != 1280 {
		t.Fatalf("want load 1280, got valid=%t reason=%q load=%v", live.Valid, live.Reason, live.HouseholdW)
	}
}

func TestLiveMeasurementOfflineProducingPVStaysUnknown(t *testing.T) {
	s := nightSite(t, -2500)
	live := s.LiveMeasurementNow("pixii", ForecastOptions{})
	if live.Valid || live.Known[DerPV] || live.Reason != "offline:growatt:pv" {
		t.Fatalf("daytime PV outage was guessed: valid=%t reason=%q", live.Valid, live.Reason)
	}
}

func TestForecastOfflineDarkPVStaysUnknown(t *testing.T) {
	s := nightSite(t, 0)
	if r := s.ForecastMeasurementNow("pixii", ForecastOptions{}); r.Valid || r.Known[DerPV] {
		t.Fatal("forecast/history path accepted an assumed PV value")
	}
}

func TestLiveMeasurementOfflineMeterStillFails(t *testing.T) {
	s := nightSite(t, 0)
	s.DriverHealthMut("pixii").Status = StatusOffline
	if live := s.LiveMeasurementNow("pixii", ForecastOptions{}); live.Valid || live.Known[DerMeter] {
		t.Fatal("offline site meter produced a load")
	}
}
