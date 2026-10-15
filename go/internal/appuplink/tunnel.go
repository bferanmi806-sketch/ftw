package appuplink

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/appenroll"
	"github.com/srcfl/ftw/go/internal/appproto"
	"github.com/srcfl/ftw/go/protocol/appwire"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

// The tunnel: the phones' sessions that FTW Cloud carries
// (docs/protocol-cloud.md, "Tunnel").
//
// A phone's session through the service is the same ftw.session.v1 session
// it runs over the relay, byte for byte. Only the routing differs. The relay
// cannot say whose a message is, so the box lets each session's AEAD try. The
// service says which device and which session, tun {dev, sid, d}, so the box
// looks the session up. It still trusts nothing the service says: the
// handshake proves the phone's key and the box admits it by that key, exactly
// as over the relay. dev only picks the slot, and a handshake whose key is
// not the device dev names is dropped.

// TunSender sends one tun up the box's link to FTW Cloud. cloudlink.Link is
// one. bulk says the message carries a lane 1 frame, which the link paces;
// everything else goes first. False means the message will not leave.
type TunSender interface {
	SendTun(t cloudwire.Tun, bulk bool) bool
}

const (
	// tunInbox is how many tun messages wait for the table. The link's
	// reader never waits on it; past it a message is dropped, which Noise
	// transport survives at the cost of one frame.
	tunInbox = 256
	// tunSessionInbox is the same, per session, so one phone's slow
	// command never holds up another phone.
	tunSessionInbox = 64
	// tunOpensPerDevice bounds one device's handshakes through the service
	// a minute, so one phone or one pairing cannot starve the others.
	// tunOpensPerMinute bounds them all, each of which costs the box up to
	// four X25519 operations.
	tunOpensPerDevice = 12
	tunOpensPerMinute = 240
)

// Tunnel is the box's table of sessions that FTW Cloud carries: one per
// device, in the slot that device holds across every carrier.
type Tunnel struct {
	build  HandlerBuilder
	enroll *appenroll.Identity
	static appwire.KeyPair
	slots  *slots
	log    *slog.Logger
	now    func() time.Time

	inbox chan tunIn

	mu     sync.Mutex
	sender TunSender
	// gen moves on every Drop, so a message queued from an uplink that has
	// ended opens nothing on the next.
	gen   uint64
	live  map[string]*tunSession
	opens map[string][]time.Time
}

type tunIn struct {
	t   cloudwire.Tun
	gen uint64
}

// tunSession is one phone's session through the service.
type tunSession struct {
	dev string
	sid uint32
	gen uint64
	*session
	in   chan []byte
	stop chan struct{}
	once sync.Once
}

// Tunnel returns the uplink's tunnel table, for the box's link to FTW Cloud
// to deliver to. Its sessions take the same slots as the relay's.
func (u *Uplink) Tunnel() *Tunnel { return u.tunnel }

func newTunnel(build HandlerBuilder, enroll *appenroll.Identity, slots *slots, log *slog.Logger) *Tunnel {
	return &Tunnel{
		build: build, enroll: enroll, static: enroll.StaticKey(), slots: slots, log: log, now: time.Now,
		inbox: make(chan tunIn, tunInbox), live: map[string]*tunSession{}, opens: map[string][]time.Time{},
	}
}

// Attach names where answers go. The box's link calls nothing until then.
func (t *Tunnel) Attach(s TunSender) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sender = s
}

// Deliver takes one tun from the service. It never blocks.
func (t *Tunnel) Deliver(m cloudwire.Tun) {
	t.mu.Lock()
	gen := t.gen
	t.mu.Unlock()
	select {
	case t.inbox <- tunIn{t: m, gen: gen}:
	default:
		t.log.Debug("tunnel inbox full; a message was dropped")
	}
}

// Drop ends every session, when the uplink that carried them ends. The
// phones start again on the next.
func (t *Tunnel) Drop() {
	t.mu.Lock()
	t.gen++
	live := t.live
	t.live = map[string]*tunSession{}
	t.mu.Unlock()
	for _, s := range live {
		s.end()
	}
}

// Run serves the table until ctx ends: the messages the service delivers,
// and the one-second pulse lane 0 needs.
func (t *Tunnel) Run(ctx context.Context) {
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	defer t.Drop()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-t.inbox:
			t.route(ctx, m)
		case <-tick.C:
			t.tick()
		}
	}
}

// count is how many sessions the table holds.
func (t *Tunnel) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.live)
}

