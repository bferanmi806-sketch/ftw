package main

import (
	"github.com/srcfl/ftw/go/internal/telemetry"
	"testing"
	"time"
)

func TestHistoryRetainsLiveMeterWhenBatteryOffline(t *testing.T) {
	tel := telemetry.NewStore()
	for _, name := range []string{"meter", "battery"} {
		tel.EnsureDriverHealth(name)
	}
	tel.Update("meter", telemetry.DerMeter, 1200, nil, nil)
	tel.RecordDriverSuccess("meter")
	tel.DriverHealthMut("battery").SetOffline()
	opts := telemetry.ForecastOptions{ExpectedFlows: []telemetry.ForecastFlow{{Driver: "battery", DerType: telemetry.DerBattery}}}
	p, available := buildHistoryPoint(tel, tickPersistControl{SiteMeterDriver: "meter"}, time.Now().UnixMilli(), time.Minute, opts)
	if !available || p.GridW != 1200 {
		t.Fatalf("live meter lost when battery offline: available=%v point=%+v", available, p)
	}
}
