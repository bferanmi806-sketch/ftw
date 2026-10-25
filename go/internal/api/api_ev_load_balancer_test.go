package api

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/srcfl/ftw/go/internal/config"
	"github.com/srcfl/ftw/go/internal/drivers"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

// Field trace, 23 Oct 2026 (srcfl/ftw#1763), Easee Cloud: FTW ordered 15 A
// (10.35 kW) throughout and the charger read back 15 A, while Easee's own
// load balancing signalled 13 A to the car, which took 8.85 kW. FTW said
// "Not following". The driver now reports the limit and, when its
// allocation shows it, names the load balancing.
func TestEaseeLoadBalancingReadsAsLimited(t *testing.T) {
	const named = `,"current_limit_a":13,"current_limited_by":"load_balancer"`
	for _, tc := range []struct {
		name, extra              string
		drawW                    float64
		status, reason, severity string
	}{
		{"named", named, 8850, "limited", "load_balancer_limit", "info"},
		{"cause not shown", `,"current_limit_a":13`, 8850, "limited", "offered_current_lower", "warning"},
		{"no limit reported", ``, 8850, "not_following", "power_below_target", "warning"},
		{"a 13 A limit does not explain zero", named, 0, "not_following", "no_power_response", "warning"},
		{"nor a car taking far less", named, 4000, "not_following", "power_below_target", "warning"},
		{"limit at the order", `,"current_limit_a":15,"current_limited_by":"load_balancer"`, 8850, "not_following", "power_below_target", "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEVTrace(10370)
				for range 5 * 60 / 5 {
					e.tick(10350, "plan")
				}
				if f := e.verdict(t); f.Status != "following" {
					t.Fatalf("before the cut: %s/%s", f.Status, f.Reason)
				}
				e.drawW, e.changedAt, e.extra = tc.drawW, time.Now(), tc.extra
				cut := time.Now()
				var f ControlFeedback
				for range 6 * 60 / 5 {
					e.tick(10350, "plan")
					f = e.verdict(t)
					if tc.status == "limited" && f.Status == "not_following" {
						t.Fatalf("at %v: not following (%s)", time.Since(cut), f.Reason)
					}
				}
				if f.Status != tc.status || f.Reason != tc.reason || f.Severity != tc.severity {
					t.Fatalf("got %s/%s/%s, want %s/%s/%s", f.Status, f.Reason, f.Severity, tc.status, tc.reason, tc.severity)
				}
				if tc.reason != "load_balancer_limit" {
					return
				}
				// Requested, allocated and delivered stay apart.
				if f.RequestedA == nil || math.Abs(*f.RequestedA-15) > 0.01 || f.LimitA == nil || *f.LimitA != 13 ||
					f.LimitedBy != "load_balancer" || f.ActualW == nil || *f.ActualW != 8850 {
					t.Fatalf("numbers: requested %v, limit %v by %q, actual %v", f.RequestedA, f.LimitA, f.LimitedBy, f.ActualW)
				}
			})
		})
	}
}

// The sentence names the charger's make with its limit, from the identity
// its driver reported. Rows without a limit do not carry it.
func TestChargerLimitCarriesTheMake(t *testing.T) {
	dir := t.TempDir()
	lua := filepath.Join(dir, "easee_like.lua")
	src := `
function driver_init(config) host.set_make("Easee") end
function driver_poll() return 60000 end
function driver_command(action, power_w, cmd) return true end
function driver_default_mode() end
`
	if err := os.WriteFile(lua, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	tel := telemetry.NewStore()
	reg := drivers.NewRegistry(tel)
	t.Cleanup(reg.ShutdownAll)
	if err := reg.Add(context.Background(), config.Driver{Name: "easee", Lua: lua}); err != nil {
		t.Fatal(err)
	}
	srv := New(&Deps{Tel: tel, Registry: reg})
	for _, tc := range []struct {
		extra, make string
	}{
		{`,"current_limit_a":13,"current_limited_by":"load_balancer"`, "Easee"},
		{``, ""},
	} {
		c := tel.BeginCommand("easee", []byte(`{"action":"ev_set_current","power_w":10350}`), time.Now())
		tel.CompleteCommand(c, "accepted")
		tel.Update("easee", telemetry.DerEV, 8850, nil, []byte(`{"connected":true`+tc.extra+`}`))
		rows := srv.controlFeedback(time.Now())
		if len(rows) != 1 || rows[0].Make != tc.make {
			t.Fatalf("extra %q: rows %+v, want make %q", tc.extra, rows, tc.make)
		}
	}
}
