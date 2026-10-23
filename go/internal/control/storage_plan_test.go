package control

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/mpc"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

func TestPhysicalStorageAllocationSurvivesDispatch(t *testing.T) {
	now := time.Now()
	d := mpc.SlotDirective{DecisionID: "plan", SlotStart: now, SlotEnd: now.Add(15 * time.Minute), BatteryEnergyWh: 500,
		StorageEnergyWh: map[string]float64{"a": 400, "b": 100}, Strategy: mpc.ModeArbitrage, GridW: 2500}
	dir := SlotDirectiveFromMPC(d)
	d.StorageEnergyWh["a"] = 0
	store := seedStore(500, []struct {
		name          string
		currentW, soc float64
	}{{"a", 0, .5}, {"b", 0, .5}})
	s := newStateWithEnergyDispatch(dir, "ferroamp")
	s.clock = func() time.Time { return now }
	// b sits on a smaller inverter, so the plan's split is physical.
	s.DriverLimits = map[string]PowerLimits{"b": {MaxChargeW: 1500, MaxDischargeW: 1500, MaxChargeWSet: true, MaxDischargeWSet: true}}
	out := ComputeDispatch(store, s, caps(map[string]float64{"a": 10000, "b": 10000}), 11040)
	want := map[string]float64{"a": 1600, "b": 400}
	if len(out) != 2 {
		t.Fatalf("targets=%+v", out)
	}
	for _, v := range out {
		if math.Abs(v.TargetW-want[v.Driver]) > 1e-6 {
			t.Fatalf("targets=%+v", out)
		}
	}
}

func TestHeterogeneousStorageSharesReachFinalDispatch(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		now := time.Now()
		dir := SlotDirective{SlotStart: now, SlotEnd: now.Add(15 * time.Minute), BatteryEnergyWh: 500,
			StorageEnergyWh: map[string]float64{"a": 25, "b": 475}, PlannedGridW: 2500, HasPlannedGridW: true}
		store := seedStore(500, []struct {
			name          string
			currentW, soc float64
		}{{"a", 0, .5}, {"b", 0, .5}})
		if blocked {
			soc := .5
			store.Update("a", telemetry.DerBattery, 0, &soc, json.RawMessage(`{"charge_capable":false}`))
		}
		s := newStateWithEnergyDispatch(dir, "ferroamp")
		s.clock = func() time.Time { return now }
		out := ComputeDispatch(store, s, caps(map[string]float64{"a": 1000, "b": 19000}), 11040)
		if len(out) != 2 {
			t.Fatalf("targets=%+v", out)
		}
		for _, v := range out {
			want := 1900.
			if v.Driver == "a" {
				want = 100
				if blocked {
					want = 0
				}
			}
			if math.Abs(v.TargetW-want) > 1e-6 {
				t.Fatalf("blocked=%v targets=%+v", blocked, out)
			}
		}
	}
}

func TestPhysicalStorageDeliveryAndSiteClamp(t *testing.T) {
	start := time.Now()
	now := start
	dir := SlotDirective{DecisionID: "first", SlotStart: start, SlotEnd: start.Add(time.Hour), BatteryEnergyWh: 2000, StorageEnergyWh: map[string]float64{"a": 1500, "b": 500}}
	s := newStateWithEnergyDispatch(dir, "site")
	s.SlotDirective = func(time.Time) (SlotDirective, bool) { return dir, true }
	s.clock = func() time.Time { return now }
	bats := []batteryInfo{{driver: "a", capacityWh: 10000, soc: .5},
		{driver: "b", capacityWh: 10000, soc: .5, maxChargeW: 1000, maxDischargeW: 1000, maxChargeWSet: true, maxDischargeWSet: true}}
	out, ok := distributePlannedStorages(s, bats, 1000, false)
	if !ok || out[0].TargetW != 750 || out[1].TargetW != 250 {
		t.Fatalf("site clamp=%+v", out)
	}
	// Only a really delivered energy; b's budget is not credited from a.
	bats[0].currentW = 1500
	now = now.Add(time.Minute)
	out, _ = distributePlannedStorages(s, bats, 10000, false)
	if math.Abs(out[0].TargetW-1500) > 1e-6 || math.Abs(out[1].TargetW-500*60./59) > 1e-6 {
		t.Fatalf("measured delivery=%+v", out)
	}
	bats[1].chargeBlocked = true
	out, _ = distributePlannedStorages(s, bats, 10000, false)
	if out[0].TargetW != 1500 || out[1].TargetW != 0 {
		t.Fatalf("reassigned blocked share=%+v", out)
	}
	dir.DecisionID = "smaller"
	dir.StorageEnergyWh = map[string]float64{"a": 10, "b": 0}
	out, _ = distributePlannedStorages(s, bats, 10000, false)
	if out[0].TargetW != 0 || out[1].TargetW != 0 {
		t.Fatalf("completed budget reversed=%+v", out)
	}
	if _, ok := distributePlannedStorages(s, bats, 1000, true); ok {
		t.Fatal("planner overrode manual hold")
	}
	s.PlanStale = true
	if _, ok := distributePlannedStorages(s, bats, 1000, false); ok {
		t.Fatal("stale plan allocated storage")
	}
}

