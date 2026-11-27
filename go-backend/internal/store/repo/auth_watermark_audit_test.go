package repo

import (
	"fmt"
	"testing"
)

// A request records its timestamp before waiting for SQLite's connection or a
// PostgreSQL row lock. Commit the newer request first to reproduce that ordering
// without a scheduler race. The same-millisecond case also covers tokenIssueTime:
// a new login may be issued at the previous watermark + 1.
func TestAuditAuthRevocationNeverRestoresRevokedSessions(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	type revokeFunc func(int64, string, int64) error
	operations := []struct {
		name string
		run  revokeFunc
	}{
		{"logout", func(id int64, _ string, at int64) error { return r.RevokeUserTokens(id, at) }},
		{"session_revoke", func(id int64, _ string, at int64) error { return r.RevokeUserSessions(id, at) }},
		{"password_change", func(id int64, _ string, at int64) error { return r.UpdateUserPassword(id, "new-hash", at) }},
		{"admin_password_reset", func(id int64, name string, at int64) error {
			return r.UpdateUserWithPassword(id, name, "new-hash", 0, 0, 0, 0, 1, 0, at)
		}},
		{"rename_and_password", func(id int64, name string, at int64) error {
			return r.UpdateUserNameAndPassword(id, name, "new-hash", at)
		}},
		{"legacy_password_sunset", func(id int64, _ string, at int64) error {
			applied, err := r.SunsetLegacyUserPassword(id, "21232f297a57a5a743894a0e4a801fc3", at)
			if err == nil && !applied {
				return fmt.Errorf("legacy credential was not invalidated")
			}
			return err
		}},
	}
	for _, previous := range []string{"logout", "password"} {
		for _, op := range operations {
			for _, at := range []int64{2000, 3000, 4000} {
				t.Run(fmt.Sprintf("%s/%s/%d", previous, op.name, at), func(t *testing.T) {
					name := fmt.Sprintf("%s_%s_%d", previous, op.name, at)
					id, err := r.CreateUser(name, "21232f297a57a5a743894a0e4a801fc3", 1, 0, 0, 0, 0, 1, 0, 1000)
					if err != nil {
						t.Fatal(err)
					}
					if previous == "logout" {
						err = r.RevokeUserTokens(id, 3000)
					} else {
						err = r.RevokeUserSessions(id, 3000)
					}
					if err != nil {
						t.Fatal(err)
					}
					if err := op.run(id, name, at); err != nil {
						t.Fatal(err)
					}
					state, err := r.GetUserAuthState(id)
					if err != nil {
						t.Fatal(err)
					}
					want := max(at, int64(3001))
					if state.RevocationWatermark() != want {
						t.Errorf("revocation watermark = %d, want %d after earlier watermark 3000 and request timestamp %d", state.RevocationWatermark(), want, at)
					}
					if !state.TokenStale(2500) || !state.TokenStale(3001) || state.TokenStale(want+1) {
						t.Errorf("revocation restored an old session or failed to revoke a login issued at watermark+1: %+v", state)
					}
				})
			}
		}
	}
}
