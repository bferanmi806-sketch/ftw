package loadpoint

import (
	"math"
	"testing"
	"time"
)

// Cloud delivery order must not replace the car's measurement times. Both
// readings arrive late; the second was measured after the first.
func TestDelayedVehicleSoCUsesMeasurementOrder(t *testing.T) {
	m := NewManager()
	m.Load([]Config{{ID: "car", VehicleCapacityWh: 86500, PluginSoC: .60}})
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	now := start
	m.SetNowFn(func() time.Time { return now })
	m.Observe("car", true, 0, 0, true)
	if !m.SetCurrentSoC("car", .60) {
		t.Fatal("manual starting level refused")
	}
	for i := 1; i <= 8; i++ {
		now = start.Add(time.Duration(i) * 5 * time.Minute)
		m.Observe("car", true, 6000, float64(i)*500, true)
	}
	first := start.Add(10 * time.Minute)
	if !m.VehicleSoCSupersedes("car", first) || !m.AnchorVehicleSoCAt("car", .62, first) {
		t.Fatal("first delayed car reading refused")
	}
	now = start.Add(45 * time.Minute)
	m.Observe("car", true, 6000, 4500, true)
	second := start.Add(20 * time.Minute)
	if !m.VehicleSoCSupersedes("car", second) {
		t.Fatal("newer car measurement lost to the first reading's receipt time")
	}
	want := .68 + 2500*DefaultChargeEfficiency/86500
	if got, fromVehicle := m.PlanningStartSoC("car", .68, second, true); !fromVehicle || math.Abs(got-want) > 1e-9 {
		t.Fatalf("planner start %.12f fromVehicle=%t, want %.12f", got, fromVehicle, want)
	}
	if !m.AnchorVehicleSoCAt("car", .68, second) {
		t.Fatal("second delayed car reading refused")
	}
	st, _ := m.State("car")
	if math.Abs(st.CurrentSoC-want) > 1e-9 || st.UpdatedAtMs != now.UnixMilli() {
		t.Fatalf("wrong corrected level or receipt time: %+v", st)
	}
	for _, observed := range []time.Time{time.Time{}, first, second} {
		if m.VehicleSoCSupersedes("car", observed) {
			t.Fatalf("old or repeated measurement became newer: %v", observed)
		}
		if got, fromVehicle := m.PlanningStartSoC("car", .68, observed, true); fromVehicle || math.Abs(got-want) > 1e-9 {
			t.Fatalf("old reading changed the estimate or counted energy twice: %.12f, %t", got, fromVehicle)
		}
	}
	// A later explicit user correction still takes precedence over a car
	// measurement taken before it, even when that reading arrives afterwards.
	now = start.Add(46 * time.Minute)
	if !m.SetCurrentSoC("car", .71) {
		t.Fatal("manual correction refused")
	}
	olderThanManual := start.Add(44 * time.Minute)
	if m.VehicleSoCSupersedes("car", olderThanManual) {
		t.Fatal("a delayed car reading replaced a newer manual correction")
	}
	if got, fromVehicle := m.PlanningStartSoC("car", .68, olderThanManual, true); fromVehicle || math.Abs(got-.71) > 1e-9 {
		t.Fatalf("manual correction lost: %.12f, %t", got, fromVehicle)
	}
	now = now.Add(2 * time.Minute)
	if !m.VehicleSoCSupersedes("car", now.Add(-time.Minute)) {
		t.Fatal("a car reading measured after the manual correction cannot replace it")
	}
}
