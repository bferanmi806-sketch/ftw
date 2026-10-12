package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/appproto"
	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

func TestPriceColumnsRepeatLongSlotsAndLeaveGapsNull(t *testing.T) {
	t0 := int64(1_791_763_200_000)
	q := int64(15 * 60_000)
	p := priceColumns("SE3", "SEK", []appproto.PricePoint{
		{StartMs: t0 + 4*q, LenMin: 60, SpotMinor: 40.4, TotalMinor: 110.6},
		{StartMs: t0, LenMin: 15, SpotMinor: 41.5, TotalMinor: 112},
		{StartMs: t0 + q, LenMin: 15, SpotMinor: -2.4, TotalMinor: 60},
	})
	if p.T0Ms != t0 || p.SlotMs != q || len(p.SpotMinor) != 8 || len(p.TotalMinor) != 8 {
		t.Fatalf("columns %+v", p)
	}
	want := []any{int64(42), int64(-2), nil, nil, int64(40), int64(40), int64(40), int64(40)}
	for i, w := range want {
		got := p.SpotMinor[i]
		if (w == nil) != (got == nil) || (got != nil && *got != w.(int64)) {
			t.Fatalf("slot %d = %v, want %v", i, got, w)
		}
	}
}

func TestEachPlugInIsANewSession(t *testing.T) {
	c := &cloudSite{}
	if s := c.session("garage", false); s != "" {
		t.Fatalf("unplugged session %q", s)
	}
	first := c.session("garage", true)
	if again := c.session("garage", true); again != first || first == "" {
		t.Fatalf("one plug-in gave %q then %q", first, again)
	}
	c.session("garage", false)
	if next := c.session("garage", true); next == first {
		t.Fatal("a second plug-in kept the first session")
	}
	if other := c.session("drive", true); other == "" {
		t.Fatal("another loadpoint has no session")
	}
}

// The wire shape has no room for limits, phases, battery size or the car's
// name, and an unplugged car has no level.
func TestCloudLoadpointLeavesCapabilitiesAndNamesBehind(t *testing.T) {
	c := &cloudSite{}
	st := loadpoint.State{
		ID: "garage", DriverName: "easee", PluggedIn: false, CurrentSoC: 0.5,
		MaxChargeW: 11_000, MinChargeW: 1_400, Phases: 3, VehicleCapacityWh: 77_000,
		VehicleName: "Familjebilen", VehicleDriver: "tesla-fleet",
	}
	lp := c.loadpoint(st, time.Now())
	if lp.SocPermille != nil || lp.Session != "" {
		t.Fatalf("unplugged loadpoint carries a level or a session: %+v", lp)
	}
	raw, err := cloudwire.Marshal(lp)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"Familjebilen", "tesla-fleet"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Fatalf("%q is in the loadpoint", leak)
		}
	}

	st.PluggedIn, st.SoCSource, st.VehicleSoCAgeS = true, "vehicle", 90
	lp = c.loadpoint(st, time.Now())
	if lp.SocPermille == nil || *lp.SocPermille != 500 || lp.SocAgeS == nil || *lp.SocAgeS != 90 {
		t.Fatalf("plugged loadpoint %+v", lp)
	}
}
