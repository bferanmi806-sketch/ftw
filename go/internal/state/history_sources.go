package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
)

// HistorySources carries independently fresh site-side powers, never the live
// Load result. Topology identifies the electrical flows included in the sum.
type HistorySources struct {
	AtMS                            int64
	Topology                        string
	Missing                         HistoryMissing
	GridW, PVW, BatteryW, EVW, V2XW float64
}

const sourcePeriodsSchema = `CREATE TABLE IF NOT EXISTS history_source_periods(start_ms INTEGER PRIMARY KEY,last_ms INTEGER NOT NULL) WITHOUT ROWID`

const sourceCursorSchema = `CREATE TABLE IF NOT EXISTS history_source_cursor(key TEXT PRIMARY KEY,value TEXT NOT NULL) WITHOUT ROWID`

// This uses the site energy columns so tariff and day readers share exactly
// the same evidence. load_wh stays zero: Load is derived after interval sums.
// ev_wh is the signed EV+V2X energy, including discharge.
const sourceEnergyTable = "history_source_energy"
const maxSourceEnergyWidth int64 = 900000

// maxSourceBridgeMS bounds an estimated bridge over a writer pause, a process
// restart or a briefly unknown source. It shares the history estimate budget
// of a recovered balance episode; longer gaps stay unknown.
const maxSourceBridgeMS = maxLoadEstimateEpisodeMS

// sourceReceipt is one snapshot held in the writer cursor. Stopped marks the
// first receipt after a process stop.
type sourceReceipt struct {
	HistorySources
	Stopped bool `json:",omitempty"`
}

type sourceCursor struct {
	At      int64
	Start   int64
	Sources HistorySources
	// Episode starts at the last fully known receipt. Its intervals wait for
	// known sources of that topology: if they return within the bridge
	// budget the intervals are an estimate, otherwise they are recorded as
	// before. Pending time never enters totals or the period's coverage.
	Episode []sourceReceipt `json:",omitempty"`
}