// route hands one message to its session, or opens one.
func (t *Tunnel) route(ctx context.Context, m tunIn) {
	t.mu.Lock()
	if m.gen != t.gen {
		t.mu.Unlock()
		return
	}
	cur := t.live[m.t.Dev]
	t.mu.Unlock()

	switch {
	case cur != nil && cur.sid == m.t.Sid && m.t.Ended():
		t.remove(cur)
	case cur != nil && cur.sid == m.t.Sid:
		select {
		case cur.in <- m.t.D:
		default:
			t.log.Debug("tunnel session inbox full; a message was dropped")
		}
	case m.t.Ended():
		// The end of a session the box no longer holds.
	default:
		if err := t.open(ctx, m); err != nil {
			t.log.Debug("tun opened no session", "err", err)
		}
	}
}

var (
	errTunRate   = errors.New("appuplink: too many tunnel handshakes this minute")
	errTunDevice = errors.New("appuplink: the handshake's key is not the device the service named")
	errTunShort  = errors.New("appuplink: too short to be handshake message 1")
)

// open runs the responder half of a handshake for a new session. A message
// that is not one gets an end back, so the phone starts again; a handshake
// from a key the box does not admit, or that the service put under another
// device, gets nothing.
func (t *Tunnel) open(ctx context.Context, m tunIn) error {
	end := cloudwire.Tun{Dev: m.t.Dev, Sid: m.t.Sid}
	if len(m.t.D) < appwire.Message1Overhead {
		t.send(end)
		return errTunShort
	}
	if !t.allowOpen(m.t.Dev) {
		return errTunRate
	}
	handshake, err := appwire.NewResponder(appwire.HandshakeOptions{
		StaticKey: t.static,
		Prologue:  prologue(t.static.Public()),
	})
	if err != nil {
		return err
	}
	payload, err := handshake.ReadMessage(m.t.D)
	if err != nil {
		// Not message 1 of this box's session: most often a frame of a
		// session the box dropped. The phone starts again.
		t.send(end)
		return err
	}
	// The service names the device; the handshake proves it. A key that is
	// not the named device's is the service, or a bug, putting one phone's
	// session under another's slot.
	if cloudwire.DeviceID(handshake.RemoteStatic()) != m.t.Dev {
		return errTunDevice
	}
	// Built before the caller is checked and sent only after, as over the
	// relay.
	reply, err := handshake.WriteMessage(nil)
	if err != nil {
		return err
	}
	noise, err := handshake.Split()
	if err != nil {
		return err
	}
	transport := appwire.NewTransport(noise)
	grant, err := t.enroll.Authorise(noise.RemoteStatic, payload)
	if err != nil {
		transport.Close()
		return err
	}

	ts := &tunSession{dev: m.t.Dev, sid: m.t.Sid, gen: m.gen,
		in: make(chan []byte, tunSessionInbox), stop: make(chan struct{})}
	send := func(msg []byte, bulk bool) bool {
		return t.sendAs(cloudwire.Tun{Dev: ts.dev, Sid: ts.sid, D: msg}, bulk)
	}
	caller := apiauth.Caller{
		Subject: subjectOf(grant.DeviceID),
		Kind:    apiauth.KindApp,
		Role:    grant.Role,
		Scopes:  appproto.ScopesForGrant(grant.Role, grant.ParentRole),
		Epoch:   grant.Epoch,
	}
	handler, err := t.build(tunSender{transport: transport, send: send}, caller,
		liveGrant{enroll: t.enroll, deviceID: grant.DeviceID})
	if err != nil {
		transport.Close()
		return err
	}
	ts.session = &session{transport: transport, handler: handler, appStatic: noise.RemoteStatic}
	// The device's one slot, whichever carrier held it before. Claiming it
	// ends the older session, through the relay or through here.
	ts.release = t.slots.claim(grant.DeviceID, func() { t.remove(ts) })

	t.mu.Lock()
	if m.gen != t.gen {
		// The uplink ended while the handshake ran.
		t.mu.Unlock()
		ts.end()
		return nil
	}
	old := t.live[ts.dev]
	t.live[ts.dev] = ts
	t.mu.Unlock()
	if old != nil {
		old.end()
	}
	select {
	case <-ts.stop:
		// A newer session of the same device claimed the slot meanwhile.
		t.remove(ts)
		return nil
	default:
	}

	go ts.serve(ctx, t)
	// Written after the session is registered, so a fast phone whose first
	// frame arrives at once meets a session that exists.
	if !t.send(cloudwire.Tun{Dev: ts.dev, Sid: ts.sid, D: reply}) {
		t.remove(ts)
		return errTunGone
	}
	t.log.Info("app session open through FTW Cloud")
	return nil
}

var errTunGone = errors.New("appuplink: the tunnel's uplink is gone")

