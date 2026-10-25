package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The app's tiles ask for fixed five-minute buckets laid from the tile's
// start. A row on a boundary belongs to the bucket it starts, and a bucket
// with no row is absent rather than zero.
func TestLoadPowerBucketsLaysFixedBuckets(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Now().Add(-time.Hour).Truncate(5 * time.Minute).UnixMilli()
	const step = int64(300_000)
	for _, r := range []struct {
		at       int64
		pv, grid float64
	}{
		{base + 10_000, -400, 300},
		{base + 200_000, -600, 100},
		{base + step, -2000, -500}, // on the boundary: the second bucket's
		// nothing in the third bucket
		{base + 3*step + 1_000, -100, 700},
	} {
		if err := s.RecordHistory(HistoryPoint{TsMs: r.at, PVW: r.pv, GridW: r.grid}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LoadPowerBuckets(context.Background(), base, base+4*step, step)
	if err != nil {
		t.Fatal(err)
	}
	want := []PowerBucket{
		{StartMS: base, PVW: -500, GridW: 200},
		{StartMS: base + step, PVW: -2000, GridW: -500},
		{StartMS: base + 3*step, PVW: -100, GridW: 700},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d buckets, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].StartMS != want[i].StartMS || got[i].PVW != want[i].PVW || got[i].GridW != want[i].GridW || got[i].Missing != 0 {
			t.Fatalf("bucket %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if _, err := s.LoadPowerBuckets(context.Background(), base, base, step); err == nil {
		t.Fatal("an empty range must be refused")
	}
}
