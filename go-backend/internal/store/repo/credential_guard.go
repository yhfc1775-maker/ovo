package repo

import (
	"errors"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrUserAuthChanged means the password proof predates a credential, role,
// status, or revocation change. The caller must authenticate again.
var ErrUserAuthChanged = errors.New("user authentication state changed")

// lockUserAuthSnapshot binds a password proof to a write in the same
// transaction. PostgreSQL's row lock serializes this write with resets,
// revocations and deletion; SQLite uses the repository's single connection.
// Compare the state actually used to verify the password, never a fresh read
// taken after verification.
func lockUserAuthSnapshot(tx *gorm.DB, expected *model.User) error {
	if expected == nil || expected.ID <= 0 || expected.Status != 1 {
		return ErrUserAuthChanged
	}
	var current model.User
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", expected.ID).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrUserAuthChanged
	}
	if err != nil {
		return err
	}
	if current.Pwd != expected.Pwd || current.RoleID != expected.RoleID || current.Status != expected.Status ||
		current.PasswordChangedAt != expected.PasswordChangedAt || current.TokensRevokedBefore != expected.TokensRevokedBefore {
		return ErrUserAuthChanged
	}
	return nil
}

// CreatePasskeyIfCurrent consumes the registration begin's password proof.
// Keep the owner lock until the credential is committed so a reset cannot
// slip between the comparison and insertion.
func (r *Repository) CreatePasskeyIfCurrent(key *model.Passkey, expected *model.User) error {
	if key == nil || expected == nil || key.UserID != expected.ID {
		return ErrUserAuthChanged
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := lockUserAuthSnapshot(tx, expected); err != nil {
			return err
		}
		return tx.Create(key).Error
	})
}

// UpdateUserNameAndPasswordIfCurrent is the self-service password change.
// The unguarded variant is for trusted resets, not old-password proofs.
func (r *Repository) UpdateUserNameAndPasswordIfCurrent(expected *model.User, username, passwordHash string, now int64) error {
	if expected == nil {
		return ErrUserAuthChanged
	}
	err := r.updateUserWithRenameGuard(expected.ID, username, map[string]interface{}{
		"user": username, "pwd": passwordHash,
		"password_changed_at": nextUserRevocationTime(now), "updated_time": now,
	}, expected)
	if err == nil && expected.ID == 1 {
		clearInitialAdminPassword()
	}
	return err
}

func (r *Repository) DeletePasskeyAndRevokeSessionsIfCurrent(expected *model.User, id string, now int64) (bool, error) {
	if expected == nil {
		return false, ErrUserAuthChanged
	}
	return r.deletePasskeyAndRevokeSessions(expected.ID, id, now, expected)
}
