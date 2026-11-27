package repo

import (
	"fmt"
	"math"
	"testing"

	"go-backend/internal/store/model"
)

func TestDroppedTelemetryCountersSaturate(t *testing.T) {
	for _, surface := range []string{"connlog", "traffic"} {
		for _, mode := range []string{"same-node", "node-sum", "chunk-sum"} {
			t.Run(surface+"/"+mode, func(t *testing.T) {
				r := newConnlogTestRepo(t)
				if err := r.db.AutoMigrate(&model.ConnlogNodeDropped{}, &model.TrafficNodeDropped{}); err != nil {
					t.Fatal(err)
				}
				add, sum := r.AddConnlogNodeDropped, r.SumConnlogDroppedByNodes
				table := "connlog_node_dropped"
				if surface == "traffic" {
					add, sum, table = r.AddTrafficNodeDropped, r.SumTrafficNodeDroppedByNodes, "traffic_node_dropped"
				}
				if err := add(1, math.MaxInt64-10, 1000); err != nil {
					t.Fatal(err)
				}
				ids := []int64{1}
				if mode == "same-node" {
					if err := add(1, 20, 2000); err != nil {
						t.Fatal(err)
					}
					var storageType string
					if err := r.db.Raw(fmt.Sprintf("SELECT typeof(dropped_total) FROM %s WHERE node_id=1", table)).Scan(&storageType).Error; err != nil {
						t.Fatal(err)
					}
					if storageType != "integer" {
						t.Errorf("counter widened from integer to %s", storageType)
					}
				} else {
					if err := add(2, 20, 2000); err != nil {
						t.Fatal(err)
					}
					ids = append(ids, 2)
					if mode == "chunk-sum" {
						ids = make([]int64, inListChunkSize+1)
						for i := range ids {
							ids[i] = int64(100 + i)
						}
						ids[0], ids[len(ids)-1] = 1, 2
					}
				}
				got, err := sum(ids)
				if err != nil || got != math.MaxInt64 {
					t.Errorf("valid counter sum = %d, %v; want saturated %d", got, err, int64(math.MaxInt64))
				}
			})
		}
	}
}
