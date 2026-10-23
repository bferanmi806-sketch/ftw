package appuplink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/srcfl/ftw/go/internal/apiauth"
	"github.com/srcfl/ftw/go/internal/appenroll"
	"github.com/srcfl/ftw/go/internal/appproto"
	"github.com/srcfl/ftw/go/protocol/appwire"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

// The exchange: a watch's one-shot door to the box (ADR 0009, decisions 1
// and 2; docs/protocol-cloud.md, "Exchange").
//
// watchOS gives an app HTTPS and nothing lower, so a watch cannot hold a
// stream. It posts one Noise message to FTW Cloud inside a one-shot request,
// and the service hands it to the box on the uplink as x {dev, id, d}. The
// box answers x {dev, id, d} with the same dev and id. dev is the device the
// service's own handshake proved, which the box checks against the key its
// handshake proves; id is the service's number, which the box copies back
// and reads nothing into.
//
// Exchange sessions are a class of their own: a table of at most eight,
// apart from the phones' places, so a watch never pushes a phone out. They
// live on the box, not on the uplink, so a session survives the uplink
// reconnecting under it. Losing FTW Cloud changes nothing for local control
// or the planner.

const (
	// MaxExchangeSessions is how many watch sessions the box holds at once.
	MaxExchangeSessions = 8

	// exchangeFrameBytes is the one size every frame takes but hello_ok:
	// lane 0's 512 B bucket.
	exchangeFrameBytes = 512
	// exchangeOpenBytes is handshake message 1 with a lane 0 hello.
	exchangeOpenBytes = appwire.Message1Overhead + exchangeFrameBytes
	// exchangeRequestBytes is one transport message with one lane 0 frame.
	exchangeRequestBytes = appwire.TransportOverhead + exchangeFrameBytes
	// exchangeAnswerFrames is how many frames every answer after the
	// handshake carries, so a read, a command and a refusal have one length.
	exchangeAnswerFrames = 2

	// A session ends after four exchanges, a minute without one, or three
	// minutes, whichever comes first. A watch opens one when its confirm
	// screen appears and spends it within seconds.
	exchangeUses     = 4
	exchangeIdle     = 60 * time.Second
	exchangeLifetime = 3 * time.Minute

	// exchangeOpensPerMinute bounds handshakes, each of which costs the box
	// up to four X25519 operations. The rest are dropped unanswered.
	exchangeOpensPerMinute = 60

	// exchangeSettle is how long the box holds an answer for a command's
	// result. Core settles a charger's result in the pass that takes the
	// intent, within 2 s for the EV save, so one answer carries both. Past
	// this the answer carries a tick in the result's place and the watch
	// asks again with the same cmdId.
	exchangeSettle = 3 * time.Second

	// ExchangeCmdFloor is the soonest the box answers a watch's cmd, counted
	// from when the request arrived (ADR 0009, decision F; Fredrik, 14
	// October 2026). A refusal is ready in a millisecond and an applied
	// charge waits for the EV save, so without a floor the time to answer
	// would tell FTW Cloud which happened. A command that takes longer, an
	// EV save of up to 2 s, answers when it is done and waits no more.
	ExchangeCmdFloor = 400 * time.Millisecond

	// exchangeInFlight bounds requests handled at once, as many as FTW Cloud
	// sends a box at once. Past it a request is answered with silence.
	exchangeInFlight = cloudwire.XInFlight

	// exchangeInbox is how many requests wait for a slot.
	exchangeInbox = 2 * exchangeInFlight
)

// exchangePrologue binds an exchange handshake to this box, and keeps it
// apart from a stream handshake: one can never stand in for the other.
func exchangePrologue(boxStatic []byte) []byte {
	return append([]byte("ftw.exchange.v1:"), boxStatic...)
}

var errExchangeRate = errors.New("appuplink: too many exchange handshakes this minute")

