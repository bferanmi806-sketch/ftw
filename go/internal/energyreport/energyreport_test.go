package energyreport

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/protocol/energywire"
	"github.com/srcfl/ftw/go/protocol/ohttp"
)

// The release names no relay and no gateway key yet, so it sends nothing.
// Once both are pinned they must be a usable pair.
func TestTheReleasePinsBothOrNeither(t *testing.T) {
	_, _, ok := Pinned()
	switch {
	case RelayURL == "" && GatewayKey == "":
		if ok {
			t.Fatal("nothing pinned, yet ready")
		}
	case !ok:
		t.Fatalf("the pinned relay %q and gateway key do not make a usable pair", RelayURL)
	}
}

func TestAPinIsRefusedUnlessItIsARelayAndAKey(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString(testGateway(t).Configs()[0].Marshal())
	if _, _, ok := pinned("https://relay.example.net/ftw", key); !ok {
		t.Fatal("a good pair was refused")
	}
	for name, p := range map[string][2]string{
		"no relay":         {"", key},
		"no key":           {"https://relay.example.net/ftw", ""},
		"plain http":       {"http://relay.example.net/ftw", key},
		"FTW Cloud itself": {"https://cloud.ftw.energy/v1/ohttp", key},
		"query":            {"https://relay.example.net/ftw?box=1", key},
		"credentials":      {"https://a:b@relay.example.net/ftw", key},
		"bad key":          {"https://relay.example.net/ftw", "AAAA"},
		"padded base64":    {"https://relay.example.net/ftw", key + "="},
	} {
		if _, _, ok := pinned(p[0], p[1]); ok {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestTheSendTimeIsDrawnInTheWindowAfterTheDayCloses(t *testing.T) {
	closed := day.AddDate(0, 0, 1)
	for name, c := range map[string]struct {
		now      time.Time
		random   float64
		sent     string
		wantDay  time.Time
		from, to time.Time
	}{
		"boot before the window":     {closed.Add(20 * time.Minute), 0, "", day, closed.Add(time.Hour), closed.Add(time.Hour)},
		"latest in the window":       {closed.Add(20 * time.Minute), 1, "", day, closed.Add(12 * time.Hour), closed.Add(12 * time.Hour)},
		"boot inside the window":     {closed.Add(6 * time.Hour), 0.5, "", day, closed.Add(9 * time.Hour), closed.Add(9 * time.Hour)},
		"boot after the window":      {closed.Add(13 * time.Hour), 0, "", closed, closed.Add(25 * time.Hour), closed.Add(25 * time.Hour)},
		"that day was sent already":  {closed.Add(6 * time.Hour), 0, "2026-10-23", closed, closed.Add(25 * time.Hour), closed.Add(25 * time.Hour)},
		"a later day was sent first": {closed.Add(6 * time.Hour), 0, "2026-10-24", closed, closed.Add(25 * time.Hour), closed.Add(25 * time.Hour)},
	} {
		kv := memKV{}
		if c.sent != "" {
			kv[lastDayKey] = c.sent
		}
		s := newSender(t, Options{KV: kv, Random: func() float64 { return c.random }})
		p := s.draw(c.now)
		if !p.day.Equal(c.wantDay) || p.at.Before(c.from) || p.at.After(c.to) || !p.live(c.now) {
			t.Fatalf("%s: plan %+v", name, p)
		}
	}
	// A clock that jumps a year makes the plan stale.
	p := plan{day: day, at: day.Add(30 * time.Hour)}
	if p.live(day.AddDate(1, 0, 0)) || p.live(day.AddDate(-1, 0, 0)) || !p.live(day.Add(30*time.Hour)) {
		t.Fatal("a stale plan stayed live")
	}
}

// Box to relay to gateway: the relay sees a fixed-length ciphertext and no
// report, the gateway reads the report and nothing that names the box, and
// the box hears 204.
func TestABoxReportReachesTheGatewayThroughTheRelay(t *testing.T) {
	g := testGateway(t)
	gw := newFakeGateway(t, g)
	relay := newFakeRelay(t, gw.URL)
	kv := memKV{}
	cfg := g.Configs()[0]
	s := newSender(t, Options{Ledger: whole(t), KV: kv, Relay: relay.server.URL, Key: &cfg, Enabled: func() bool { return true },
		Now: func() time.Time { return day.Add(30 * time.Hour) }})

	s.send(context.Background(), day)
	if len(gw.reports) != 1 {
		t.Fatalf("gateway got %d reports", len(gw.reports))
	}
	got := gw.reports[0]
	if got.Date != "2026-10-23" || got.GridImport != 24 || got.VehicleCharge != 24 {
		t.Fatalf("report %+v", got)
	}
	if kv[lastDayKey] != "2026-10-23" || gw.answered != http.StatusNoContent {
		t.Fatalf("kv %v answered %d", kv, gw.answered)
	}
	body := relay.bodies[0]
	if bytes.Contains(body, []byte("kwh")) || bytes.Contains(body, []byte("2026-10-23")) || len(body) != 7+32+energywire.RequestSize+16 {
		t.Fatalf("the relay saw %d bytes: %q", len(body), body)
	}
	if relay.headers[0].Get("Content-Type") != ohttp.RequestType || relay.headers[0].Get("User-Agent") != "ftw-energy-report" {
		t.Fatalf("relay headers %v", relay.headers[0])
	}
	// A restart in the same window does not send the day again.
	s.send(context.Background(), day)
	if len(gw.reports) != 1 {
		t.Fatal("the same day went twice")
	}
}

func TestNothingIsSentWithoutTheOwnersAnswer(t *testing.T) {
	g := testGateway(t)
	gw := newFakeGateway(t, g)
	relay := newFakeRelay(t, gw.URL)
	cfg := g.Configs()[0]
	ledger := whole(t)
	var mu sync.Mutex
	reads := 0
	for name, o := range map[string]Options{
		"no answer":        {Enabled: nil},
		"the answer is no": {Enabled: func() bool { return false }},
		"clock not set":    {Enabled: func() bool { return true }, ClockSynced: func() bool { return false }},
		// Withdrawn while the ledger was being read.
		"withdrawn mid-read": {Enabled: func() bool {
			mu.Lock()
			defer mu.Unlock()
			reads++
			return reads == 1
		}},
		"incomplete day": {Enabled: func() bool { return true }, Ledger: wholeDay(t, func(id string, at time.Time) bool { return id == "inverter:1/pv" && at.After(day.Add(time.Hour)) })},
	} {
		if o.Ledger == nil {
			o.Ledger = ledger
		}
		o.Relay, o.Key, o.KV = relay.server.URL, &cfg, memKV{}
		newSender(t, o).send(context.Background(), day)
		if len(relay.bodies) != 0 {
			t.Fatalf("%s: sent", name)
		}
	}
}

// Without a relay and a gateway key, Run returns and never reads the ledger.
func TestWithoutARelayRunSendsNothing(t *testing.T) {
	l := &countingLedger{}
	s := newSender(t, Options{Ledger: l, Enabled: func() bool { return true }})
	done := make(chan struct{})
	go func() { s.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run waited with nowhere to send")
	}
	if l.n != 0 {
		t.Fatal("read the ledger with nowhere to send")
	}
	if p := s.Preview(context.Background()); p.Relay != "" || p.Day == "" {
		t.Fatalf("preview %+v", p)
	}
}

// The box believes only a sealed 204 from the gateway it encrypted to.
func TestTheBoxReadsOnlyTheGatewaysSealedAnswer(t *testing.T) {
	g := testGateway(t)
	cfg := g.Configs()[0]
	ledger := whole(t)
	r, _, _ := Day(context.Background(), ledger, day)
	for name, h := range map[string]http.HandlerFunc{
		"relay error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"plain 204":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		"forged body": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ohttp.ResponseType)
			_, _ = w.Write(make([]byte, 64))
		},
		"sealed 400": sealedAnswer(t, g, http.StatusBadRequest),
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.example/", http.StatusTemporaryRedirect)
		},
	} {
		srv := httptest.NewServer(h)
		s := newSender(t, Options{Ledger: ledger, Relay: srv.URL, Key: &cfg})
		if err := s.post(context.Background(), r); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		srv.Close()
	}
	srv := httptest.NewServer(sealedAnswer(t, g, http.StatusNoContent))
	defer srv.Close()
	s := newSender(t, Options{Ledger: ledger, Relay: srv.URL, Key: &cfg})
	if err := s.post(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func newSender(t *testing.T, o Options) *Sender {
	t.Helper()
	if o.Ledger == nil {
		o.Ledger = &countingLedger{}
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testGateway(t *testing.T) *ohttp.Gateway {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := ohttp.NewGateway(ohttp.Key{ID: 7, Private: k})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func sealedAnswer(t *testing.T, g *ohttp.Gateway, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, ctx, err := g.Decapsulate(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sealed, _ := ctx.Seal(ohttp.Response{Status: status}.Marshal(energywire.ResponseSize))
		w.Header().Set("Content-Type", ohttp.ResponseType)
		_, _ = w.Write(sealed)
	}
}

// fakeGateway stands in for FTW Cloud's: it reads only the one inner request.
type fakeGateway struct {
	*httptest.Server
	g        *ohttp.Gateway
	reports  []energywire.Report
	answered int
}

func newFakeGateway(t *testing.T, g *ohttp.Gateway) *fakeGateway {
	f := &fakeGateway{g: g}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		msg, ctx, err := g.Decapsulate(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.answered = http.StatusBadRequest
		req, err := ohttp.ParseRequest(msg)
		if err == nil && req.Method == energywire.Method && req.Path == energywire.Path && len(msg) == energywire.RequestSize {
			if rep, err := energywire.Decode(req.Content, day.Add(30*time.Hour)); err == nil {
				f.reports = append(f.reports, rep)
				f.answered = http.StatusNoContent
			}
		}
		sealed, _ := ctx.Seal(ohttp.Response{Status: f.answered}.Marshal(energywire.ResponseSize))
		w.Header().Set("Content-Type", ohttp.ResponseType)
		_, _ = w.Write(sealed)
	}))
	t.Cleanup(f.Close)
	return f
}

// fakeRelay forwards the body and content type, and nothing about the box.
type fakeRelay struct {
	server  *httptest.Server
	bodies  [][]byte
	headers []http.Header
}

func newFakeRelay(t *testing.T, gateway string) *fakeRelay {
	f := &fakeRelay{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())
		resp, err := http.Post(gateway, r.Header.Get("Content-Type"), bytes.NewReader(body))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

type memKV map[string]string

func (m memKV) LoadConfig(k string) (string, bool) { v, ok := m[k]; return v, ok }
func (m memKV) SaveConfig(k, v string) error       { m[k] = v; return nil }

type countingLedger struct{ n int }

func (c *countingLedger) EnergyAssets() ([]state.EnergyAsset, error) { c.n++; return nil, nil }
func (c *countingLedger) LoadEnergyHistoryContext(context.Context, state.EnergyHistoryQuery) ([]state.EnergyLedgerPoint, bool, error) {
	c.n++
	return nil, false, nil
}
