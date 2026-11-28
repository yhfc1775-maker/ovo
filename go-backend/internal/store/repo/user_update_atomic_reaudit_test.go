package repo

import (
	"errors"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestApplyUserUpdateRejectsStaleSnapshot(t *testing.T) {
	r, err := Open(t.TempDir() + "/user-update-stale.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	userID, err := r.CreateUser("stale-user", "hash", 1, now+86400000, 5, 7, 3, 1, 0, now, 512)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.GetUserByID(userID)
	if err != nil || snapshot == nil {
		t.Fatalf("load initial snapshot: user=%+v err=%v", snapshot, err)
	}
	if err := r.DB().Model(&model.User{}).Where("id = ?", userID).Update("num", 9).Error; err != nil {
		t.Fatal(err)
	}
	err = r.ApplyUserUpdate(UserUpdateMutation{
		UserID: userID, Expected: snapshot, Username: "stale-user-renamed", Flow: 200,
		Num: snapshot.Num, ExpTime: snapshot.ExpTime, FlowResetTime: snapshot.FlowResetTime,
		Status: snapshot.Status, MaxConn: snapshot.MaxConn, Now: now,
	})
	if !errors.Is(err, ErrUserUpdateConflict) {
		t.Fatalf("stale update error = %v, want ErrUserUpdateConflict", err)
	}
	after, err := r.GetUserByID(userID)
	if err != nil || after == nil {
		t.Fatalf("load state after rejected update: user=%+v err=%v", after, err)
	}
	if after.User != "stale-user" || after.Flow != snapshot.Flow || after.Num != 9 {
		t.Fatalf("stale update mutated current row: username=%q flow=%d num=%d", after.User, after.Flow, after.Num)
	}
}