// exchangeSession is one watch's short conversation with the box.
type exchangeSession struct {
	transport *appwire.Transport
	handler   *appproto.Handler
	// appStatic is the watch's key as the handshake authenticated it, and
	// dev the device id FTW Cloud named it by.
	appStatic []byte
	dev       string
	openedAt  time.Time

	// run lets one request at a time through the handler. A command whose
	// answer went without its result may still be running when the watch
	// asks again; the second request waits for it.
	run sync.Mutex

	mu     sync.Mutex
	lastAt time.Time
	uses   int
	// gen names the request whose frames are being caught, and catching
	// says whether one is. A frame from any other run is dropped.
	gen      uint64
	catching bool
	capture  [][]byte
}

// Send collects the handler's frames for the request being answered now. A
// frame sent when no answer is waiting, such as a result that settled after
// its answer went, is dropped: the command log holds it, and asking again
// returns it.
func (s *exchangeSession) Send(frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catching && len(s.capture) < exchangeAnswerFrames {
		s.capture = append(s.capture, append([]byte(nil), frame...))
	}
	return nil
}

// next numbers a request and stamps it at now.
func (s *exchangeSession) next(now time.Time) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uses++
	s.lastAt = now
	return uint64(s.uses)
}

// catch starts collecting frames for one request.
func (s *exchangeSession) catch(gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen, s.capture, s.catching = gen, nil, true
}

// release stops collecting and returns what the request sent. Nil when the
// request never started catching, because an earlier one still held the
// handler.
func (s *exchangeSession) release(gen uint64) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen || !s.catching {
		return nil
	}
	frames := s.capture
	s.capture, s.catching = nil, false
	return frames
}

// spent reports whether the session has run its course.
func (s *exchangeSession) spent(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uses >= exchangeUses ||
		now.Sub(s.lastAt) > exchangeIdle ||
		now.Sub(s.openedAt) > exchangeLifetime
}

func (s *exchangeSession) close() {
	s.handler.Close()
	s.transport.Close()
}

// exchanges is the box's table of watch sessions. One per box: a watch's
// request may arrive on the uplink before a reconnect or after it, and finds
// its session either way.
type exchanges struct {
	build  HandlerBuilder
	enroll *appenroll.Identity
	static appwire.KeyPair
	log    *slog.Logger
	// now is the clock session ages are measured on.
	now func() time.Time
	// settle is exchangeSettle; a test shortens it.
	settle time.Duration
	// cmdFloor is ExchangeCmdFloor as New wires it. Zero holds nothing,
	// which only a test that builds the table by hand gets.
	cmdFloor time.Duration

	mu    sync.Mutex
	live  []*exchangeSession
	opens []time.Time
}

// answer takes one request for the watch FTW Cloud names dev and returns the
// reply, or nil for silence. A stranger, a removed key, a key that is not
// dev's, a spent session, a malformed body and a handshake over the rate all
// get silence.
func (x *exchanges) answer(ctx context.Context, dev string, body []byte) []byte {
	arrived := time.Now()
	x.prune()
	switch len(body) {
	case exchangeOpenBytes:
		reply, err := x.open(ctx, dev, body)
		if err != nil {
			x.log.Debug("exchange handshake opened no session", "err", err)
			return nil
		}
		return reply
	case exchangeRequestBytes:
		return x.route(ctx, dev, body, arrived)
	default:
		return nil
	}
}

var errExchangeNotDev = errors.New("appuplink: the exchange handshake proved another device than FTW Cloud named")

