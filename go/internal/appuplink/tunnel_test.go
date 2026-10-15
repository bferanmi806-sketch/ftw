package appuplink

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/appenroll"
	"github.com/srcfl/ftw/go/internal/appproto"
	"github.com/srcfl/ftw/go/internal/control"
	"github.com/srcfl/ftw/go/protocol/appwire"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

// capturedTun is the box's link to FTW Cloud, as the tunnel sees it.
type capturedTun struct {
	ch chan cloudwire.Tun
}

func (c *capturedTun) SendTun(t cloudwire.Tun) bool {
	select {
	case c.ch <- t:
		return true
	default:
		return false
	}
}

type tunRig struct {
	enroll *appenroll.Identity
	tunnel *Tunnel
	slots  *slots
	out    *capturedTun
	modes  *countingModes
}

func newTunRig(t *testing.T) *tunRig {
	t.Helper()
	enroll, err := appenroll.LoadOrCreate(filepath.Join(t.TempDir(), "nova.key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &tunRig{enroll: enroll, slots: &slots{}, out: &capturedTun{ch: make(chan cloudwire.Tun, 64)},
		modes: &countingModes{}}
	r.tunnel = newTunnel(handlerOver(r.modes, appproto.NewCmdLog()), enroll, r.slots, slog.New(slog.DiscardHandler))
	r.tunnel.Attach(r.out)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.tunnel.Run(ctx)
	return r
}

// pair enrols a phone directly, as a pairing over any carrier would.
func (r *tunRig) pair(t *testing.T) *appClient {
	t.Helper()
	code, _, err := r.enroll.MintPairingCode(apiauth.RoleOwner, appenroll.PairingTTL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := newAppClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.enroll.Authorise(app.staticPublic(), code); err != nil {
		t.Fatal(err)
	}
	return app
}

// next is the next tun the box sent, failing after d.
func (r *tunRig) next(t *testing.T, d time.Duration) cloudwire.Tun {
	t.Helper()
	select {
	case m := <-r.out.ch:
		return m
	case <-time.After(d):
		t.Fatalf("the box sent no tun within %s", d)
		return cloudwire.Tun{}
	}
}

// silent fails if the box sends a tun within d.
func (r *tunRig) silent(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-r.out.ch:
		t.Fatalf("the box sent %+v", m)
	case <-time.After(d):
	}
}

// open runs the handshake for app under dev and sid, through the tunnel.
func (r *tunRig) open(t *testing.T, app *appClient, dev string, sid uint32, code []byte) {
	t.Helper()
	m1, err := app.message1(r.enroll.StaticKey().Public(), code)
	if err != nil {
		t.Fatal(err)
	}
	r.tunnel.Deliver(cloudwire.Tun{Dev: dev, Sid: sid, D: m1})
	reply := r.next(t, 3*time.Second)
	if reply.Dev != dev || reply.Sid != sid || reply.Ended() {
		t.Fatalf("the handshake's answer is %+v", reply)
	}
	if err := app.readMessage2(reply.D); err != nil {
		t.Fatal(err)
	}
}

func (r *tunRig) send(t *testing.T, app *appClient, dev string, sid uint32, msgType string, body any) {
	t.Helper()
	raw, err := appproto.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := appwire.EncodeFrame(appwire.Frame{Lane: appwire.LaneControl, Bucket: 512,
		Envelope: appwire.Envelope{T: msgType, B: raw}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := app.encrypt(frame)
	if err != nil {
		t.Fatal(err)
	}
	r.tunnel.Deliver(cloudwire.Tun{Dev: dev, Sid: sid, D: msg})
}

// receive is the next frame of type want for app, skipping lane 0 ticks.
func (r *tunRig) receive(t *testing.T, app *appClient, want string) appwire.Frame {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := r.next(t, time.Until(deadline))
		if m.Ended() {
			t.Fatalf("the box ended the session while waiting for %s", want)
		}
		plain, err := app.decrypt(m.D)
		if err != nil {
			t.Fatal(err)
		}
		f, err := appwire.DecodeFrame(plain)
		if err != nil {
			t.Fatal(err)
		}
		if f.Envelope.T == want {
			return f
		}
	}
	t.Fatalf("no %s", want)
	return appwire.Frame{}
}

func (r *tunRig) hello(t *testing.T, app *appClient, dev string, sid uint32) {
	t.Helper()
	r.send(t, app, dev, sid, appproto.MsgHello, appproto.Hello{
		Proto: appproto.ProtoRange{Min: 0, Max: appproto.ProtoMax},
		App:   appproto.AppInfo{Build: "test", UA: "ios"},
	})
	r.receive(t, app, appproto.MsgHelloOK)
}

func setModeCmd(id string) appproto.Cmd {
	return appproto.Cmd{
		CmdID: id, Op: appproto.OpSetMode,
		Args:            map[string]any{"mode": string(control.ModeSelfConsumption)},
		NotValidAfterMs: 10 * 60 * 1000, Expect: appproto.Expect{Rev: 1},
	}
}

// A paired phone's session runs through the tunnel as over the relay: the
// same handshake, the same hello and a command the box acts on and answers.
func TestATunnelSessionAnswersACommand(t *testing.T) {
	r := newTunRig(t)
	app := r.pair(t)
	dev := deviceIDOf(app.staticPublic())
	r.open(t, app, dev, 7, nil)
	r.hello(t, app, dev, 7)

	r.send(t, app, dev, 7, appproto.MsgCmd, setModeCmd("0192f2a0-7c1e-7000-8000-00000000a001"))
	r.receive(t, app, appproto.MsgCmdAck)
	var res appproto.CmdResult
	if err := appproto.Unmarshal(r.receive(t, app, appproto.MsgCmdResult).Envelope.B, &res); err != nil {
		t.Fatal(err)
	}
	if res.State != appproto.CmdApplied || r.modes.count() != 1 {
		t.Fatalf("result %q after %d mode changes", res.State, r.modes.count())
	}
}

// The service names the device, and the box checks it against the key the
// handshake proved. A session put under another device's id opens nothing
// and gets no answer.
func TestAForgedDevGetsNothing(t *testing.T) {
	r := newTunRig(t)
	app := r.pair(t)
	other := r.pair(t)
	m1, err := app.message1(r.enroll.StaticKey().Public(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.tunnel.Deliver(cloudwire.Tun{Dev: deviceIDOf(other.staticPublic()), Sid: 1, D: m1})
	r.silent(t, 500*time.Millisecond)
	if r.tunnel.count() != 0 {
		t.Fatal("a session opened under another device's id")
	}
}

// A key the box never paired gets nothing, through the tunnel as over the
// relay, and neither does a wrong pairing code.
func TestAStrangerThroughTheTunnelGetsNothing(t *testing.T) {
	r := newTunRig(t)
	if _, _, err := r.enroll.MintPairingCode(apiauth.RoleOwner, appenroll.PairingTTL); err != nil {
		t.Fatal(err)
	}
	for _, code := range [][]byte{nil, make([]byte, appenroll.PairingCodeBytes)} {
		app, err := newAppClient()
		if err != nil {
			t.Fatal(err)
		}
		m1, err := app.message1(r.enroll.StaticKey().Public(), code)
		if err != nil {
			t.Fatal(err)
		}
		r.tunnel.Deliver(cloudwire.Tun{Dev: deviceIDOf(app.staticPublic()), Sid: 1, D: m1})
		r.silent(t, 400*time.Millisecond)
	}
	if r.tunnel.count() != 0 || r.enroll.AuthorisedCount() != 0 {
		t.Fatal("a stranger got a session or a pairing")
	}
}

// A new phone pairs through the tunnel with the code from the QR, inside its
// handshake, exactly as over the relay.
func TestPairingThroughTheTunnel(t *testing.T) {
	r := newTunRig(t)
	code, _, err := r.enroll.MintPairingCode(apiauth.RoleViewer, appenroll.PairingTTL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := newAppClient()
	if err != nil {
		t.Fatal(err)
	}
	dev := cloudwire.DeviceID(app.staticPublic())
	r.open(t, app, dev, 3, code)
	r.hello(t, app, dev, 3)
	if g, ok := r.enroll.GrantFor(dev); !ok || g.Role != apiauth.RoleOwner {
		t.Fatalf("the first phone through the tunnel holds %+v", g)
	}
}

// One slot per device: a new session from the same device ends its older one
// and tells the phone, and a session of another device stays.
func TestOneSessionPerDevice(t *testing.T) {
	r := newTunRig(t)
	app := r.pair(t)
	other := r.pair(t)
	dev, otherDev := deviceIDOf(app.staticPublic()), deviceIDOf(other.staticPublic())
	r.open(t, other, otherDev, 1, nil)
	r.open(t, app, dev, 1, nil)

	again := &appClient{static: app.static}
	m1, err := again.message1(r.enroll.StaticKey().Public(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.tunnel.Deliver(cloudwire.Tun{Dev: dev, Sid: 2, D: m1})
	var sawEnd, sawReply bool
	for !(sawEnd && sawReply) {
		m := r.next(t, 3*time.Second)
		switch {
		case m.Dev == dev && m.Sid == 1 && m.Ended():
			sawEnd = true
		case m.Dev == dev && m.Sid == 2 && !m.Ended():
			if err := again.readMessage2(m.D); err != nil {
				t.Fatal(err)
			}
			sawReply = true
		}
	}
	if r.tunnel.count() != 2 {
		t.Fatalf("%d sessions, want the new one and the other device's", r.tunnel.count())
	}
}

// A relay session and a tunnel session of one device share its slot: the
// newer ends the older, whichever carrier either rides.
func TestTheRelayAndTheTunnelShareADevicesSlot(t *testing.T) {
	r := newTunRig(t)
	app := r.pair(t)
	dev := deviceIDOf(app.staticPublic())
	streams := &sessions{
		slots: r.slots, build: newHandler, enroll: r.enroll, static: r.enroll.StaticKey(),
		write: func([]byte) error { return nil }, log: slog.New(slog.DiscardHandler),
	}
	viaRelay := &appClient{static: app.static}
	m1, err := viaRelay.message1(r.enroll.StaticKey().Public(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := streams.open(m1); err != nil {
		t.Fatal(err)
	}
	r.open(t, app, dev, 9, nil)
	if streams.count() != 0 {
		t.Fatal("the relay session outlived the device's tunnel session")
	}

	viaRelay = &appClient{static: app.static}
	m1, _ = viaRelay.message1(r.enroll.StaticKey().Public(), nil)
	if err := streams.open(m1); err != nil {
		t.Fatal(err)
	}
	if end := r.next(t, 3*time.Second); end.Dev != dev || end.Sid != 9 || !end.Ended() {
		t.Fatalf("the tunnel session ended with %+v", end)
	}
	if r.tunnel.count() != 0 || streams.count() != 1 {
		t.Fatalf("%d tunnel and %d relay sessions", r.tunnel.count(), streams.count())
	}
}

// A message for a session the box does not hold gets an end, so the phone
// starts again at once instead of waiting.
func TestAnUnknownSessionGetsAnEnd(t *testing.T) {
	r := newTunRig(t)
	app := r.pair(t)
	dev := deviceIDOf(app.staticPublic())
	r.tunnel.Deliver(cloudwire.Tun{Dev: dev, Sid: 4, D: bytes.Repeat([]byte{1}, 600)})
	if m := r.next(t, 3*time.Second); m.Dev != dev || m.Sid != 4 || !m.Ended() {
		t.Fatalf("answer %+v", m)
	}
}

// The uplink ending ends every session, and a message queued from it opens
// nothing afterwards.
func TestDropEndsEverySession(t *testing.T) {
	r := newTunRig(t)
	app := r.pair(t)
	dev := deviceIDOf(app.staticPublic())
	r.open(t, app, dev, 1, nil)
	r.tunnel.Drop()
	if r.tunnel.count() != 0 {
		t.Fatal("a session outlived its uplink")
	}
	r.silent(t, 300*time.Millisecond)
}

// The phone ending its session, with an empty d, ends it at the box.
func TestAnEmptyDEndsTheSession(t *testing.T) {
	r := newTunRig(t)
	app := r.pair(t)
	dev := deviceIDOf(app.staticPublic())
	r.open(t, app, dev, 1, nil)
	r.tunnel.Deliver(cloudwire.Tun{Dev: dev, Sid: 1})
	deadline := time.Now().Add(3 * time.Second)
	for r.tunnel.count() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the phone's end left the session open")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Revoking a phone ends its tunnel session too.
func TestARevokeEndsATunnelSession(t *testing.T) {
	r := newTunRig(t)
	enrolOwner(t, r.enroll)
	app := r.pair(t)
	dev := deviceIDOf(app.staticPublic())
	r.open(t, app, dev, 1, nil)
	removed, err := r.enroll.Revoke(dev, appenroll.AtTheBox)
	if err != nil {
		t.Fatal(err)
	}
	if n := r.tunnel.dropByAppKey(removed[0].Key); n != 1 || r.tunnel.count() != 0 {
		t.Fatalf("dropped %d, %d left", n, r.tunnel.count())
	}
}

