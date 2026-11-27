package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestNodeCascadeRetiresUninstallAttemptAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "delete-ledger-failure"}[fail], func(t *testing.T) {
			r := openCascadeRepo(t, "uninstall-cascade.db")
			node := model.Node{Name: "uninstall-cascade", Secret: "fixture-secret", ServerIP: "127.0.0.1", Port: "30000", Status: 1}
			if err := r.DB().Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
			if fail {
				if err := r.DB().Exec(`CREATE TRIGGER reject_pending_retire BEFORE DELETE ON node_pending_uninstall BEGIN SELECT RAISE(FAIL, 'injected retire failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := r.DeleteNodeCascade(node.ID)
			if (err != nil) != fail {
				t.Fatalf("uninstall registry failure was not part of the parent transaction: %v", err)
			}
			parent, perr := r.GetNodeByID(node.ID)
			if perr != nil {
				t.Fatal(perr)
			}
			pending, rerr := r.GetNodePendingUninstall(node.ID)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if (parent != nil) != fail || (pending != nil) != fail {
				t.Fatalf("parent/attempt split after cascade: parent=%v pending=%v", parent != nil, pending != nil)
			}
		})
	}
}
