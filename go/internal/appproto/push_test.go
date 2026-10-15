package appproto

import "testing"

// A session through FTW Cloud hears a poll the moment it lands, and only
// what it changed. A poll that changed nothing sends nothing.
func TestPushSendsWhatAPollChangedAndNothingElse(t *testing.T) {
	h, box, rec, _ := newRig(t)
	subscribe(t, h, rec)

	if err := h.Push(); err != nil {
		t.Fatal(err)
	}
	if got := rec.types(); got != "[]" {
		t.Fatalf("a poll that changed nothing sent %s", got)
	}

	box.snap.GridW = 1450
	if err := h.Push(); err != nil {
		t.Fatal(err)
	}
	f := rec.only(t, MsgDelta)
	if f.lane != LaneControl || f.size != 512 {
		t.Fatalf("pushed delta: lane %d, %d bytes; want lane 0 in its 512-byte bucket", f.lane, f.size)
	}
	d := body[Delta](t, f)
	if len(d.Fields) != 1 || d.Fields[fidKey(FidGridW)] != 1450 {
		t.Fatalf("pushed delta carries %v, want only grid_w 1450", d.Fields)
	}

	rec.reset()
	if err := h.Push(); err != nil {
		t.Fatal(err)
	}
	if got := rec.types(); got != "[]" {
		t.Fatalf("the same reading again sent %s", got)
	}
}

// Hidden, the phone asked for one frame in five seconds. A poll does not
// break that; the beat carries what changed when the five seconds are up.
func TestPushWaitsForTheBeatWhileHidden(t *testing.T) {
	h, box, rec, clock := newRig(t)
	subscribe(t, h, rec)
	deliver(t, h, MsgSub, nil, Sub{Bucket: 512, Hz: 0.2})

	box.snap.GridW = 1450
	if err := h.Push(); err != nil {
		t.Fatal(err)
	}
	clock.uptimeMs += 4_999
	if err := h.Beat(); err != nil {
		t.Fatal(err)
	}
	if got := rec.types(); got != "[]" {
		t.Fatalf("hidden session sent %s within its five seconds", got)
	}
	clock.uptimeMs++
	if err := h.Beat(); err != nil {
		t.Fatal(err)
	}
	if d := body[Delta](t, rec.only(t, MsgDelta)); d.Fields[fidKey(FidGridW)] != 1450 {
		t.Fatalf("the hidden beat carries %v, want grid_w 1450", d.Fields)
	}
}

// Silence on lane 0 means the session is gone, so a quiet house still gets
// a tick once per subscribed period, counted from the last frame of any kind.
func TestBeatTicksOnlyAfterAQuietPeriod(t *testing.T) {
	h, box, rec, clock := newRig(t)
	subscribe(t, h, rec)

	clock.uptimeMs += 999
	if err := h.Beat(); err != nil {
		t.Fatal(err)
	}
	if got := rec.types(); got != "[]" {
		t.Fatalf("beat %d ms after the snapshot sent %s", 999, got)
	}
	clock.uptimeMs++
	if err := h.Beat(); err != nil {
		t.Fatal(err)
	}
	tick := rec.only(t, MsgTick)
	if tick.size != 512 {
		t.Fatalf("beat tick is %d bytes, want the 512-byte bucket", tick.size)
	}

	// A pushed delta resets the quiet period.
	rec.reset()
	clock.uptimeMs += 500
	box.snap.GridW = 1450
	if err := h.Push(); err != nil {
		t.Fatal(err)
	}
	clock.uptimeMs += 999
	if err := h.Beat(); err != nil {
		t.Fatal(err)
	}
	if got := rec.types(); got != "[delta]" {
		t.Fatalf("got %s; a beat within a second of the delta must send nothing", got)
	}
	clock.uptimeMs++
	if err := h.Beat(); err != nil {
		t.Fatal(err)
	}
	if got := rec.types(); got != "[delta tick]" {
		t.Fatalf("got %s, want the delta and then one tick", got)
	}

	// One sequence across pushes and beats, with no gap for a poll that
	// changed nothing.
	frames := rec.snapshot()
	if d, k := body[Delta](t, frames[0]), body[Tick](t, frames[1]); d.Seq != 2 || k.Seq != 3 {
		t.Fatalf("seq %d then %d, want 2 then 3", d.Seq, k.Seq)
	}
}
