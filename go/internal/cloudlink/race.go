package cloudlink

import (
	"context"
	"errors"
	"net"
	"net/url"
	"time"

	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

// The race (docs/protocol-cloud.md, "Racing the WebSocket"): QUIC starts
// first; after cloudwire.RaceDelay without a QUIC handshake the WebSocket
// starts too, and the first to open carries the uplink. The link remembers
// the winner for each network, named by the box's local address towards the
// service, and starts the next time with it, for a day: a network that
// blocked UDP once gets QUIC first again after that.

// winnerHolds is how long a network's winner goes first.
const winnerHolds = 24 * time.Hour

type winner struct {
	transport string
	at        time.Time
}

type dialed struct {
	transport string
	conn      uplinkConn
	err       error
}

// dial opens the uplink by whichever transport opens first.
func (l *Link) dial(ctx context.Context) (uplinkConn, error) {
	addr, quicOn := l.quicAddr()
	if !quicOn {
		return l.dialWebSocket(ctx)
	}
	network := localNetwork(addr)
	first, second := cloudwire.TransportQUIC, cloudwire.TransportWebSocket
	if l.preferred(network) == cloudwire.TransportWebSocket {
		first, second = second, first
	}

	ctx, cancel := context.WithCancel(ctx)
	results := make(chan dialed, 2)
	start := func(transport string) {
		go func() {
			var conn uplinkConn
			var err error
			if transport == cloudwire.TransportQUIC {
				var q *quicConn
				if q, err = dialQUIC(ctx, addr, l.tlsConfig(), nil); err == nil {
					conn = q
				}
			} else {
				conn, err = l.dialWebSocket(ctx)
			}
			results <- dialed{transport: transport, conn: conn, err: err}
		}()
	}
	start(first)
	started := 1
	lead := time.NewTimer(cloudwire.RaceDelay)
	defer lead.Stop()
	var errs []error
	for {
		select {
		case <-lead.C:
			if started == 1 {
				start(second)
				started = 2
			}
		case r := <-results:
			if r.err == nil {
				cancel()
				if started == 2 {
					// The loser may still open; close it once it does.
					go func() {
						if late := <-results; late.err == nil {
							_ = late.conn.Close()
						}
					}()
				}
				l.remember(network, r.transport)
				return r.conn, nil
			}
			errs = append(errs, r.err)
			if started == 1 {
				start(second)
				started = 2
				continue
			}
			if len(errs) == 2 {
				cancel()
				return nil, errors.Join(errs...)
			}
		}
	}
}

func (l *Link) dialWebSocket(ctx context.Context) (uplinkConn, error) {
	conn, resp, err := l.dialer().DialContext(ctx, l.opts.Endpoint, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// quicAddr is the service's QUIC address: the WebSocket endpoint's host and
// port, over UDP. False when QUIC is off: a test that names its own
// endpoint and does not ask for QUIC.
func (l *Link) quicAddr() (string, bool) {
	if !l.quicOn {
		return "", false
	}
	if l.opts.QUICAddr != "" {
		return l.opts.QUICAddr, true
	}
	u, err := url.Parse(l.opts.Endpoint)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port), true
}

// localNetwork names the network the box reaches addr through: its local
// address on the route there. Connecting a UDP socket sends nothing.
func localNetwork(addr string) string {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// preferred is the transport that won last on network, QUIC when none has.
func (l *Link) preferred(network string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w, ok := l.winners[network]; ok && time.Since(w.at) < winnerHolds {
		return w.transport
	}
	return cloudwire.TransportQUIC
}

func (l *Link) remember(network, transport string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.winners == nil {
		l.winners = map[string]winner{}
	}
	l.winners[network] = winner{transport: transport, at: time.Now()}
	l.transport = transport
}
