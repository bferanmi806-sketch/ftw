package appuplink

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/appenroll"
	"github.com/srcfl/ftw/go/internal/appproto"
	"github.com/srcfl/ftw/go/protocol/appwire"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

// movingSite is stubSite with a grid reading the test moves.
type movingSite struct{ gridW atomic.Int64 }

func (m *movingSite) Snapshot() appproto.Snapshot {
	s := stubSite{}.Snapshot()
	s.GridW = float64(m.gridW.Load())
	return s
}

// arrivalRig is a tunnel table that hears arrivals, with one phone
// subscribed through it.
type arrivalRig struct {
	*tunRig
	site     *movingSite
	arrivals chan struct{}
	app      *appClient
}

func newArrivalRig(t *testing.T) *arrivalRig {
	t.Helper()
	enroll, err := appenroll.LoadOrCreate(filepath.Join(t.TempDir(), "nova.key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &arrivalRig{site: &movingSite{}, arrivals: make(chan struct{}, 1),
		tunRig: &tunRig{enroll: enroll, slots: &slots{}, out: &capturedTun{ch: make(chan cloudwire.Tun, 64)}, modes: &countingModes{}}}
	r.site.gridW.Store(1200)
	cmds := appproto.NewCmdLog()
	build := func(s appproto.Sender, caller apiauth.Caller, grants appproto.GrantReader) (*appproto.Handler, error) {
		return appproto.New(appproto.Config{
			Clock: appproto.SystemClock{StartedAt: time.Now(), Source: "ntp"},
			Site:  r.site, Info: stubInfo{}, Modes: r.modes, Plans: stubPlans{}, Codec: codec{},
			Sender: s, Caller: caller, Grants: grants, Commands: cmds,
			SrcGrid: "meter", SrcPV: "meter", SrcBattery: "meter", Logger: slog.New(slog.DiscardHandler),
		})
	}
	r.tunnel = newTunnel(build, enroll, r.slots, r.arrivals, slog.New(slog.DiscardHandler))
	r.tunnel.Attach(r.out)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.tunnel.Run(ctx)

	r.app = r.pair(t)
	dev := deviceIDOf(r.app.staticPublic())
	r.open(t, r.app, dev, 7, nil)
	r.hello(t, r.app, dev, 7)
	r.send(t, r.app, dev, 7, appproto.MsgSub, appproto.Sub{Bucket: 512, Hz: 1})
	r.receive(t, r.app, appproto.MsgSnap)
	return r
}

// laneZero is the next lane 0 reading the phone gets, when it got it, and
// its padded size.
func (r *arrivalRig) laneZero(t *testing.T, within time.Duration) (appwire.Frame, time.Time, int) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		m := r.next(t, time.Until(deadline))
		at := time.Now()
		plain, err := r.app.decrypt(m.D)
		if err != nil {
			t.Fatal(err)
		}
		f, err := appwire.DecodeFrame(plain)
		if err != nil {
			t.Fatal(err)
		}
		if f.Envelope.T == appproto.MsgDelta || f.Envelope.T == appproto.MsgTick {
			return f, at, len(plain)
		}
	}
}

// Through FTW Cloud a reading leaves the box as it lands, not on the next
// pulse of a one-second clock, and still in its fixed-size bucket. Five
// readings at different phases of the second all go within 200 ms; with
// the pulse, all five doing so would be a one-in-ten-thousand fluke.
func TestAReadingLeavesThroughTheTunnelAsItArrives(t *testing.T) {
	r := newArrivalRig(t)
	for i := range 5 {
		time.Sleep(time.Duration(130+97*i) * time.Millisecond)
		want := int64(2000 + i)
		r.site.gridW.Store(want)
		at := time.Now()
		r.arrivals <- struct{}{}
		for {
			f, got, size := r.laneZero(t, 2*time.Second)
			if size != 512 {
				t.Fatalf("a %s left in %d bytes, want the 512-byte bucket", f.Envelope.T, size)
			}
			var d appproto.Delta
			if f.Envelope.T != appproto.MsgDelta || appproto.Unmarshal(f.Envelope.B, &d) != nil ||
				d.Fields["2"] != want {
				continue // a beat that crossed the arrival
			}
			if took := got.Sub(at); took > 200*time.Millisecond {
				t.Fatalf("reading %d took %s to leave the box", i, took)
			}
			break
		}
	}
}

// With nothing arriving, the session still hears from the box once a
// second, so the phone never reads a quiet house as a lost carrier.
func TestAQuietTunnelSessionStillBeats(t *testing.T) {
	r := newArrivalRig(t)
	_, first, _ := r.laneZero(t, 2*time.Second)
	f, second, size := r.laneZero(t, 2*time.Second)
	if f.Envelope.T != appproto.MsgTick || size != 512 {
		t.Fatalf("a quiet beat sent a %s in %d bytes, want a 512-byte tick", f.Envelope.T, size)
	}
	if gap := second.Sub(first); gap < 900*time.Millisecond || gap > 1500*time.Millisecond {
		t.Fatalf("beats %s apart, want about a second", gap)
	}
}

// Drivers that each read once a second never wait for the pace; only a
// stream faster than five frames a second does.
func TestThePaceTakesThreeAtOnceAndFiveASecond(t *testing.T) {
	t0 := time.Unix(1_760_000_000, 0)
	p := pace{tokens: tunBurst, at: t0}
	for i := range 3 {
		if w := p.wait(t0); w != 0 {
			t.Fatalf("frame %d of a burst waits %s", i, w)
		}
		p.take(t0)
	}
	if w := p.wait(t0); w != 200*time.Millisecond {
		t.Fatalf("a fourth frame at once waits %s, want 200ms", w)
	}

	// A poll every 100 ms for ten seconds gets five frames a second.
	sent := 0
	next := t0
	for at := t0; at.Before(t0.Add(10 * time.Second)); at = at.Add(100 * time.Millisecond) {
		if at.Before(next) {
			continue
		}
		if w := p.wait(at); w > 0 {
			next = at.Add(w)
			continue
		}
		p.take(at)
		sent++
	}
	if sent < 49 || sent > 52 {
		t.Fatalf("%d frames in ten seconds of polls every 100 ms, want about 50", sent)
	}
}