func (s *Store) recordSourceEnergyTx(ctx context.Context, tx *sql.Tx, p *HistoryPoint) error {
	var last sourceCursor
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM history_source_cursor WHERE key='last'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &last); err != nil {
			return err
		}
	}
	// commit makes source energy final through end. Readers count the
	// period's time as expected, and a closed day waits for it.
	commit := func(end int64) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_source_periods VALUES(?,?) ON CONFLICT(start_ms) DO UPDATE SET last_ms=excluded.last_ms`, last.Start, end); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO history_source_cursor VALUES('through',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, end)
		return err
	}
	// An abandoned episode keeps the original evidence and its unknowns,
	// through its last receipt.
	abandon := func() error {
		n := len(last.Episode)
		if n == 0 {
			return nil
		}
		for i := 1; i < n; i++ {
			if err := recordedSourceIntervalTx(ctx, tx, last.Episode[i-1].HistorySources, last.Episode[i]); err != nil {
				return err
			}
		}
		through := last.Episode[n-1].AtMS
		last.Episode = nil
		return commit(through)
	}
	drop := func() error {
		if err := abandon(); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM history_source_cursor WHERE key IN ('last','stopped')`)
		return err
	}
	if p == nil || p.Sources == nil {
		return drop()
	}
	at := p.Sources.AtMS
	if at <= 0 {
		return errors.New("history source snapshot has no observation time")
	}
	var through int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM history_source_cursor WHERE key='through'),0)`).Scan(&through); err != nil {
		return err
	}
	if latest := max(through, last.At); at <= latest {
		if at == latest {
			return nil
		}
		return drop()
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO history_source_cursor VALUES('from',?)`, at); err != nil {
		return err
	}
	if p.Sources.Missing&MissingPower == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO energy_assets(asset_id,device_id,kind,label,read_only,first_seen_ms,last_seen_ms) VALUES(?,'',?,'Observed load',1,?,?)
 ON CONFLICT(asset_id) DO UPDATE SET last_seen_ms=MAX(last_seen_ms,excluded.last_seen_ms)`, ObservedConsumerAssetID, AssetObservedConsumer, at, at); err != nil {
			return err
		}
	}
	cur := sourceReceipt{HistorySources: *p.Sources}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM history_source_cursor WHERE key='stopped')`).Scan(&cur.Stopped); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_source_cursor WHERE key='stopped'`); err != nil {
		return err
	}
	known := func(v HistorySources) bool { return v.Missing&MissingPower == 0 }
	switch {
	case last.At == 0:
		last.Start = at
	case at-last.At > maxSourceBridgeMS:
		if err := abandon(); err != nil {
			return err
		}
		// A long stop starts a new period, as an older writer may have run.
		if cur.Stopped {
			last.Start = at
		}
	case len(last.Episode) > 0:
		last.Episode = append(last.Episode, cur)
	case !cur.Stopped && at-last.At <= maxLoadEstimateTickMS && last.Sources.Topology == cur.Topology && known(last.Sources) && known(cur.HistorySources):
		// Hold the previous receipt forward, bounded by the next safe tick. Do
		// not assign a newly arrived charger value to time before its receipt.
		if err := writeSourceIntervalTx(ctx, tx, last.At, at, last.Sources, 0); err != nil {
			return err
		}
	case known(last.Sources):
		last.Episode = []sourceReceipt{{HistorySources: last.Sources}, cur}
	default:
		if err := recordedSourceIntervalTx(ctx, tx, last.Sources, cur); err != nil {
			return err
		}
	}
	if len(last.Episode) > 0 {
		anchor := last.Episode[0].HistorySources
		switch {
		case known(cur.HistorySources) && cur.Topology == anchor.Topology && at-anchor.AtMS <= maxSourceBridgeMS:
			if err := bridgeSourceEpisodeTx(ctx, tx, last.Episode); err != nil {
				return err
			}
			last.Episode = nil
		case at-anchor.AtMS > maxSourceBridgeMS:
			if err := abandon(); err != nil {
				return err
			}
		}
	}
	end := at
	if len(last.Episode) > 0 {
		end = last.Episode[0].AtMS
	}
	if err := commit(end); err != nil {
		return err
	}
	last.At, last.Sources = at, cur.HistorySources
	b, err := json.Marshal(last)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO history_source_cursor VALUES('last',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(b))
	return err
}

// recordedSourceIntervalTx keeps the original evidence: the previous receipt
// held to the next safe tick of the same topology. Either endpoint's unknown
// source leaves the interval, and so its Load, unknown.
func recordedSourceIntervalTx(ctx context.Context, tx *sql.Tx, prev HistorySources, cur sourceReceipt) error {
	if cur.Stopped || cur.AtMS-prev.AtMS > maxLoadEstimateTickMS || prev.Topology != cur.Topology {
		return nil
	}
	missing := (prev.Missing | cur.Missing) & MissingPower
	if missing != 0 {
		missing |= MissingLoad
	}
	return writeSourceIntervalTx(ctx, tx, prev.AtMS, cur.AtMS, prev, missing)
}

// bridgeSourceEpisodeTx estimates the episode between two fully known
// receipts of one topology. A measured source keeps its own receipts; an
// unknown one, or a receipt of another topology, holds the first receipt's
// values. The marker keeps the estimate distinguishable from measurement.
func bridgeSourceEpisodeTx(ctx context.Context, tx *sql.Tx, episode []sourceReceipt) error {
	anchor := episode[0].HistorySources
	for i := 1; i < len(episode); i++ {
		v := episode[i-1].HistorySources
		if v.Topology != anchor.Topology || v.Missing&MissingLoad != 0 {
			v = anchor
		}
		for flag, w := range map[HistoryMissing][2]*float64{MissingGrid: {&v.GridW, &anchor.GridW}, MissingPV: {&v.PVW, &anchor.PVW}, MissingBattery: {&v.BatteryW, &anchor.BatteryW}, MissingEV: {&v.EVW, &anchor.EVW}, MissingV2X: {&v.V2XW, &anchor.V2XW}} {
			if v.Missing&flag != 0 {
				*w[0] = *w[1]
			}
		}
		if err := writeSourceIntervalTx(ctx, tx, episode[i-1].AtMS, episode[i].AtMS, v, EstimatedLoad); err != nil {
			return err
		}
	}
	return nil
}

func writeSourceIntervalTx(ctx context.Context, tx *sql.Tx, from, until int64, v HistorySources, missing HistoryMissing) error {
	// A delayed writer may finish a just-closed local day. Invalidate only
	// the nearby cached dates; source rows and old history stay intact.
	low := time.UnixMilli(from).UTC().AddDate(0, 0, -1).Format("2006-01-02")
	high := time.UnixMilli(until).UTC().AddDate(0, 0, 1).Format("2006-01-02")
	for _, table := range []string{"energy_daily", "energy_daily_partial", "history_daily_quality"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE day>=? AND day<=?`, low, high); err != nil {
			return err
		}
	}
	for from < until {
		start := bucketStart(from, ArchiveResolutionMS)
		to := min(until, start+ArchiveResolutionMS)
		ms := to - from
		h := float64(ms) / 3600000
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_source_energy VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(start_ms,resolution_ms) DO UPDATE SET first_ms=MIN(first_ms,excluded.first_ms),last_ms=MAX(last_ms,excluded.last_ms),covered_ms=covered_ms+excluded.covered_ms,n=n+excluded.n,
 import_wh=import_wh+excluded.import_wh,export_wh=export_wh+excluded.export_wh,pv_wh=pv_wh+excluded.pv_wh,bat_charge_wh=bat_charge_wh+excluded.bat_charge_wh,bat_discharge_wh=bat_discharge_wh+excluded.bat_discharge_wh,ev_wh=ev_wh+excluded.ev_wh,ev_charge_wh=ev_charge_wh+excluded.ev_charge_wh,ev_discharge_wh=ev_discharge_wh+excluded.ev_discharge_wh`,
			start, ArchiveResolutionMS, from, to, ms, 1,
			math.Max(0, v.GridW)*h, math.Max(0, -v.GridW)*h, -v.PVW*h,
			math.Max(0, v.BatteryW)*h, math.Max(0, -v.BatteryW)*h, 0, (v.EVW+v.V2XW)*h, (math.Max(0, v.EVW)+math.Max(0, v.V2XW))*h, (math.Max(0, -v.EVW)+math.Max(0, -v.V2XW))*h); err != nil {
			return err
		}
		if err := putHistoryQuality(ctx, tx, "source_energy", start, ArchiveResolutionMS, missing); err != nil {
			return err
		}
		from = to
	}
	return nil
}

func (s *Store) sourceEnergyFrom(ctx context.Context) (int64, bool, error) {
	var from int64
	err := s.history.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM history_source_cursor WHERE key='from'`).Scan(&from)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return from, err == nil, err
}

