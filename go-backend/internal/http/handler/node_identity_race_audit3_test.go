package handler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"gorm.io/gorm"
)

func TestConcurrentNodeEditsCannotCreateDuplicateIdentity(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h := New(r, "test-secret")
	first := kr1SeedNode(t, r, "identity-first", "198.51.100.11", 0, "agent")
	second := kr1SeedNode(t, r, "identity-second", "198.51.100.12", 0, "agent")
	checked, release := make(chan struct{}, 2), make(chan struct{})
	var count atomic.Int32
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	const callback = "audit_hold_node_identity_check"
	if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*int64); tx.Statement.Table == "node" && ok && count.Add(1) <= 2 {
			checked <- struct{}{}
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Query().Remove(callback)
	results := make(chan kr1Envelope, 2)
	for _, id := range []int64{first, second} {
		go func(id int64) {
			results <- kr1CallNodeUpdate(t, h, map[string]interface{}{
				"id": id, "name": "same-identity", "serverIp": "198.51.100.99",
			})
		}(id)
	}
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("first duplicate check did not run")
	}
	// Buggy edits can both pass the check before either writes. Correctly
	// serialized edits queue the second request until the first write finishes.
	select {
	case <-checked:
	case <-time.After(time.Second):
	}
	unblock()
	successes := 0
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.Code == 0 {
				successes++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("node edit did not finish")
		}
	}
	var duplicates int64
	if err := r.DB().Model(&model.Node{}).Where("name = ? AND server_ip = ?", "same-identity", "198.51.100.99").Count(&duplicates).Error; err != nil {
		t.Fatal(err)
	}
	if successes != 1 || duplicates != 1 {
		t.Errorf("concurrent edits bypassed identity uniqueness: successful=%d matching_nodes=%d", successes, duplicates)
	}
}

func TestNodePartialEditCannotRestoreReassignedIdentity(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h := New(r, "test-secret")
	first := kr1SeedNode(t, r, "original-identity", "198.51.100.21", 0, "agent")
	second := kr1SeedNode(t, r, "other-identity", "198.51.100.22", 0, "agent")
	snapshot, release := make(chan struct{}), make(chan struct{})
	var held atomic.Bool
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	const callback = "audit_hold_partial_node_snapshot"
	if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if node, ok := tx.Statement.Dest.(*model.Node); ok && node.ID == first && node.Name == "original-identity" && held.CompareAndSwap(false, true) {
			close(snapshot)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Query().Remove(callback)
	partial := make(chan kr1Envelope, 1)
	go func() {
		partial <- kr1CallNodeUpdate(t, h, map[string]interface{}{"id": first, "remark": "late partial edit"})
	}()
	select {
	case <-snapshot:
	case <-time.After(5 * time.Second):
		t.Fatal("partial edit did not read a snapshot")
	}
	claimed := make(chan []kr1Envelope, 1)
	go func() {
		rename := kr1CallNodeUpdate(t, h, map[string]interface{}{"id": first, "name": "renamed-identity"})
		claim := kr1CallNodeUpdate(t, h, map[string]interface{}{"id": second, "name": "original-identity", "serverIp": "198.51.100.21"})
		claimed <- []kr1Envelope{rename, claim}
	}()
	var others []kr1Envelope
	select {
	case others = <-claimed:
	case <-time.After(time.Second):
	}
	unblock()
	select {
	case result := <-partial:
		if result.Code != 0 {
			t.Fatalf("partial edit failed: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("partial edit did not finish")
	}
	if others == nil {
		select {
		case others = <-claimed:
		case <-time.After(5 * time.Second):
			t.Fatal("identity reassignment did not finish")
		}
	}
	for _, result := range others {
		if result.Code != 0 {
			t.Fatalf("identity reassignment failed: %+v", result)
		}
	}
	var duplicates int64
	if err := r.DB().Model(&model.Node{}).Where("name = ? AND server_ip = ?", "original-identity", "198.51.100.21").Count(&duplicates).Error; err != nil {
		t.Fatal(err)
	}
	if duplicates != 1 {
		t.Errorf("stale partial edit restored a reassigned identity: matching_nodes=%d", duplicates)
	}
}
