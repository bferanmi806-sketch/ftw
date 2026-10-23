package cloudlink

import (
	"context"
	"slices"
	"time"

	"github.com/srcfl/ftw/go/protocol"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

// HistorySite is a Site that keeps history. HistoryWindow answers stored
// series as the app protocol's history provider does: every series that
// overlaps [fromMs, toMs), at the stored width stepMs, without missing
// buckets. A Site without it sends no tiles.
type HistorySite interface {
	HistoryWindow(ctx context.Context, names []string, stepMs, fromMs, toMs int64) (map[string][]protocol.HistorySample, error)
}

// tilesRecheckEvery is how often the closed tiles of the window are packed
// again, so a gap the ledger repaired reaches the service. A test shortens
// it.
var tilesRecheckEvery = time.Hour

const (
	// tilesRetryAfter holds tiles back after a read failed or the budget
	// refused one.
	tilesRetryAfter = time.Minute
	// tilesReadTimeout bounds one read of the ledger.
	tilesReadTimeout = 5 * time.Second
	// tilesNamedMax is how many tiles a resync may name.
	tilesNamedMax = cloudwire.TilesClosedMax + 1
)

// tileState is one connection's view of the tiles it has sent.
type tileState struct {
	// have is each closed tile the service holds, by id, as hello_ok named
	// them and as sent since.
	have map[string]string
	// trail is the start of the trailing tile and through the end of the
	// last of its buckets sent on this connection; through == trail means
	// none of it has gone. appends counts appends since it last went whole.
	trail, through int64
	appends        int
	// all asks for every tile of the window, at connect and after a resync
	// that names none; force sends them whatever have says. named are the
	// tiles a resync named.
	all, force bool
	named      []string
	recheckAt  time.Time
	retryAt    time.Time
}

func newTileState(have map[string]string) tileState {
	held := make(map[string]string, len(have))
	for id, etag := range have {
		held[id] = etag
	}
	return tileState{have: held, all: true}
}

// resync takes the service's resync of tiles: the tiles it names, or every
// one of the window when it names none.
func (ts *tileState) resync(named []string) {
	ts.force = true
	if len(named) == 0 || len(named) > tilesNamedMax {
		ts.all, ts.named = true, nil
		return
	}
	ts.named = slices.Clone(named)
}

// tiles sends what the tiles class owes: the trailing tile as its buckets
// close, a tile that closed, and at connect or a resync the tiles of the
// last 48 hours the service lacks (docs/protocol-cloud.md, tiles). It reads
// the ledger only when a bucket closed or a pass is due.
func (w *writer) tiles(snap grantSnapshot, st *cadence, now time.Time) error {
	l := w.link
	hs, ok := l.opts.Site.(HistorySite)
	// Tiles are dated by wall time, so a box without NTP sends none.
	if !ok || !snap.allowed(cloudwire.MsgTiles) || !l.clockSynced() {
		return nil
	}
	ts := &st.tiles
	if now.Before(ts.retryAt) {
		return nil
	}
	res := cloudwire.TileRes
	step, span := protocol.ResolutionStepMs[res], protocol.ResolutionTileSpanMs[res]
	wall := l.opts.Now().UnixMilli()
	closedThrough := (wall - cloudwire.TileSettleMs) / step * step
	open := protocol.TileStartFor(res, closedThrough)
	oldest := wall - cloudwire.TileKeepMs

	pass := ts.all || len(ts.named) > 0 || !now.Before(ts.recheckAt)
	if pass && (ts.all || slices.Contains(ts.named, protocol.TileIDFor(res, 1, open))) {
		// The trailing tile goes whole again.
		ts.trail, ts.through = open, open
	}
	if ts.trail != open {
		// The trailing tile closed since its last piece: it goes closed.
		if prev := ts.trail; prev != 0 && prev < open && prev+span > oldest {
			if sent, err := w.closedTile(snap, hs, ts, prev, closedThrough, false); err != nil || !sent {
				return err
			}
		}
		ts.trail, ts.through, ts.appends = open, open, 0
	}
	if closedThrough > ts.through {
		whole := ts.through == open || ts.appends >= cloudwire.TileAppendsMax
		from := ts.through
		if whole {
			from = open
		}
		c, err := w.pack(hs, ts, open, from, closedThrough)
		if err != nil {
			return nil
		}
		first := int((from - open) / step)
		piece := cloudwire.Piece(c, first, int((closedThrough-open)/step))
		if whole {
			piece = cloudwire.Piece(c, 0, int(protocol.PointsPerTile(res)))
		}
		if sent, err := w.sendTile(snap, ts, &piece); err != nil || !sent {
			return err
		}
		ts.through = closedThrough
		if whole {
			ts.appends = 0
		} else {
			ts.appends++
		}
	}

	if !pass {
		return nil
	}
	// Every closed tile of the window, newest first, that the service does
	// not hold at this etag, or every one named.
	for start := open - span; start+span > oldest; start -= span {
		id := protocol.TileIDFor(res, 1, start)
		if !ts.all && len(ts.named) > 0 && !slices.Contains(ts.named, id) {
			continue
		}
		if sent, err := w.closedTile(snap, hs, ts, start, closedThrough, ts.force); err != nil || !sent {
			return err
		}
	}
	ts.all, ts.force, ts.named = false, false, nil
	ts.recheckAt = now.Add(tilesRecheckEvery)
	return nil
}

// closedTile packs the tile at start whole and sends it unless the service
// holds it at that etag and force is false. sent is false when nothing could
// go and the caller should stop for now; a tile that needs no sending counts
// as sent.
func (w *writer) closedTile(snap grantSnapshot, hs HistorySite, ts *tileState, start, closedThrough int64, force bool) (bool, error) {
	span := protocol.ResolutionTileSpanMs[cloudwire.TileRes]
	c, err := w.pack(hs, ts, start, start, start+span)
	if err != nil {
		return false, nil
	}
	c.Partial = start+span > closedThrough
	if c.Partial || (!force && ts.have[c.TileID] == c.Etag) {
		return true, nil
	}
	body := cloudwire.Tiles{TileID: c.TileID, Etag: c.Etag, Res: c.Res, StartMs: c.StartMs, StepMs: c.StepMs,
		Series: c.Series, Data: c.Data}
	sent, err := w.sendTile(snap, ts, &body)
	if sent {
		ts.have[c.TileID] = c.Etag
	}
	return sent, err
}

// pack reads [from, to) of the tile at start from the ledger and packs the
// tile as the app protocol does, so a closed tile is the same bytes as over
// Noise. Buckets from to on are left out: they have not closed.
func (w *writer) pack(hs HistorySite, ts *tileState, start, from, to int64) (protocol.HistChunk, error) {
	l := w.link
	res := cloudwire.TileRes
	ctx, cancel := context.WithTimeout(context.Background(), tilesReadTimeout)
	defer cancel()
	windows, err := hs.HistoryWindow(ctx, cloudwire.TileSeries, protocol.ResolutionStepMs[res], from, to)
	if err != nil {
		l.log.Debug("FTW Cloud tiles: the ledger did not answer", "err", err)
		ts.retryAt = time.Now().Add(tilesRetryAfter)
		return protocol.HistChunk{}, err
	}
	for name, samples := range windows {
		windows[name] = slices.DeleteFunc(slices.Clone(samples), func(s protocol.HistorySample) bool {
			return s.StartMs < from || s.StartMs >= to
		})
	}
	return protocol.PackTile(res, 1, start, cloudwire.TileSeries, windows, to), nil
}

// sendTile sends one tile under the byte budget. Past it, tiles wait a
// minute and go again from where they were.
func (w *writer) sendTile(snap grantSnapshot, ts *tileState, body *cloudwire.Tiles) (bool, error) {
	sent, err := w.sendBody(snap, cloudwire.MsgTiles, body, false)
	if err == nil && !sent {
		ts.retryAt = time.Now().Add(tilesRetryAfter)
	}
	return sent, err
}
