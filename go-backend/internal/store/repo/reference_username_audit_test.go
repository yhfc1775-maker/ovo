package repo

import (
	"fmt"
	"go-backend/internal/store/model"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestReferenceUsernameRenameFamily(t *testing.T) {
	for _, mode := range []string{"self", "self-verified", "admin", "admin-password", "transaction", "compound"} {
		t.Run(mode, func(t *testing.T) {
			r, err := Open(t.TempDir() + "/references.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			now := time.Now().UnixMilli()
			id, err := r.CreateUser("reference-old", "hash", 1, now+86400000, 5, 7, 3, 1, 0, now, 512)
			if err != nil {
				t.Fatal(err)
			}
			old, err := r.GetUserByID(id)
			if err != nil {
				t.Fatal(err)
			}
			f := model.Forward{UserID: id, UserName: "reference-old", Name: "reference-forward", TunnelID: 1, RemoteAddr: "192.0.2.1:80", CreatedTime: now, UpdatedTime: now, Status: 1}
			if err := r.db.Create(&f).Error; err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "self":
				err = r.UpdateUserNameAndPassword(id, "reference-new", "newhash", now+1)
			case "self-verified":
				err = r.UpdateUserNameAndPasswordIfCurrent(old, "reference-new", "newhash", now+1)
			case "admin":
				err = r.UpdateUserWithoutPassword(id, "reference-new", old.Flow, old.Num, old.ExpTime, old.FlowResetTime, old.Status, old.MaxConn, now+1)
			case "admin-password":
				err = r.UpdateUserWithPassword(id, "reference-new", "newhash", old.Flow, old.Num, old.ExpTime, old.FlowResetTime, old.Status, old.MaxConn, now+1)
			case "transaction":
				err = r.db.Transaction(func(tx *gorm.DB) error {
					return r.UpdateUserWithoutPasswordTx(tx, id, "reference-new", old.Flow, old.Num, old.ExpTime, old.FlowResetTime, old.Status, old.MaxConn, now+1)
				})
			case "compound":
				err = r.ApplyUserUpdate(UserUpdateMutation{UserID: id, Expected: old, Username: "reference-new", Flow: old.Flow, FlowMiB: old.FlowMiB, Num: old.Num, ExpTime: old.ExpTime, FlowResetTime: old.FlowResetTime, Status: old.Status, MaxConn: old.MaxConn, Now: now + 1})
			}
			if err != nil {
				t.Fatal(err)
			}
			var got model.Forward
			if err := r.db.First(&got, f.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.UserName != "reference-new" {
				t.Fatalf("%s rename committed but forward copy remained %q", mode, got.UserName)
			}
		})
	}
}

func TestReferenceUsernameCopyFailureRollsBackPrimary(t *testing.T) {
	r, err := Open(t.TempDir() + "/rollback.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	id, err := r.CreateUser("before-reference", "hash", 1, now+86400000, 5, 7, 3, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec(`CREATE TRIGGER reject_reference_name BEFORE UPDATE OF user_name ON "forward" BEGIN SELECT RAISE(ABORT,'reference copy write refused'); END`).Error; err != nil {
		t.Fatal(err)
	}
	f := model.Forward{UserID: id, UserName: "before-reference", Name: "f", TunnelID: 1, RemoteAddr: "192.0.2.1:80", CreatedTime: now, UpdatedTime: now, Status: 1}
	if err := r.db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
	err = r.UpdateUserNameAndPassword(id, "after-reference", "newhash", now+1)
	if err == nil {
		t.Error("reference write failure was not returned")
	}
	got, e := r.GetUserByID(id)
	if e != nil {
		t.Fatal(e)
	}
	if got.User != "before-reference" {
		t.Error(fmt.Sprintf("failed cascade committed primary username %q", got.User))
	}
}

func TestReferenceUsernameWritersUseCurrentParent(t *testing.T) {
	for _, mode := range []string{"create", "rollback", "import"} {
		t.Run(mode, func(t *testing.T) {
			r, err := Open(t.TempDir() + "/writer.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			now := time.Now().UnixMilli()
			id, err := r.CreateUser("current-parent", "hash", 1, now+86400000, 5, 7, 3, 1, 0, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.db.Create(&model.Tunnel{ID: 601, Name: "reference-tunnel", Type: 1, Flow: 1, TrafficRatio: 1, Protocol: "tls", Status: 1, CreatedTime: now, UpdatedTime: now}).Error; err != nil {
				t.Fatal(err)
			}
			f := model.Forward{ID: 701, UserID: id, UserName: "current-parent", Name: "writer", TunnelID: 601, RemoteAddr: "192.0.2.1:80", Strategy: "fifo", Status: 1, CreatedTime: now, UpdatedTime: now}
			switch mode {
			case "create":
				f.ID, err = r.CreateForwardTx(id, "stale-snapshot", "writer", 601, "192.0.2.1:80", "fifo", now, 0, nil, 0, "", nil, 0, 0, nil, 0, 0, 0)
			case "rollback":
				if err = r.db.Create(&f).Error; err != nil {
					t.Fatal(err)
				}
				r.RollbackForwardFields(f.ID, id, "stale-snapshot", "writer", 601, f.RemoteAddr, "fifo", 1, nil, 0, 0, nil, 0, 0, 0, now)
			case "import":
				err = r.db.Transaction(func(tx *gorm.DB) error {
					_, e := r.importForwards(tx, []model.ForwardBackup{{ID: f.ID, UserID: id, UserName: "stale-snapshot", Name: f.Name, TunnelID: 601, RemoteAddr: f.RemoteAddr, Strategy: "fifo", Status: 1, CreatedTime: now}}, now, &importReport{skipped: map[string]int{}}, nil)
					return e
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			var got model.Forward
			if err := r.db.First(&got, f.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.UserName != "current-parent" {
				t.Fatalf("%s replayed stale parent name %q", mode, got.UserName)
			}
		})
	}
}

func TestReferenceUsernameUsersOnlyImport(t *testing.T) {
	r, err := Open(t.TempDir() + "/users-import.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	id, err := r.CreateUser("before-user-import", "reference-test-password", 1, now+86400000, 5, 7, 3, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	f := model.Forward{UserID: id, UserName: "before-user-import", Name: "user-import-forward", TunnelID: 601, RemoteAddr: "192.0.2.1:80", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
		_, e := importUsers(tx, []model.UserBackup{{ID: id, User: "after-user-import", Pwd: "reference-test-password", RoleID: 1, Status: 1}}, now+1, &importReport{skipped: map[string]int{}})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	user, e := r.GetUserByID(id)
	if e != nil {
		t.Fatal(e)
	}
	if user.User != "after-user-import" {
		t.Fatalf("fixture did not rename primary: %q", user.User)
	}
	var got model.Forward
	if err := r.db.First(&got, f.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.UserName != "after-user-import" {
		t.Fatalf("users-only restore left forward name %q", got.UserName)
	}
}
