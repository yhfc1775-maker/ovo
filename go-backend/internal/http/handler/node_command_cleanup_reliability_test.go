package handler

import (
	"go-backend/internal/store/repo"
	"reflect"
	"testing"
)

// Reflection keeps this behavioral proof runnable on the baseline's void
// cleanup methods, which silently discarded every command failure.
func TestReliabilityCleanupCommandFailuresAreReturned(t *testing.T) {
	r, e := repo.Open(t.TempDir() + "/cleanup.db")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	h := New(r, "test")
	id := seedWaveNode(t, r, "cleanup-offline", "198.51.100.10")
	if e := r.DB().Exec(`INSERT INTO tunnel(id,name,type,flow,status,created_time,updated_time) VALUES(701,'cleanup',2,0,1,1,1)`).Error; e != nil {
		t.Fatal(e)
	}
	if e := r.DB().Exec(`INSERT INTO chain_tunnel(tunnel_id,chain_type,node_id,inx) VALUES(701,1,?,1)`, id).Error; e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name string
		fn   interface{}
		args []reflect.Value
	}{
		{"cleanup", h.cleanupTunnelRuntime, []reflect.Value{reflect.ValueOf(int64(701))}},
		{"rollback", h.rollbackTunnelRuntime, []reflect.Value{reflect.ValueOf([]int64{id}), reflect.ValueOf([]int64{id}), reflect.ValueOf(int64(701)), reflect.ValueOf("tls")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := reflect.ValueOf(c.fn).Call(c.args)
			if len(result) != 1 || result[0].IsNil() {
				t.Fatalf("%s silently discarded node command failure: %v", c.name, result)
			}
		})
	}
}