// Field report, 23 Oct 2026: two identical Pixii batteries took turns. The
// planner is indifferent between them, so Energyplan put a whole slot on one
// battery and the next slot on the other; Core followed each battery's
// share and the SoCs drifted apart (74 % against 60 %).
func TestInterchangeableStoragesShareThePlanTotal(t *testing.T) {
	start := time.Now()
	now := start
	dir := SlotDirective{DecisionID: "p", SlotStart: start, SlotEnd: start.Add(15 * time.Minute), BatteryEnergyWh: 500,
		StorageEnergyWh: map[string]float64{"a": 500, "b": 0}}
	store := seedStore(0, []struct {
		name          string
		currentW, soc float64
	}{{"a", 0, .74}, {"b", 0, .60}})
	s := newStateWithEnergyDispatch(dir, "ferroamp")
	s.SlotDirective = func(time.Time) (SlotDirective, bool) { return dir, true }
	s.clock = func() time.Time { return now }
	for slot, plan := range []map[string]float64{{"a": 500, "b": 0}, {"a": 0, "b": 500}} {
		dir.SlotStart, dir.SlotEnd = now, now.Add(15*time.Minute)
		dir.StorageEnergyWh = plan
		out := ComputeDispatch(store, s, caps(map[string]float64{"a": 10000, "b": 10000}), 11040)
		if len(out) != 2 {
			t.Fatalf("slot %d targets=%+v", slot, out)
		}
		for _, v := range out {
			if math.Abs(v.TargetW-1000) > 1e-6 {
				t.Fatalf("slot %d: each battery should carry half of 2000 W, got %+v", slot, out)
			}
		}
		now = now.Add(15 * time.Minute)
	}
}

// The same two batteries on a cover-load slot: the plan put −564 W on one,
// the house drew 3 kW. Both batteries must cover the load, not one battery
// at its planned rate with the rest from the grid.
func TestInterchangeableStoragesShareALargeLoad(t *testing.T) {
	now := time.Now()
	dir := SlotDirective{DecisionID: "p", SlotStart: now, SlotEnd: now.Add(15 * time.Minute), BatteryEnergyWh: -141,
		StorageEnergyWh: map[string]float64{"a": -141, "b": 0}, PlannedGridW: 0, HasPlannedGridW: true}
	store := seedStore(3000, []struct {
		name          string
		currentW, soc float64
	}{{"a", 0, .74}, {"b", 0, .60}})
	s := newStateWithEnergyDispatch(dir, "ferroamp")
	s.clock = func() time.Time { return now }
	out := ComputeDispatch(store, s, caps(map[string]float64{"a": 10000, "b": 10000}), 11040)
	if len(out) != 2 {
		t.Fatalf("targets=%+v", out)
	}
	if math.Abs(out[0].TargetW-out[1].TargetW) > 1e-6 || out[0].TargetW+out[1].TargetW > -564-1e-6 {
		t.Fatalf("both batteries should share more than the planned 564 W, got %+v", out)
	}
}

// A battery on a hybrid inverter with a small limit is physically different:
// splitting 6 kW by capacity would ask it for 3 kW and lose 1.5 kW. The plan
// already put the energy where each inverter can deliver it, so Core keeps
// following each battery's planned energy.
func TestPhysicallyDifferentStoragesFollowThePlan(t *testing.T) {
	now := time.Now()
	dir := SlotDirective{DecisionID: "p", SlotStart: now, SlotEnd: now.Add(15 * time.Minute), BatteryEnergyWh: 1500,
		StorageEnergyWh: map[string]float64{"a": 1125, "b": 375}}
	store := seedStore(500, []struct {
		name          string
		currentW, soc float64
	}{{"a", 0, .5}, {"b", 0, .5}})
	s := newStateWithEnergyDispatch(dir, "ferroamp")
	s.clock = func() time.Time { return now }
	s.DriverLimits = map[string]PowerLimits{
		"a": {MaxChargeW: 5000, MaxDischargeW: 5000, MaxChargeWSet: true, MaxDischargeWSet: true},
		"b": {MaxChargeW: 1500, MaxDischargeW: 1500, MaxChargeWSet: true, MaxDischargeWSet: true},
	}
	out := ComputeDispatch(store, s, caps(map[string]float64{"a": 10000, "b": 10000}), 11040)
	want := map[string]float64{"a": 4500, "b": 1500}
	if len(out) != 2 {
		t.Fatalf("targets=%+v", out)
	}
	for _, v := range out {
		if math.Abs(v.TargetW-want[v.Driver]) > 1e-6 {
			t.Fatalf("targets=%+v, want %v", out, want)
		}
	}
}

func TestInterchangeableStoragesRule(t *testing.T) {
	limit := func(charge, discharge float64) batteryInfo {
		return batteryInfo{capacityWh: 10000, maxChargeW: charge, maxDischargeW: discharge, maxChargeWSet: true, maxDischargeWSet: true}
	}
	for _, tc := range []struct {
		name string
		bats []batteryInfo
		want bool
	}{
		{"identical defaults", []batteryInfo{{capacityWh: 10000}, {capacityWh: 10000}}, true},
		{"twice the capacity and twice the power", []batteryInfo{limit(3000, 3000), {capacityWh: 20000, maxChargeW: 6000, maxDischargeW: 6000, maxChargeWSet: true, maxDischargeWSet: true}}, true},
		{"4 % apart", []batteryInfo{limit(5000, 5000), limit(4800, 4800), limit(5000, 5000)}, true},
		{"6 % apart", []batteryInfo{limit(5000, 5000), limit(4700, 5000)}, false},
		{"spread across three", []batteryInfo{limit(4850, 5000), limit(5000, 5000), limit(5150, 5000)}, false},
		{"smaller inverter", []batteryInfo{limit(5000, 5000), limit(1500, 1500)}, false},
		{"same power, smaller pack", []batteryInfo{{capacityWh: 1000}, {capacityWh: 19000}}, false},
		{"charge disabled on one", []batteryInfo{limit(5000, 5000), limit(0, 5000)}, false},
		{"unknown capacity", []batteryInfo{{capacityWh: 10000}, {}}, false},
	} {
		if got := interchangeableStorages(tc.bats); got != tc.want {
			t.Errorf("%s: interchangeable=%v, want %v", tc.name, got, tc.want)
		}
	}
}
