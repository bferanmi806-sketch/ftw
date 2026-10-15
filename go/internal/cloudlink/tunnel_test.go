package cloudlink

import (
	"bytes"
	"crypto/ecdsa"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/srcfl/ftw/go/protocol"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

// A box links when its first phone may open a tunnel, with the readable copy
// off: it sends its roster and nothing to read.
func TestTheBoxLinksForItsPhonesAndSendsNoReadings(t *testing.T) {
	svc := newFakeService(t)
	h := newHarness(t, svc)
	if _, err := os.Stat(h.keyPath); !os.IsNotExist(err) {
		t.Fatal("cloud.key exists before anything needs the link")
	}
	h.setRoster(func(r *fakeRosterState) { r.tun = true })

	r := h.nextRoster(t)
	if len(r.Devices) != 1 || !r.Devices[0].Tun {
		t.Fatalf("roster devices %+v", r.Devices)
	}
	if len(r.Grant.ID) != 0 || len(r.Grant.Scopes) != 0 || len(r.Grant.Purposes) != 0 ||
		r.Grant.Controller != cloudwire.ControllerSourceful {
		t.Fatalf("a roster without a grant names %+v", r.Grant)
	}
	var hello cloudwire.Hello
	svc.mu.Lock()
	payload, _ := cloudwire.DecodeFrame(svc.hello)
	svc.mu.Unlock()
	env, _ := cloudwire.DecodeEnvelope(payload)
	if err := cloudwire.Unmarshal(env.B, &hello); err != nil {
		t.Fatal(err)
	}
	if len(hello.GrantID) != 0 || !slices.Equal(hello.Caps, []string{cloudwire.MsgRoster, cloudwire.CapTun}) {
		t.Fatalf("hello names grant %x and caps %v", hello.GrantID, hello.Caps)
	}

	time.Sleep(2500 * time.Millisecond)
	for _, env := range svc.received() {
		if env.T != cloudwire.MsgRoster {
			t.Fatalf("%s left a box whose readable copy is off", env.T)
		}
	}
	st := h.link.Status()
	if st.Link != "on" || st.State != "off" || st.Home != svc.home {
		t.Fatalf("status %+v", st)
	}
	if s := h.link.SessionInfo(); s == nil || s.Home != svc.home || len(s.Sources) != 0 {
		t.Fatalf("session information %+v", s)
	}
}

// A QR code on the screen links the box and is listed by its hash, never by
// the code.
func TestAPendingCodeIsListedByItsHash(t *testing.T) {
	svc := newFakeService(t)
	h := newHarness(t, svc)
	code := bytes.Repeat([]byte{0x42}, 16)
	h.setRoster(func(r *fakeRosterState) {
		r.gone, r.code, r.expires = true, code, time.Now().Add(10*time.Minute)
	})
	home := h.link.Status().Home
	if home == "" {
		t.Fatal("a code on the screen did not link the box at once")
	}
	r := h.nextRoster(t)
	if len(r.Pending) != 1 || !bytes.Equal(r.Pending[0].Hash, cloudwire.PairingHash(home, code)) ||
		r.Pending[0].TTLS < 590 || r.Pending[0].TTLS > 600 {
		t.Fatalf("pending %+v", r.Pending)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	for _, raw := range svc.raw {
		if bytes.Contains(raw, code) {
			t.Fatal("the pairing code itself left the house")
		}
	}
}

// tun goes both ways unsigned, reaches the Tunnel unread, and counts against
// its own budget: a readable copy past its budget does not stop it, and the
// tunnel past its own does not stop the copy.
func TestTunGoesBothWaysApartFromTheBudget(t *testing.T) {
	svc := newFakeService(t)
	svc.afterHello = func(conn *websocket.Conn, home string) {
		svc.write(conn, nil, home, cloudwire.MsgTun, cloudwire.Tun{Dev: "q5Lw0Zb1", Sid: 9, D: []byte{1, 2, 3}})
	}
	h := newHarness(t, svc)
	h.link.mu.Lock()
	h.link.budget.add(h.link.uptimeMs(), budgetBytes)
	h.link.mu.Unlock()
	h.setRoster(func(r *fakeRosterState) { r.tun = true })

	select {
	case got := <-h.tunnel.ch:
		if got.Dev != "q5Lw0Zb1" || got.Sid != 9 || !bytes.Equal(got.D, []byte{1, 2, 3}) {
			t.Fatalf("the Tunnel got %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tun reached the Tunnel")
	}
	if !h.link.SendTun(cloudwire.Tun{Dev: "q5Lw0Zb1", Sid: 9, D: []byte{4, 5}}) {
		t.Fatal("SendTun refused with the uplink up")
	}
	up := svc.waitFor(cloudwire.MsgTun, 3*time.Second)
	var tun cloudwire.Tun
	if err := cloudwire.Unmarshal(up.B, &tun); err != nil || tun.Sid != 9 || !bytes.Equal(tun.D, []byte{4, 5}) {
		t.Fatalf("the service got %+v: %v", tun, err)
	}
	st := h.link.Status()
	if st.TunBytesLast24h == 0 || st.BytesLast24h != budgetBytes+st.BytesLast24h-budgetBytes {
		t.Fatalf("status %+v", st)
	}

	// Past the tunnel's own budget, tun stops.
	h.link.mu.Lock()
	h.link.tunBudget.add(h.link.uptimeMs(), tunBudgetBytes)
	h.link.mu.Unlock()
	if h.link.SendTun(cloudwire.Tun{Dev: "q5Lw0Zb1", Sid: 9, D: []byte{6}}) {
		t.Fatal("SendTun took a message past the tunnel's budget")
	}
	if h.link.Status().TunDropped == 0 {
		t.Fatal("the drop was not counted")
	}
}

// The readable copy's budget does not count tun, so a phone at work never
// stops hb.
func TestTunDoesNotSpendTheCopysBudget(t *testing.T) {
	svc := newFakeService(t)
	h := newHarness(t, svc)
	h.setRoster(func(r *fakeRosterState) { r.tun = true })
	h.nextRoster(t)
	before := h.link.Status().BytesLast24h
	for range 10 {
		if !h.link.SendTun(cloudwire.Tun{Dev: "q5Lw0Zb1", Sid: 1, D: make([]byte, 4096)}) {
			t.Fatal("SendTun refused")
		}
		svc.waitFor(cloudwire.MsgTun, 3*time.Second)
	}
	st := h.link.Status()
	if st.BytesLast24h != before || st.TunBytesLast24h < 40_960 {
		t.Fatalf("copy %d → %d bytes, tunnel %d", before, st.BytesLast24h, st.TunBytesLast24h)
	}
}

// Without an uplink nothing queues: a session the phone cannot reach is one
// it starts again.
func TestSendTunWithoutAnUplinkIsRefused(t *testing.T) {
	svc := newFakeService(t)
	h := newHarness(t, svc)
	if h.link.SendTun(cloudwire.Tun{Dev: "x", Sid: 1, D: []byte{1}}) {
		t.Fatal("SendTun took a message with no uplink")
	}
	if h.link.SendTun(cloudwire.Tun{Dev: "x", Sid: 1, D: make([]byte, cloudwire.TunDataMax+1)}) {
		t.Fatal("SendTun took a d over the limit")
	}
}

// Every session the uplink carried ends with it.
func TestTheTunnelDropsWithTheUplink(t *testing.T) {
	svc := newFakeService(t)
	svc.afterHello = func(conn *websocket.Conn, _ string) {
		time.Sleep(300 * time.Millisecond)
		_ = conn.Close()
	}
	h := newHarness(t, svc)
	h.setRoster(func(r *fakeRosterState) { r.tun = true })
	deadline := time.Now().Add(5 * time.Second)
	for h.tunnel.dropped() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Tunnel was not told the uplink ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// When nothing needs the link any more, the box asks the service to delete
// the home and deletes cloud.key on the receipt. The next link has a new home.
func TestTheLinkEndsWhenNothingNeedsIt(t *testing.T) {
	svc := newFakeService(t)
	svc.purge = true
	h := newHarness(t, svc)
	h.setRoster(func(r *fakeRosterState) { r.tun = true })
	h.nextRoster(t)
	home := h.link.Status().Home

	h.setRoster(func(r *fakeRosterState) { r.gone = true })
	bye := svc.waitFor(cloudwire.MsgBye, 3*time.Second)
	var b cloudwire.Bye
	if cloudwire.Unmarshal(bye.B, &b) != nil || !b.Purge || b.Why != cloudwire.ByeRevoked {
		t.Fatalf("bye %+v", b)
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.link.Status().Link != "off" {
		if time.Now().After(deadline) {
			t.Fatalf("still %+v after the receipt", h.link.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(h.keyPath); !os.IsNotExist(err) {
		t.Fatal("cloud.key survived the end of the link")
	}

	h.setRoster(func(r *fakeRosterState) { r.gone, r.tun = false, true })
	if next := h.link.Status().Home; next == "" || next == home {
		t.Fatalf("the next link's home is %q after %q", next, home)
	}
}

// A grant made before the link existed keeps its key and home id: the link
// takes them over.
func TestAGrantFromBeforeTheLinkKeepsItsHome(t *testing.T) {
	kv := &memKV{}
	keyPath := filepath.Join(t.TempDir(), "cloud.key")
	key, err := createKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	fp, pk, _ := fingerprint(key)
	if err := saveJSON(kv, grantKey, grantRecord{ID: bytes.Repeat([]byte{4}, 16), KeyFp: fp,
		Scopes: []string{protocol.ScopeLiveRead}, Purposes: []string{protocol.CloudPurposeServe},
		GrantKey: bytes.Repeat([]byte{7}, 32), Controller: cloudwire.ControllerSourceful}); err != nil {
		t.Fatal(err)
	}
	svc := newFakeService(t)
	link, err := New(Options{Site: &fakeSite{}, Roster: fakeRoster{}, KV: kv, KeyPath: keyPath,
		ServiceKeys: []*ecdsa.PublicKey{&svc.key.PublicKey}})
	if err != nil {
		t.Fatal(err)
	}
	st := link.Status()
	if st.State != "on" || st.Link != "on" || st.Home != cloudwire.HomeID(pk) {
		t.Fatalf("status %+v", st)
	}
	if rec, _ := loadJSON[linkRecord](kv, linkKey); rec == nil || rec.KeyFp != fp {
		t.Fatalf("link record %+v", rec)
	}
}

// A grant on a box whose link is ending waits for the receipt.
func TestAGrantWaitsForAnEndingLink(t *testing.T) {
	svc := newFakeService(t)
	h := newHarness(t, svc)
	h.setRoster(func(r *fakeRosterState) { r.tun = true })
	h.nextRoster(t)
	h.setRoster(func(r *fakeRosterState) { r.gone = true })
	svc.waitFor(cloudwire.MsgBye, 3*time.Second)
	if err := h.link.Grant(GrantRequest{Scopes: []string{protocol.ScopeLiveRead}, Policy: "p"}); !errors.Is(err, ErrLinkEnding) {
		t.Fatalf("grant while the link ends: %v", err)
	}
}

// A QR that names the home reads it here: empty until the box links, and
// empty again while its link ends.
func TestHomeIsSetOnlyWhileLinked(t *testing.T) {
	svc := newFakeService(t)
	h := newHarness(t, svc)
	if h.link.Home() != "" {
		t.Fatal("a home before the box linked")
	}
	h.setRoster(func(r *fakeRosterState) { r.tun = true })
	if home := h.link.Home(); home == "" || home != h.link.Status().Home {
		t.Fatalf("home %q, status %q", home, h.link.Status().Home)
	}
	h.nextRoster(t)
	h.setRoster(func(r *fakeRosterState) { r.gone = true })
	svc.waitFor(cloudwire.MsgBye, 3*time.Second)
	if h.link.Home() != "" {
		t.Fatal("a home while the link ends")
	}
}
