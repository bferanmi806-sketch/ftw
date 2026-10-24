// Package energyreport sends one anonymous energy report a day, through an
// independent Oblivious HTTP relay, so that neither the relay nor FTW Cloud
// can tell which home sent it.
//
// The report is six whole-kWh sums of one closed UTC day and its date
// (package energywire). The box seals it to FTW Cloud's gateway key, pinned
// in this release, and posts it to the relay:
//
//   - the relay sees the box's address, the time and a ciphertext of fixed
//     length, never the report;
//   - the gateway sees the report and the relay's address, never the box's.
//     It adds the sums to that day's totals and keeps nothing of the report.
//
// Two things keep the report from being joined to the daily fleet report,
// which goes straight to FTW Cloud from the box's own address: it carries no
// field the fleet report carries, and it is sent at its own time, drawn
// afresh for each day from 1 to 12 hours after the day closes.
//
// It runs only when the owner's answer to the project statistics question
// allows the fleet report, and never for a site a partner controls. It adds
// no question or setting of its own.
package energyreport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/srcfl/ftw/go/protocol/energywire"
	"github.com/srcfl/ftw/go/protocol/ohttp"
)

// Compile-time settings, as in cloudlink: a relay a household could point
// elsewhere is a relay a support call could point at an attacker's. A local
// test build may set them with -ldflags -X.
var (
	// RelayURL is the OHTTP relay the report goes through, run by someone
	// other than Sourceful. Empty until that relay exists: the box then
	// sends nothing.
	RelayURL = ""
	// GatewayKey is FTW Cloud's gateway key configuration (RFC 9458,
	// section 3), base64url, as
	// https://cloud.ftw.energy/.well-known/ohttp-gateway serves it. The box
	// encrypts only to this key and never asks the service for one, so every
	// box on a release uses the same key and a key swapped in at the service
	// reads nothing. Empty means no gateway key exists for this build.
	GatewayKey = ""
)

// Pinned returns this build's relay and gateway key. ok is false unless both
// are set and the key offers the suite package ohttp speaks.
func Pinned() (relay string, key ohttp.KeyConfig, ok bool) {
	return pinned(RelayURL, GatewayKey)
}

func pinned(relay, key string) (string, ohttp.KeyConfig, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || relay == "" || key == "" {
		return "", ohttp.KeyConfig{}, false
	}
	cfg, err := ohttp.ParseKeyConfig(raw)
	if err != nil {
		return "", ohttp.KeyConfig{}, false
	}
	u, err := url.Parse(relay)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == energywire.Authority {
		return "", ohttp.KeyConfig{}, false
	}
	return relay, cfg, true
}

const (
	// The send window after a day closes: late enough that the day's last
	// readings are in the ledger, early enough that the gateway still takes
	// the date.
	firstSend = time.Hour
	lastSend  = 12 * time.Hour

	sendTimeout      = 30 * time.Second
	maxResponseBytes = 1 << 10

	// lastDayKey records the last date this box sent, on the box only, so a
	// restart does not send a day twice.
	lastDayKey = "energy_report.last_date"
)

// KV is where the box remembers the last date it sent.
type KV interface {
	LoadConfig(key string) (string, bool)
	SaveConfig(key, value string) error
}

// Options wires the sender.
type Options struct {
	// Enabled is the owner's answer and the partner rule, read at each send.
	Enabled func() bool
	Ledger  Ledger
	KV      KV

	// Relay and Key default to Pinned. Tests set them to a fake relay.
	Relay string
	Key   *ohttp.KeyConfig

	Client *http.Client
	Logger *slog.Logger

	// Injectable seams. Production leaves them nil.
	Now         func() time.Time
	Random      func() float64
	ClockSynced func() bool
}

// Sender owns the schedule.
type Sender struct {
	enabled func() bool
	ledger  Ledger
	kv      KV
	relay   string
	key     ohttp.KeyConfig
	ready   bool
	client  *http.Client
	log     *slog.Logger
	now     func() time.Time
	random  func() float64
	synced  func() bool
}

func New(opts Options) (*Sender, error) {
	if opts.Ledger == nil {
		return nil, errors.New("energyreport: Ledger is required")
	}
	s := &Sender{enabled: opts.Enabled, ledger: opts.Ledger, kv: opts.KV, client: opts.Client, log: opts.Logger,
		now: opts.Now, random: opts.Random, synced: opts.ClockSynced}
	if opts.Key != nil {
		s.relay, s.key, s.ready = opts.Relay, *opts.Key, opts.Relay != ""
	} else {
		s.relay, s.key, s.ready = Pinned()
	}
	if s.enabled == nil {
		s.enabled = func() bool { return false }
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: sendTimeout}
	} else {
		c := *s.client
		s.client = &c
	}
	// A redirect would resend the sealed report to an address nobody chose.
	s.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "energyreport")
	if s.now == nil {
		s.now = time.Now
	}
	if s.random == nil {
		s.random = randomFloat
	}
	if s.synced == nil {
		s.synced = func() bool { return true }
	}
	return s, nil
}

