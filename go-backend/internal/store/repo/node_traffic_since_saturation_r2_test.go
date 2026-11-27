package repo

import (
	"math"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestNodeTrafficSinceSaturatesAllAggregationPaths(t *testing.T) {
	const hourMs = int64(time.Hour / time.Millisecond)
	bucket := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC).UnixMilli()
	for _, tc := range []struct {
		name string
		from int64
		rows []model.NodeTraffic
	}{
		{
			name: "boundary share plus full hour",
			from: bucket + hourMs/2,
			rows: []model.NodeTraffic{
				{NodeID: 1, Hour: bucket, InBytes: math.MaxInt64 - 10, OutBytes: math.MaxInt64 - 20},
				{NodeID: 1, Hour: bucket + hourMs, InBytes: math.MaxInt64 - 30, OutBytes: math.MaxInt64 - 40},
			},
		},
		{
			name: "aligned full hours",
			from: bucket,
			rows: []model.NodeTraffic{
				{NodeID: 1, Hour: bucket, InBytes: math.MaxInt64 - 10, OutBytes: math.MaxInt64 - 20},
				{NodeID: 1, Hour: bucket + hourMs, InBytes: 30, OutBytes: 40},
			},
		},
		{
			name: "nonaligned full hours",
			from: bucket + hourMs/2,
			rows: []model.NodeTraffic{
				{NodeID: 1, Hour: bucket + hourMs, InBytes: math.MaxInt64 - 10, OutBytes: math.MaxInt64 - 20},
				{NodeID: 1, Hour: bucket + 2*hourMs, InBytes: 30, OutBytes: 40},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			// The independent node checks that saturation is per node and
			// that reading all rows releases SQLite's sole connection before
			// the boundary-hour query.
			tc.rows = append(tc.rows, model.NodeTraffic{NodeID: 2, Hour: bucket + hourMs, InBytes: 7, OutBytes: 11})
			if err := r.AddNodeTraffic(tc.rows); err != nil {
				t.Fatal(err)
			}
			got, err := r.NodeTrafficSince(tc.from)
			if err != nil {
				t.Fatalf("valid hourly counters failed aggregation: %v", err)
			}
			if want := [2]int64{math.MaxInt64, math.MaxInt64}; got[1] != want {
				t.Errorf("large node counters = %v, want saturated %v", got[1], want)
			}
			if want := [2]int64{7, 11}; got[2] != want {
				t.Errorf("independent node counters = %v, want %v", got[2], want)
			}
		})
	}
}