// open runs the responder half of a handshake for a watch. The watch's hello
// rides in message 1 and its hello_ok in message 2, so a session costs one
// round trip.
func (x *exchanges) open(ctx context.Context, dev string, message1 []byte) ([]byte, error) {
	if !x.allowOpen() {
		return nil, errExchangeRate
	}
	handshake, err := appwire.NewResponder(appwire.HandshakeOptions{
		StaticKey: x.static,
		Prologue:  exchangePrologue(x.static.Public()),
	})
	if err != nil {
		return nil, err
	}
	payload, err := handshake.ReadMessage(message1)
	if err != nil {
		return nil, err
	}
	frame, err := appwire.DecodeFrame(payload)
	if err != nil {
		return nil, err
	}
	if frame.Envelope.T != appproto.MsgHello {
		return nil, fmt.Errorf("appuplink: message 1 carries %q, not a hello", frame.Envelope.T)
	}
	var hello appproto.Hello
	if err := appproto.Unmarshal(frame.Envelope.B, &hello); err != nil {
		return nil, err
	}

	// FTW Cloud names the device by the key its own handshake proved. One
	// that differs from the key this handshake proves is answered with
	// silence, so a service cannot spend one watch's requests on another.
	if cloudwire.DeviceID(handshake.RemoteStatic()) != dev {
		return nil, errExchangeNotDev
	}
	// Checked before anything is written: answering a stranger would hand
	// them a fresh ephemeral key for nothing.
	grant, err := x.enroll.AuthoriseWatch(handshake.RemoteStatic(), hello.Code)
	if err != nil {
		return nil, err
	}

	now := x.now()
	sess := &exchangeSession{appStatic: handshake.RemoteStatic(), dev: dev, openedAt: now, lastAt: now}
	handler, err := x.build(sess, apiauth.Caller{
		Subject: subjectOf(grant.DeviceID),
		Kind:    apiauth.KindApp,
		Role:    grant.Role,
		Scopes:  appproto.ScopesForGrant(grant.Role, grant.ParentRole),
		Epoch:   grant.Epoch,
	}, liveGrant{enroll: x.enroll, deviceID: grant.DeviceID})
	if err != nil {
		return nil, err
	}
	sess.handler = handler

	sess.catch(0)
	err = handler.Handle(ctx, payload)
	frames := sess.release(0)
	if err != nil || len(frames) != 1 {
		handler.Close()
		return nil, fmt.Errorf("appuplink: hello made %d frames: %v", len(frames), err)
	}
	reply, err := handshake.WriteMessage(frames[0])
	if err != nil {
		handler.Close()
		return nil, err
	}
	noise, err := handshake.Split()
	if err != nil {
		handler.Close()
		return nil, err
	}
	sess.transport = appwire.NewTransport(noise)
	x.admit(sess)
	return reply, nil
}

// route finds the session a transport message belongs to by letting each of
// dev's sessions' AEAD try, and runs the exchange in it.
func (x *exchanges) route(ctx context.Context, dev string, message []byte, arrived time.Time) []byte {
	x.mu.Lock()
	live := slices.Clone(x.live)
	x.mu.Unlock()

	for _, sess := range live {
		if sess.dev != dev {
			continue
		}
		frame, err := sess.transport.Decrypt(message)
		if err != nil {
			continue
		}
		reply := x.exchange(ctx, sess, frame)
		if reply != nil && x.cmdFloor > 0 && isCmd(frame) {
			holdUntil(ctx, arrived.Add(x.cmdFloor))
		}
		if sess.spent(x.now()) {
			x.drop(sess)
		}
		return reply
	}
	return nil
}

// isCmd reports whether a request frame is a command.
func isCmd(frame []byte) bool {
	f, err := appwire.DecodeFrame(frame)
	return err == nil && f.Envelope.T == appproto.MsgCmd
}

// holdUntil waits for a moment on the real clock, or for the box to stop.
func holdUntil(ctx context.Context, at time.Time) {
	wait := time.Until(at)
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// exchange runs one request through the session's handler and seals the
// answer: exactly two lane 0 frames. A command's ack and result when the
// result settles in time; otherwise a tick takes the missing place.
func (x *exchanges) exchange(ctx context.Context, sess *exchangeSession, frame []byte) []byte {
	gen := sess.next(x.now())
	done := make(chan error, 1)
	go func() {
		sess.run.Lock()
		defer sess.run.Unlock()
		sess.catch(gen)
		done <- sess.handler.Handle(ctx, frame)
	}()

	wait := x.settle
	if wait <= 0 {
		wait = exchangeSettle
	}
	settle := time.NewTimer(wait)
	select {
	case err := <-done:
		if err != nil {
			x.log.Warn("watch exchange failed on a frame", "err", err)
		}
	case <-settle.C:
	case <-ctx.Done():
	}
	settle.Stop()

	frames := sess.release(gen)
	for len(frames) < exchangeAnswerFrames {
		tick, err := sess.handler.PadFrame()
		if err != nil {
			return nil
		}
		frames = append(frames, tick)
	}

	var reply []byte
	for _, f := range frames {
		sealed, err := sess.transport.Encrypt(f)
		if err != nil {
			return nil
		}
		reply = append(reply, sealed...)
	}
	return reply
}

// allowOpen counts a handshake against the minute's budget.
func (x *exchanges) allowOpen() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	now := x.now()
	x.opens = slices.DeleteFunc(x.opens, func(at time.Time) bool { return now.Sub(at) >= time.Minute })
	if len(x.opens) >= exchangeOpensPerMinute {
		return false
	}
	x.opens = append(x.opens, now)
	return true
}

