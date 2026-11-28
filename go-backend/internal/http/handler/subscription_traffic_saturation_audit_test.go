package handler

import (
	"math"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestSubscriptionTrafficSaturatesRangeAndBucketTotals(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	first := now.Add(-time.Hour).UnixMilli()
	hours := []model.SubscriptionTraffic{
		{Hour: first, UploadBytes: math.MaxInt64 - 10, DownloadBytes: math.MaxInt64 - 20},
		{Hour: now.UnixMilli(), UploadBytes: 30, DownloadBytes: 40},
	}
	for _, period := range []string{"hour", "day", "month"} {
		t.Run(period, func(t *testing.T) {
			view := subTraffic(hours, period, now, 0, math.MaxInt64, math.MaxInt64)
			if view.Up != math.MaxInt64 || view.Down != math.MaxInt64 {
				t.Fatalf("range total = %d/%d, want saturated %d/%d", view.Up, view.Down, int64(math.MaxInt64), int64(math.MaxInt64))
			}
			for _, point := range view.Points {
				if point.Up < 0 || point.Down < 0 {
					t.Fatalf("negative point after aggregating valid counters: %+v", point)
				}
			}
		})
	}
}

func TestSubscriptionTrafficResetRemainderSaturatesAfterHours(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	resetHour := now.Add(-3 * time.Hour)
	since := resetHour.Add(30 * time.Minute).UnixMilli()
	for _, tc := range []struct {
		name       string
		after      [][2]int64
		counters   [2]int64
		resetBytes [2]int64
	}{
		{
			name: "two large hours after reset",
			after: [][2]int64{
				{math.MaxInt64 - 10, math.MaxInt64 - 30},
				{math.MaxInt64 - 20, math.MaxInt64 - 40},
			},
			counters:   [2]int64{math.MaxInt64 - 100, math.MaxInt64 - 200},
			resetBytes: [2]int64{0, 0},
		},
		{
			name:       "ordinary exact reset remainder",
			after:      [][2]int64{{10, 20}, {20, 30}},
			counters:   [2]int64{100, 200},
			resetBytes: [2]int64{70, 150},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hours := []model.SubscriptionTraffic{{
				Hour: resetHour.UnixMilli(), UploadBytes: 1000, DownloadBytes: 2000,
			}}
			for i, values := range tc.after {
				hours = append(hours, model.SubscriptionTraffic{
					Hour:        resetHour.Add(time.Duration(i+1) * time.Hour).UnixMilli(),
					UploadBytes: values[0], DownloadBytes: values[1],
				})
			}
			view := subTraffic(hours, "hour", now, since, tc.counters[0], tc.counters[1])
			found := false
			for _, point := range view.Points {
				if point.Time == resetHour.UnixMilli() {
					found = true
					if got := [2]int64{point.Up, point.Down}; got != tc.resetBytes {
						t.Errorf("post-reset hour = %v, want exact remainder %v", got, tc.resetBytes)
					}
				}
				if point.Up < 0 || point.Down < 0 {
					t.Errorf("negative point after reset: %+v", point)
				}
			}
			if !found {
				t.Fatal("reset hour missing from graph window")
			}
			if view.Up < 0 || view.Down < 0 {
				t.Errorf("negative range total after reset: %d/%d", view.Up, view.Down)
			}
		})
	}
}
