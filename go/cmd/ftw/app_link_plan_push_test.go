package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/appproto"
	"github.com/srcfl/ftw/go/internal/appuplink"
	"github.com/srcfl/ftw/go/internal/control"
	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/internal/mpc"
	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

// A car plugged in reaches a streaming phone as a plan with its windows as
// soon as the planner has replanned, wired the way startAppLink wires it: the
// real planner, loadpoint manager, feed and handler. A viewer gets nothing.
func TestAPlugInReachesAStreamingPhoneAsAPlan(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC().Truncate(time.Hour)
	var prices []state.PricePoint
	for i := range 6 {
		prices = append(prices, state.PricePoint{
			Zone: "SE3", SlotTsMs: now.Add(time.Duration(i) * time.Hour).UnixMilli(), SlotLenMin: 60,
			SpotOreKwh: float64(140 - i*20), TotalOreKwh: float64(190 - i*20), Source: "test", FetchedAtMs: now.UnixMilli(),
		})
	}
	if err := st.SavePrices(prices); err != nil {
		t.Fatal(err)
	}

	// A battery online with a fresh level, which the planner waits for.
	tel := telemetry.NewStore()
	soc := 0.5
	tel.Update("bat", telemetry.DerBattery, 0, &soc, nil)
	tel.DriverHealthMut("bat").RecordSuccess()
	svc := mpc.New(st, tel, "SE3", mpc.Params{
		Mode: mpc.ModeSelfConsumption, SoCLevels: 11, ActionLevels: 5,
		CapacityWh: 10000, InitialSoC: 0.5, SoCMin: 0.1, SoCMax: 0.95,
		MaxChargeW: 3000, MaxDischargeW: 3000, ChargeEfficiency: 0.95, DischargeEfficiency: 0.95,
	})
	svc.Horizon = 5 * time.Hour
	svc.BaseLoad = 500
	// Production checks every ten seconds with a 30 s gap; the test waits
	// for neither, only for the planner to see the plug.
	svc.ReactiveInterval = 20 * time.Millisecond
	svc.MinReplanGap = 0

	mgr := loadpoint.NewManager()
	mgr.Load([]loadpoint.Config{{ID: "easee", DriverName: "easee", VehicleCapacityWh: 60000, MinChargeW: 4100, MaxChargeW: 11000}})
	deadline := len(prices)*4 - 1
	svc.Loadpoints = func(int) []*mpc.LoadpointSpec {
		var specs []*mpc.LoadpointSpec
		for _, lp := range mgr.States() {
			if lp.PluggedIn {
				specs = append(specs, &mpc.LoadpointSpec{
					ID: lp.ID, CapacityWh: 60000, Levels: 11, SoCMax: 1, InitialSoC: 0.3, PluggedIn: true,
					TargetSoC: 0.6, TargetSlotIdx: deadline, MaxChargeW: 11000, AllowedStepsW: []float64{0, 11000},
				})
			}
		}
		return specs
	}

	feed := appproto.NewPlanFeed(appproto.PlanSettle, func() bool { return svc.PlanSnapshot().Pending })
	svc.OnPlanChange(feed.Changed)
	cars := &appPlanCars{planner: svc, mgr: mgr}
	owner := streamingSession(t, cars, feed, apiauth.RoleOwner)
	viewer := streamingSession(t, cars, feed, apiauth.RoleViewer)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); svc.Stop() })
	svc.Start(ctx)
	first := owner.waitPlan(t, func(p appproto.Plan) bool { return len(p.Cars) == 1 && !p.Cars[0].PluggedIn })
	if len(first.Cars[0].Windows) != 0 {
		t.Fatalf("an unplugged car has windows: %+v", first.Cars[0])
	}

	plugged := time.Now()
	mgr.Observe("easee", true, 0, 0, true)
	got := owner.waitPlan(t, func(p appproto.Plan) bool {
		return len(p.Cars) == 1 && p.Cars[0].PluggedIn && len(p.Cars[0].Windows) > 0
	})
	took := time.Since(plugged)
	t.Logf("plug-in to pushed plan: %v, %d windows, %d Wh", took, len(got.Cars[0].Windows), got.Cars[0].TotalWh)
	if took > 3*time.Second {
		t.Fatalf("plan pushed %v after the plug-in", took)
	}
	if got.Rev <= first.Rev || got.Cars[0].Pending || got.Cars[0].Outdated {
		t.Fatalf("pushed rev %d after %d, car %+v", got.Rev, first.Rev, got.Cars[0])
	}
	if n := viewer.plans(t); len(n) != 0 {
		t.Fatalf("a viewer got %d plans", len(n))
	}
}

type pushedSession struct {
	sent *sentFrames
}

// streamingSession is one phone that said hello and subscribed.
func streamingSession(t *testing.T, cars appproto.PlanCars, feed *appproto.PlanFeed, role string) pushedSession {
	t.Helper()
	sent := &sentFrames{}
	h, err := appproto.New(appproto.Config{
		Clock: appproto.SystemClock{StartedAt: time.Now(), Source: "ntp"},
		Site:  emptySite{},
		// Not newAppBoxInfo: the box's zone is read once per process, and
		// a test that sets TZ later must still be the first to read it.
		Info:     appBoxInfo{id: "ftw-0001", build: "test", tz: "UTC"},
		Modes:    &appModes{},
		Plans:    &appPlans{planner: cars.(*appPlanCars).planner, ctrl: &control.State{}, ctrlMu: &sync.Mutex{}},
		PlanCars: cars,
		PlanFeed: feed,
		Codec:    appuplink.Codec(),
		Sender:   sent,
		Caller:   apiauth.Caller{Kind: apiauth.KindApp, Role: role, Scopes: appproto.ScopesForRole(role)},
		Grants:   roleGrant(role),
		Commands: appproto.NewCmdLog(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	for _, msg := range []struct {
		t string
		b any
	}{
		{appproto.MsgHello, appproto.Hello{Proto: appproto.ProtoRange{Min: 0, Max: appproto.ProtoMax}}},
		{appproto.MsgSub, appproto.Sub{Bucket: 512, Hz: 1}},
	} {
		b, err := appproto.Marshal(msg.b)
		if err != nil {
			t.Fatal(err)
		}
		frame, err := appuplink.Codec().EncodeFrame(appproto.Frame{Lane: appproto.LaneControl, Bucket: 512, Envelope: appproto.Envelope{T: msg.t, B: b}})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Handle(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
	}
	if !h.Subscribed() {
		t.Fatal("not streaming")
	}
	return pushedSession{sent: sent}
}

func (s pushedSession) plans(t *testing.T) []appproto.Plan {
	t.Helper()
	var out []appproto.Plan
	for _, raw := range s.sent.all() {
		f, err := appuplink.Codec().DecodeFrame(raw)
		if err != nil {
			t.Fatal(err)
		}
		if f.Envelope.T != appproto.MsgPlan {
			continue
		}
		var p appproto.Plan
		if err := appproto.Unmarshal(f.Envelope.B, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func (s pushedSession) waitPlan(t *testing.T, match func(appproto.Plan) bool) appproto.Plan {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		for _, p := range s.plans(t) {
			if match(p) {
				return p
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no matching plan in %d pushed", len(s.plans(t)))
	return appproto.Plan{}
}

type emptySite struct{}

func (emptySite) Snapshot() appproto.Snapshot { return appproto.Snapshot{} }

type roleGrant string

func (r roleGrant) Grant() (appproto.Enrolment, bool) {
	return appproto.Enrolment{Role: string(r), Epoch: 1}, true
}