// Preview is what the box would send for yesterday, for Settings.
type Preview struct {
	// Relay is where it goes, empty when this build has no relay.
	Relay string `json:"relay"`
	// Sending is whether this box sends at all: a relay and a gateway key in
	// this build, and the owner's answer now.
	Sending bool `json:"sending"`
	// Day is the closed UTC day it describes.
	Day string `json:"day"`
	// Payload is the report, or nil when that day is incomplete.
	Payload *energywire.Report `json:"payload"`
}

// Preview builds yesterday's report with the same call the sender uses.
func (s *Sender) Preview(ctx context.Context) Preview {
	day := s.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	p := Preview{Relay: s.relay, Sending: s.ready && s.enabled(), Day: day.Format(time.DateOnly)}
	if r, ok, err := Day(ctx, s.ledger, day); err == nil && ok {
		p.Payload = &r
	}
	return p
}

// Run sends once a day until ctx ends. Without a relay and a gateway key it
// returns at once and sends nothing.
func (s *Sender) Run(ctx context.Context) {
	if !s.ready {
		return
	}
	var p plan
	for {
		now := s.now()
		if !p.live(now) {
			p = s.draw(now)
		}
		if !now.Before(p.at) {
			s.send(ctx, p.day)
			p.done = true
			continue
		}
		// Wake at least hourly, so a wall clock that NTP moves is noticed
		// and the plan drawn again.
		timer := time.NewTimer(min(p.at.Sub(now), time.Hour))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// plan is one day's report: the day it describes and when to send it.
type plan struct {
	day  time.Time
	at   time.Time
	done bool
}

// live reports whether the plan still belongs to the clock's day.
func (p plan) live(now time.Time) bool {
	return !p.done && !p.day.IsZero() && !now.Before(p.day) && now.Before(p.day.Add(24*time.Hour+lastSend))
}

// draw plans the next report. Within a day's send window and before that day
// is sent, the send time is drawn from what is left of the window; otherwise
// from the next day's window. The time is drawn afresh each day, so it does
// not settle into a slot that would name the box.
func (s *Sender) draw(now time.Time) plan {
	now = now.UTC()
	closed := now.Truncate(24 * time.Hour)
	day := closed.AddDate(0, 0, -1)
	end := closed.Add(lastSend)
	if now.Before(end) && !s.sent(day) {
		from := closed.Add(firstSend)
		if now.After(from) {
			from = now
		}
		return plan{day: day, at: from.Add(time.Duration(s.random() * float64(end.Sub(from))))}
	}
	next := closed.AddDate(0, 0, 1)
	return plan{day: closed, at: next.Add(firstSend + time.Duration(s.random()*float64(lastSend-firstSend)))}
}

func (s *Sender) sent(day time.Time) bool {
	if s.kv == nil {
		return false
	}
	last, ok := s.kv.LoadConfig(lastDayKey)
	return ok && last >= day.Format(time.DateOnly)
}

// send makes one attempt for one day and forgets about it. There is no
// retry: a lost report is one fewer in a count that says it counts reports.
func (s *Sender) send(ctx context.Context, day time.Time) {
	if !s.ready || !s.synced() || !s.enabled() || s.sent(day) {
		return
	}
	r, ok, err := Day(ctx, s.ledger, day)
	if err != nil || !ok {
		s.log.Debug("no complete day to report", "day", day.Format(time.DateOnly), "err", err)
		return
	}
	// The answer may have changed while the ledger was read.
	if !s.enabled() {
		return
	}
	// Remembered before it goes, so a crash mid-send loses the day rather
	// than counting it twice.
	if s.kv != nil {
		if err := s.kv.SaveConfig(lastDayKey, r.Date); err != nil {
			return
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := s.post(sendCtx, r); err != nil {
		s.log.Debug("energy report did not go through", "err", err)
	}
}

// post seals one report and reads the gateway's answer.
func (s *Sender) post(ctx context.Context, r energywire.Report) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	inner := ohttp.Request{Method: energywire.Method, Scheme: energywire.Scheme, Authority: energywire.Authority,
		Path: energywire.Path, Content: body}.Marshal(energywire.RequestSize)
	sealed, reader, err := ohttp.Encapsulate(s.key, inner)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.relay, bytes.NewReader(sealed))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ohttp.RequestType)
	// Pinned, as for the fleet report: the relay sees it, and Go's default
	// names the runtime's version.
	req.Header.Set("User-Agent", "ftw-energy-report")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || media != ohttp.ResponseType || len(data) > maxResponseBytes {
		return fmt.Errorf("energy report: relay answered %d %q", resp.StatusCode, media)
	}
	msg, err := reader.Open(data)
	if err != nil {
		return err
	}
	answer, err := ohttp.ParseResponse(msg)
	if err != nil {
		return err
	}
	if answer.Status != http.StatusNoContent {
		return fmt.Errorf("energy report: gateway answered %d", answer.Status)
	}
	return nil
}
