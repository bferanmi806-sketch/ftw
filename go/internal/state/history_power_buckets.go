package state

import (
	"context"
	"errors"
	"strings"
	"time"
)

// PowerBucket is the measured mean power over one fixed bucket, site-signed,
// from the same stored rows /api/history draws. Missing marks the flows that
// some row in the bucket did not measure; their mean is not a value then.
type PowerBucket struct {
	StartMS int64
	GridW   float64
	PVW     float64
	BatW    float64
	Missing HistoryMissing
}

// LoadPowerBuckets returns the mean measured power in each bucketMs bucket of
// [fromMs, toMs), the buckets laid from fromMs. A bucket with no stored row is
// absent. Unlike an energy counter, which may move in 0.1 kWh steps, the
// power rows give each bucket its own shape. Load is not here: it comes from
// the source accounting, which the energy ledger keeps.
func (s *Store) LoadPowerBuckets(ctx context.Context, fromMs, toMs, bucketMs int64) ([]PowerBucket, error) {
	if bucketMs <= 0 || toMs <= fromMs {
		return nil, errors.New("power buckets need a positive width and range")
	}
	if (toMs-fromMs+bucketMs-1)/bucketMs > maxSeriesBuckets {
		return nil, ErrHistoryQueryLimit
	}
	if s.history == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := s.history.QueryContext(ctx, halfOpenHistoryRange(historyTierUnion)+`
		SELECT (ts_ms - ?) / ? AS bucket,
		       SUM(COALESCE(grid_w, 0)*n)/SUM(n), SUM(COALESCE(pv_w, 0)*n)/SUM(n), SUM(COALESCE(bat_w, 0)*n)/SUM(n),
		       `+historyMissingAggregate+`
		FROM deduped GROUP BY bucket ORDER BY bucket`,
		fromMs, toMs, fromMs, toMs, fromMs, toMs, fromMs, toMs, fromMs, toMs, fromMs, bucketMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PowerBucket
	for rows.Next() {
		var b PowerBucket
		var index int64
		if err := rows.Scan(&index, &b.GridW, &b.PVW, &b.BatW, &b.Missing); err != nil {
			return nil, err
		}
		b.StartMS = fromMs + index*bucketMs
		out = append(out, b)
	}
	return out, rows.Err()
}

// halfOpenHistoryRange turns historyTierUnion's closed ranges into [from, to),
// so a row on a bucket boundary belongs to one bucket only.
func halfOpenHistoryRange(union string) string {
	union = strings.ReplaceAll(union, "ts_ms BETWEEN ? AND ?", "ts_ms >= ? AND ts_ms < ?")
	return strings.ReplaceAll(union, "last_ms BETWEEN ? AND ?", "last_ms >= ? AND last_ms < ?")
}
