package repo

import (
	"errors"
	"go-backend/internal/store/model"
	"testing"
	"time"
)

func TestAuditPasswordProofWriteFamily(t *testing.T) {
	for _, change := range []string{"password", "disable", "role", "logout", "delete"} {
		for _, operation := range []string{"password", "register", "delete-key"} {
			t.Run(change+"/"+operation, func(t *testing.T) {
				r, err := Open(":memory:")
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				now := time.Now().UnixMilli()
				id, err := r.CreateUser("proof-user", "synthetic-old-hash", 1, 0, 0, 0, 0, 1, 0, now-1000)
				if err != nil {
					t.Fatal(err)
				}
				if err = r.CreatePasskey(&model.Passkey{ID: "owned", UserID: id, CredentialJSON: "{}"}); err != nil {
					t.Fatal(err)
				}
				expected, err := r.GetUserByID(id)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "password":
					err = r.UpdateUserPassword(id, "synthetic-reset-hash", now)
				case "disable":
					err = r.db.Model(&model.User{}).Where("id = ?", id).Update("status", 0).Error
				case "role":
					err = r.db.Model(&model.User{}).Where("id = ?", id).Update("role_id", 0).Error
				case "logout":
					err = r.RevokeUserTokens(id, now)
				case "delete":
					err = r.DeleteUserCascade(id)
				}
				if err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "password":
					err = r.UpdateUserNameAndPasswordIfCurrent(expected, "proof-user", "stale-proof-hash", now)
				case "register":
					err = r.CreatePasskeyIfCurrent(&model.Passkey{ID: "stale", UserID: id, CredentialJSON: "{}"}, expected)
				case "delete-key":
					_, err = r.DeletePasskeyAndRevokeSessionsIfCurrent(expected, "owned", now)
				}
				if !errors.Is(err, ErrUserAuthChanged) {
					t.Fatalf("stale proof accepted: %v", err)
				}
				key, e := r.GetPasskey(id, "stale")
				if e != nil || key != nil {
					t.Fatalf("stale credential persisted: %v %v", key, e)
				}
				if change != "delete" {
					key, e = r.GetPasskey(id, "owned")
					if e != nil || key == nil {
						t.Fatal("old key removed by stale proof")
					}
				}
			})
		}
	}
}
