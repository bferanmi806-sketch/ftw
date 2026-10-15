package appproto

import (
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/mpc"
)

const testSettle = 20 * time.Millisecond

// fakeChargers is each charger's share of the plan as a test sets it.
type fakeChargers struct{ chargers atomic.Pointer[[]PlanCharger] }

func (f *fakeChargers) set(c []PlanCharger)                  { f.chargers.Store(&c) }
func (f *fakeChargers) PlanChargers(time.Time) []PlanCharger { return *f.chargers.Load() }
func (b *fakeBox) replan(rev uint64, startMs int64)          { b.plan, b.planRev = planAt(startMs), rev }

func planAt(startMs int64) *mpc.Plan {
	return &mpc.Plan{
		GeneratedAtMs: startMs,
		Actions: []mpc.Action{
			{SlotStartMs: startMs, SlotLenMin: 15, BatteryW: 2000, GridW: 2400, PriceOre: 40, SoC: 0.5},
			{SlotStartMs: startMs + 900_000, SlotLenMin: 15, BatteryW: 0, GridW: 400, PriceOre: 90, SoC: 0.55},
		},
	}
}

// newFeedRig is a phone's session on a box with a charger and a feed.
func newFeedRig(t *testing.T, feed *PlanFeed, caller apiauth.Caller, grants *fakeGrants, exchange bool) (*Handler, *fakeBox, *recorder, *fakeChargers) {
	t.Helper()
	clock := &fakeClock{uptimeMs: 60_000, now: time.UnixMilli(1_760_000_000_000)}
	box := &fakeBox{identity: Identity{ID: "ftw-0001", Build: "test", TZ: "Europe/Stockholm"}}
	box.replan(1, clock.now.UnixMilli())
	chargers := &fakeChargers{}
	chargers.set([]PlanCharger{{ID: "easee", Windows: []PlanChargerWindow{}}})
	rec := &recorder{}
	h, err := New(Config{
		Clock: clock, Site: box, Info: box, Modes: box, Plans: box,
		PlanChargers: chargers, PlanFeed: feed, Exchange: exchange,
		Codec: testCodec{}, Sender: rec, Caller: caller, Grants: grants, Commands: NewCmdLog(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(h.Close)
	return h, box, rec, chargers
}

func plans(rec *recorder) []sent {
	var out []sent
	for _, f := range rec.snapshot() {
		if f.env.T == MsgPlan {
			out = append(out, f)
		}
	}
	return out
}

// waitPlans waits for n pushed plans, then a while longer, so a test also
// sees a push that should not have come.
func waitPlans(t *testing.T, rec *recorder, n int) []sent {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for len(plans(rec)) < n && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	return settled(rec)
}

// settled is every plan pushed once the feed has had five settles to push.
func settled(rec *recorder) []sent {
	time.Sleep(5 * testSettle)
	return plans(rec)
}

func TestAReplanPushesThePlanWithTheChargerToAStreamingPhone(t *testing.T) {
	feed := NewPlanFeed(testSettle, nil)
	h, box, rec, chargers := newFeedRig(t, feed, ownerCaller(), newGrants(), false)
	subscribe(t, h, rec)

	// A car is plugged in and the planner publishes a plan that charges it.
	box.replan(2, box.plan.GeneratedAtMs+60_000)
	chargers.set([]PlanCharger{{ID: "easee", PluggedIn: true, Windows: []PlanChargerWindow{{StartMs: 1, EndMs: 2, Wh: 7400}}, TotalWh: 7400}})
	start := time.Now()
	feed.Changed()

	got := waitPlans(t, rec, 1)
	if len(got) != 1 {
		t.Fatalf("want one pushed plan, got %d (%s)", len(got), rec.types())
	}
	if took := time.Since(start); took > testSettle+time.Second {
		t.Fatalf("plan pushed after %v", took)
	}
	if got[0].env.ID != nil {
		t.Fatal("a pushed plan answers no request")
	}
	if got[0].lane != LaneBulk {
		t.Fatalf("plan on lane %d", got[0].lane)
	}
	p := body[Plan](t, got[0])
	if p.Rev != 2 || len(p.Slots) != 2 {
		t.Fatalf("plan rev %d with %d slots", p.Rev, len(p.Slots))
	}
	if len(p.Chargers) != 1 || !p.Chargers[0].PluggedIn || len(p.Chargers[0].Windows) != 1 || p.Chargers[0].Windows[0].Wh != 7400 {
		t.Fatalf("chargers %+v", p.Chargers)
	}
}

func TestTwoReplansInsideTheSettleArePushedOnce(t *testing.T) {
	feed := NewPlanFeed(testSettle, nil)
	h, box, rec, _ := newFeedRig(t, feed, ownerCaller(), newGrants(), false)
	subscribe(t, h, rec)

	box.replan(2, box.plan.GeneratedAtMs+60_000)
	feed.Changed()
	box.replan(3, box.plan.GeneratedAtMs+60_000)
	feed.Changed()

	got := waitPlans(t, rec, 1)
	if len(got) != 1 {
		t.Fatalf("want one push, got %d", len(got))
	}
	if p := body[Plan](t, got[0]); p.Rev != 3 {
		t.Fatalf("pushed rev %d, want the newest", p.Rev)
	}
}

func TestThePushWaitsForANewerPlanBeingMade(t *testing.T) {
	var newer atomic.Bool
	feed := NewPlanFeed(testSettle, newer.Load)
	h, box, rec, _ := newFeedRig(t, feed, ownerCaller(), newGrants(), false)
	subscribe(t, h, rec)

	// A plan is published while a newer request is already running: the
	// phone would see it for a moment and then lose it.
	newer.Store(true)
	box.replan(2, box.plan.GeneratedAtMs+60_000)
	feed.Changed()
	if got := settled(rec); len(got) != 0 {
		t.Fatalf("pushed a plan that is about to go")
	}

	newer.Store(false)
	box.replan(3, box.plan.GeneratedAtMs+60_000)
	feed.Changed()
	got := waitPlans(t, rec, 1)
	if len(got) != 1 || body[Plan](t, got[0]).Rev != 3 {
		t.Fatalf("want the newer plan once, got %d", len(got))
	}
}

func TestAPhoneWithoutThePlanScopeGetsNoPush(t *testing.T) {
	feed := NewPlanFeed(testSettle, nil)
	h, _, rec, _ := newFeedRig(t, feed, viewerCaller(), newViewerGrants(), false)
	subscribe(t, h, rec)

	feed.Changed()
	if got := settled(rec); len(got) != 0 {
		t.Fatalf("a viewer got %d plans", len(got))
	}
}

func TestADemotedPhoneStopsGettingPushes(t *testing.T) {
	feed := NewPlanFeed(testSettle, nil)
	grants := newGrants()
	h, _, rec, _ := newFeedRig(t, feed, ownerCaller(), grants, false)
	subscribe(t, h, rec)

	grants.setRole(apiauth.RoleViewer)
	feed.Changed()
	if got := settled(rec); len(got) != 0 {
		t.Fatalf("a demoted phone got %d plans", len(got))
	}
}

func TestNoPushBeforeTheStreamOrAfterTheSessionCloses(t *testing.T) {
	feed := NewPlanFeed(testSettle, nil)
	h, _, rec, _ := newFeedRig(t, feed, ownerCaller(), newGrants(), false)

	feed.Changed()
	if got := settled(rec); len(got) != 0 {
		t.Fatalf("pushed %d plans before sub", len(got))
	}

	subscribe(t, h, rec)
	h.Close()
	feed.Changed()
	if got := settled(rec); len(got) != 0 {
		t.Fatalf("pushed %d plans after close", len(got))
	}
}

func TestAWatchExchangeIsNotOnTheFeed(t *testing.T) {
	feed := NewPlanFeed(testSettle, nil)
	newFeedRig(t, feed, ownerCaller(), newGrants(), true)
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if len(feed.members) != 0 {
		t.Fatal("an exchange joined the feed")
	}
}

// The far tail of a long plan gives way to the chargers, never the session.
func TestALongPlanWithTwoChargersStillFitsABulkFrame(t *testing.T) {
	feed := NewPlanFeed(testSettle, nil)
	h, box, rec, chargers := newFeedRig(t, feed, ownerCaller(), newGrants(), false)
	subscribe(t, h, rec)

	start := box.plan.GeneratedAtMs
	long := &mpc.Plan{GeneratedAtMs: start}
	for i := range 193 {
		long.Actions = append(long.Actions, mpc.Action{SlotStartMs: start + int64(i)*900_000, SlotLenMin: 15, BatteryW: -2500, GridW: -1200, PriceOre: 123.4, SoC: 0.5})
	}
	box.plan, box.planRev = long, 9
	var two []PlanCharger
	for _, id := range []string{"easee", "zaptec-garage"} {
		ch := PlanCharger{ID: id, PluggedIn: true, TotalWh: 99_999}
		for i := range 32 {
			ch.Windows = append(ch.Windows, PlanChargerWindow{StartMs: start + int64(i)*1_800_000, EndMs: start + int64(i)*1_800_000 + 900_000, Wh: 2_750})
		}
		short := int64(12_000)
		ch.ShortfallWh = &short
		two = append(two, ch)
	}
	chargers.set(two)
	feed.Changed()

	got := waitPlans(t, rec, 1)
	if len(got) != 1 {
		t.Fatalf("want one push, got %d (%s)", len(got), rec.types())
	}
	p := body[Plan](t, got[0])
	if len(p.Chargers) != 2 || len(p.Chargers[1].Windows) != 32 || *p.Chargers[1].ShortfallWh != 12_000 {
		t.Fatalf("chargers cut: %d", len(p.Chargers))
	}
	if len(p.Slots) < 96 {
		t.Fatalf("only %d slots left", len(p.Slots))
	}
}

// planChargersVector is a plan with two chargers as the box encodes it. The same hex
// is decoded in srcfl/ftw-app's PlanPushTests, so a change here is a change
// to the apps' wire.
const planChargersVector = "a6637265760765736c6f747381a765677269645719012c66726561736f6e70657870656e736976655f696d706f72746773746172744d731b000001a131ea48806862617474657279573904af6a6475726174696f6e4d731a000dbba06a70726963654d696e6f7218576b736f635065726d696c6c65190280657374616c65f4686365696c696e6757f668636861726765727382a76269646565617365656770656e64696e67f467746f74616c5768191ce86777696e646f777381a3627768191ce865656e644d731b000001a1325825806773746172744d731b000001a131ea4880686f75746461746564f469706c7567676564496ef56b73686f727466616c6c57681904b0a762696466676172616765656361724964782030313233343536373839616263646566303132333435363738396162636465666770656e64696e67f567746f74616c5768006777696e646f777380686f75746461746564f569706c7567676564496ef468757074696d654d731a0036ee80"

func TestThePlanWithChargersVector(t *testing.T) {
	short, price, soc := int64(1200), int64(87), int64(640)
	p := Plan{
		Rev: 7, UptimeMs: 3_600_000,
		Slots: []PlanSlot{{StartMs: 1_791_838_800_000, DurationMs: 900_000, BatteryW: -1200, GridW: 300, PriceMinor: &price, Reason: ReasonExpensiveImport, SocPermille: &soc}},
		Chargers: []PlanCharger{
			{ID: "easee", PluggedIn: true, Windows: []PlanChargerWindow{{StartMs: 1_791_838_800_000, EndMs: 1_791_846_000_000, Wh: 7400}}, TotalWh: 7400, ShortfallWh: &short},
			{ID: "garage", CarID: "0123456789abcdef0123456789abcdef", Pending: true, Outdated: true, Windows: []PlanChargerWindow{}},
		},
	}
	b, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != planChargersVector {
		t.Fatalf("plan bytes moved:\n got %s\nwant %s", got, planChargersVector)
	}
}