// admit keeps a new session, ending the oldest when the table is full. The
// newcomer is the watch somebody is holding now.
func (x *exchanges) admit(sess *exchangeSession) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.live = append(x.live, sess)
	for len(x.live) > MaxExchangeSessions {
		x.live[0].close()
		x.live = x.live[1:]
	}
}

// prune ends every session that has run its course.
func (x *exchanges) prune() {
	now := x.now()
	x.mu.Lock()
	defer x.mu.Unlock()
	x.live = slices.DeleteFunc(x.live, func(s *exchangeSession) bool {
		if s.spent(now) {
			s.close()
			return true
		}
		return false
	})
}

func (x *exchanges) drop(sess *exchangeSession) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.live = slices.DeleteFunc(x.live, func(s *exchangeSession) bool {
		if s == sess {
			s.close()
			return true
		}
		return false
	})
}

// dropByAppKey ends every session a removed key holds. Its next message 1
// meets AuthoriseWatch and gets silence.
func (x *exchanges) dropByAppKey(pub []byte) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	dropped := 0
	x.live = slices.DeleteFunc(x.live, func(s *exchangeSession) bool {
		if bytes.Equal(s.appStatic, pub) {
			s.close()
			dropped++
			return true
		}
		return false
	})
	return dropped
}

func (x *exchanges) count() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.live)
}

// --------------------------------------------------------------------------
// The carrier
// --------------------------------------------------------------------------

// XSender sends one x up the box's link to FTW Cloud. cloudlink.Link is one.
// It never blocks; an error means the answer did not leave, and the watch
// asks again.
type XSender interface {
	SendX(m cloudwire.X) error
}

// Exchange is the box's door for watches through FTW Cloud: the service
// hands it each x, and it answers each with one x, the table's reply or
// silence.
type Exchange struct {
	table *exchanges
	log   *slog.Logger
	inbox chan cloudwire.X

	mu     sync.Mutex
	sender XSender
}

func newExchange(table *exchanges, log *slog.Logger) *Exchange {
	return &Exchange{table: table, log: log, inbox: make(chan cloudwire.X, exchangeInbox)}
}

// Exchange returns the uplink's door for watches, for the box's link to FTW
// Cloud to deliver to. Nil without an ExchangeHandler.
func (u *Uplink) Exchange() *Exchange { return u.exchange }

// Attach names where answers go.
func (e *Exchange) Attach(s XSender) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sender = s
}

// Deliver takes one x from the service. It never blocks: past the inbox the
// request is answered with silence at once, so the watch hears no later
// than it would from a box that kept quiet.
func (e *Exchange) Deliver(m cloudwire.X) {
	select {
	case e.inbox <- m:
	default:
		e.reply(m, nil)
	}
}

// Run answers requests until ctx ends, exchangeInFlight at a time. A
// command holds its slot for the floor and its result, up to a few seconds,
// so requests run beside each other.
func (e *Exchange) Run(ctx context.Context) {
	slots := make(chan struct{}, exchangeInFlight)
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-e.inbox:
			select {
			case slots <- struct{}{}:
			default:
				e.reply(m, nil)
				continue
			}
			go func() {
				defer func() { <-slots }()
				e.reply(m, e.table.answer(ctx, m.Dev, m.D))
			}()
		}
	}
}

// reply sends the answer under the request's dev and id. Silence is an
// empty d, so the service answers the watch at once rather than at its
// deadline.
func (e *Exchange) reply(m cloudwire.X, d []byte) {
	e.mu.Lock()
	s := e.sender
	e.mu.Unlock()
	if s == nil {
		return
	}
	if err := s.SendX(cloudwire.X{Dev: m.Dev, ID: m.ID, D: d}); err != nil {
		e.log.Debug("an exchange answer did not leave", "err", err)
	}
}