// tunSender encrypts one session's frames for the tunnel and says which are
// bulk, so the link paces those and sends lane 0 (commands, their answers
// and the ticks) first.
type tunSender struct {
	transport *appwire.Transport
	send      func(msg []byte, bulk bool) bool
}

func (s tunSender) Send(frame []byte) error {
	msg, err := s.transport.Encrypt(frame)
	if err != nil {
		return err
	}
	if !s.send(msg, len(frame) > 1 && frame[1] == appwire.LaneBulk) {
		return errTunGone
	}
	return nil
}

// serve runs one session's frames in order, on its own goroutine.
func (s *tunSession) serve(ctx context.Context, t *Tunnel) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case d := <-s.in:
			frame, err := s.transport.Decrypt(d)
			if err != nil {
				// A replay, or bytes that are not this session's. The
				// service can delay and drop; it cannot make the box act.
				continue
			}
			if err := s.handler.Handle(ctx, frame); err != nil {
				t.log.Warn("app session failed on a frame", "err", err)
				t.remove(s)
				return
			}
		}
	}
}

// end closes the session once and frees its slot. It reports whether this
// call ended it.
func (s *tunSession) end() bool {
	ended := false
	s.once.Do(func() {
		ended = true
		close(s.stop)
		s.session.close()
	})
	return ended
}

// remove takes a session out of the table and ends it, telling the phone
// when the uplink that carried it is still up.
func (t *Tunnel) remove(s *tunSession) {
	t.mu.Lock()
	if t.live[s.dev] == s {
		delete(t.live, s.dev)
	}
	current := s.gen == t.gen
	t.mu.Unlock()
	if s.end() && current {
		t.send(cloudwire.Tun{Dev: s.dev, Sid: s.sid})
	}
}

// send hands one tun to the link, ahead of bulk: a handshake's answer and
// an end.
func (t *Tunnel) send(m cloudwire.Tun) bool { return t.sendAs(m, false) }

func (t *Tunnel) sendAs(m cloudwire.Tun, bulk bool) bool {
	t.mu.Lock()
	s := t.sender
	t.mu.Unlock()
	return s != nil && s.SendTun(m, bulk)
}

// allowOpen counts a handshake against its device's allowance for the
// minute, and against the box's.
func (t *Tunnel) allowOpen(dev string) bool {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	total := 0
	for d, list := range t.opens {
		kept := list[:0]
		for _, at := range list {
			if now.Sub(at) < time.Minute {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(t.opens, d)
			continue
		}
		t.opens[d] = kept
		total += len(kept)
	}
	if total >= tunOpensPerMinute || len(t.opens[dev]) >= tunOpensPerDevice {
		return false
	}
	t.opens[dev] = append(t.opens[dev], now)
	return true
}

// tick sends the one-second pulse to every session.
func (t *Tunnel) tick() {
	t.mu.Lock()
	live := make([]*tunSession, 0, len(t.live))
	for _, s := range t.live {
		live = append(live, s)
	}
	t.mu.Unlock()
	for _, s := range live {
		if err := s.handler.Tick(); err != nil {
			t.log.Warn("app session failed on a tick", "err", err)
			t.remove(s)
		}
	}
}

// dropByAppKey ends every session a revoked key runs, as the relay's table
// does.
func (t *Tunnel) dropByAppKey(pub []byte) int {
	t.mu.Lock()
	var gone []*tunSession
	for _, s := range t.live {
		if bytes.Equal(s.appStatic, pub) {
			gone = append(gone, s)
		}
	}
	t.mu.Unlock()
	for _, s := range gone {
		_ = s.handler.Terminate(appproto.TerminateRevoked)
		t.remove(s)
	}
	return len(gone)
}

// --------------------------------------------------------------------------
// One slot per device
// --------------------------------------------------------------------------

// slots holds each device's one session, whichever carrier it rides. A new
// session from a device ends its older one, so a phone that reconnects, a
// watch or another phone never pushes out a different device's session.
type slots struct {
	mu   sync.Mutex
	n    uint64
	held map[string]slot
}

type slot struct {
	id  uint64
	end func()
}

// claim gives dev's slot to a new session, whose end func closes it, and
// ends the session that held it. The returned func frees the slot if this
// session still holds it. A nil table claims nothing.
func (s *slots) claim(dev string, end func()) func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.held == nil {
		s.held = map[string]slot{}
	}
	s.n++
	id := s.n
	old, had := s.held[dev]
	s.held[dev] = slot{id: id, end: end}
	s.mu.Unlock()
	if had {
		old.end()
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if cur, ok := s.held[dev]; ok && cur.id == id {
			delete(s.held, dev)
		}
	}
}