// HistorySourceProgress is the last committed receipt time. Once it reaches
// a day boundary, later source integration cannot change that closed day.
func (s *Store) HistorySourceProgress(ctx context.Context) (int64, bool, error) {
	var through int64
	err := s.history.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM history_source_cursor WHERE key='through'`).Scan(&through)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return through, err == nil, err
}

func (s *Store) sourcePeriodCoverage(ctx context.Context, from, to int64) (int64, error) {
	var ms int64
	err := s.history.QueryRowContext(ctx, `SELECT COALESCE(SUM(MIN(last_ms,?)-MAX(start_ms,?)),0) FROM history_source_periods WHERE last_ms>? AND start_ms<?`, to, from, from, to).Scan(&ms)
	return ms, err
}
func (s *Store) sourcePeriods(ctx context.Context, from, to int64) ([][2]int64, error) {
	rows, err := s.history.QueryContext(ctx, `SELECT start_ms,last_ms FROM history_source_periods WHERE start_ms<=? AND last_ms>=? ORDER BY start_ms`, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]int64
	for rows.Next() {
		var v [2]int64
		if err := rows.Scan(&v[0], &v[1]); err != nil {
			return nil, err
		}
		out = append(out, v)
		if len(out) > maxRawSeriesPoints {
			return nil, ErrHistoryQueryLimit
		}
	}
	return out, rows.Err()
}

type sourceLoadBucket struct {
	start, width, first, last, covered, n int64
	wh                                    float64
	missing                               HistoryMissing
}

// Whole source minutes only: never prorate a stored bucket across a gap or a
// price boundary. Signed minutes survive until this requested interval sums.
func (s *Store) sourceLoadBuckets(ctx context.Context, from, to, width int64) ([]sourceLoadBucket, error) {
	return s.sourceLoadBucketsLimited(ctx, from, to, width, 0)
}

// A positive limit counts complete, valid buckets, not empty time slots.
// Ordered streaming bounds memory even when an older prefix has no data.
func (s *Store) sourceLoadBucketsLimited(ctx context.Context, from, to, width int64, limit int) ([]sourceLoadBucket, error) {
	rows, err := s.history.QueryContext(ctx, `SELECT e.start_ms,e.resolution_ms,e.first_ms,e.last_ms,e.covered_ms,e.n,
 e.import_wh-e.export_wh+e.pv_wh-e.bat_charge_wh+e.bat_discharge_wh-e.ev_wh,COALESCE(q.missing,0)
 FROM history_source_energy e LEFT JOIN history_quality q ON q.kind='source_energy' AND q.start_ms=e.start_ms AND q.resolution_ms=e.resolution_ms
 WHERE e.start_ms>=? AND e.start_ms<? AND e.start_ms+e.resolution_ms>? ORDER BY e.start_ms`, from-maxSourceEnergyWidth, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sourceLoadBucket
	var b *sourceLoadBucket
	finish := func() bool {
		if b == nil {
			return false
		}
		// A partial tail stays live-only; never pad missing time with zero.
		if b.covered != b.width || b.first != b.start || b.last != b.start+b.width || b.wh < 0 || !finiteHistoryValue(b.wh) {
			b.missing |= MissingLoad
		}
		if limit == 0 || b.covered > 0 && b.missing&MissingPower == 0 {
			out = append(out, *b)
		}
		return limit > 0 && len(out) >= limit
	}
	for rows.Next() {
		var r sourceLoadBucket
		if err := rows.Scan(&r.start, &r.width, &r.first, &r.last, &r.covered, &r.n, &r.wh, &r.missing); err != nil {
			return nil, err
		}
		start := from + (r.start-from)/width*width
		if r.start < from {
			start = from
		}
		w := max(width, r.width)
		if b == nil || b.start != start {
			if finish() {
				return out, nil
			}
			b = &sourceLoadBucket{start: start, width: w, first: r.first, last: r.last}
		}
		if r.first < from || r.last > to || r.first < start || r.last > start+w {
			b.missing |= MissingLoad
			continue
		}
		b.first = min(b.first, r.first)
		b.last = max(b.last, r.last)
		b.covered += r.covered
		b.n += r.n
		b.wh += r.wh
		b.missing |= r.missing
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	finish()
	return out, nil
}

func finiteHistoryValue(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func (s *Store) applySourceLoadHistory(ctx context.Context, points []HistoryPoint, from, to, width int64) error {
	cut, ok, err := s.sourceEnergyFrom(ctx)
	if err != nil || !ok || to <= cut {
		return err
	}
	buckets, err := s.sourceLoadBuckets(ctx, from, to, width)
	if err != nil {
		return err
	}
	periods, err := s.sourcePeriods(ctx, from, to)
	if err != nil {
		return err
	}
	byStart := map[int64]sourceLoadBucket{}
	for _, b := range buckets {
		byStart[b.start] = b
	}
	for i := range points {
		p := &points[i]
		if p.TsMs < cut {
			continue
		}
		inPeriod := false
		for _, v := range periods {
			if v[0] <= p.TsMs && p.TsMs <= v[1] {
				inPeriod = true
				break
			}
		}
		if !inPeriod {
			continue
		}
		start := from + (p.TsMs-from)/width*width
		p.Missing &^= MissingLoad | BalanceMismatch | EstimatedLoad
		p.LoadW = 0
		b, exists := byStart[start]
		if !exists || b.covered == 0 || b.missing&MissingPower != 0 {
			p.Missing |= MissingLoad
		} else {
			p.LoadW = b.wh * 3600000 / float64(b.width)
			p.Missing |= EstimatedLoad
		}
		// JSON must not retain the old instantaneous Load alongside the column.
		var detail map[string]any
		if json.Unmarshal([]byte(p.JSON), &detail) == nil && detail != nil {
			detail["load_house_w"] = p.Missing.Value(MissingLoad, p.LoadW)
			detail["history_load_method"] = "source_interval_balance_v1"
			if raw, err := json.Marshal(detail); err == nil {
				p.JSON = string(raw)
			}
		}
	}
	return nil
}

func (s *Store) applySourceLoadLedger(ctx context.Context, q EnergyHistoryQuery, points []EnergyLedgerPoint) ([]EnergyLedgerPoint, bool, error) {
	if q.AssetID != "" && q.AssetID != ObservedConsumerAssetID {
		return points, false, nil
	}
	cut, ok, err := s.sourceEnergyFrom(ctx)
	if err != nil || !ok || q.UntilMS <= cut {
		return points, false, err
	}
	buckets, err := s.sourceLoadBucketsLimited(ctx, q.SinceMS, q.UntilMS, q.BucketMS, q.Limit+1)
	if err != nil {
		return nil, false, err
	}
	periods, err := s.sourcePeriods(ctx, q.SinceMS, q.UntilMS)
	if err != nil {
		return nil, false, err
	}
	out := points[:0]
	for _, p := range points {
		overlaps := false
		if p.Flow == FlowConsumerUse {
			for _, v := range periods {
				if v[0] < p.BucketStartMS+p.BucketLenMS && v[1] > p.BucketStartMS {
					overlaps = true
					break
				}
			}
		}
		if overlaps {
			continue
		}
		out = append(out, p)
	}
	for _, b := range buckets {
		asset := "system"
		if q.AssetID != "" {
			asset = q.AssetID
		}
		out = append(out, EnergyLedgerPoint{SchemaVersion: EnergyLedgerSchemaVersion, AssetID: asset, Flow: FlowConsumerUse, BucketStartMS: b.start, BucketLenMS: b.width, EnergyWh: b.wh, Source: "power_telemetry", Quality: "integrated", Provenance: "source_interval_balance_v1", SampleCount: b.n})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].BucketStartMS < out[j].BucketStartMS })
	rank := 0
	for i, p := range out {
		if i == 0 || p.BucketStartMS != out[i-1].BucketStartMS {
			rank++
		}
		if rank > q.Limit {
			return out[:i], true, nil
		}
	}
	return out, false, nil
}
