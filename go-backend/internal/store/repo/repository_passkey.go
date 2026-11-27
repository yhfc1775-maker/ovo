package repo

import (
	"database/sql"
	"errors"

	"go-backend/internal/store/model"
	"gorm.io/gorm"
)

func (r *Repository) ListPasskeys(userID int64) ([]model.Passkey, error) {
	var keys []model.Passkey
	err := r.db.Where("user_id = ?", userID).Order("created_at desc").Find(&keys).Error
	return keys, err
}

func (r *Repository) GetPasskey(userID int64, id string) (*model.Passkey, error) {
	var key model.Passkey
	err := r.db.Where("user_id = ? AND id = ?", userID, id).Take(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &key, err
}

// Credential IDs are unique across all users. Resolve the owner from the stored
// credential before checking the untrusted user handle in a discoverable login.
func (r *Repository) GetPasskeyByID(id string) (*model.Passkey, error) {
	var key model.Passkey
	err := r.db.Where("id = ?", id).Take(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &key, err
}

func (r *Repository) CreatePasskey(key *model.Passkey) error {
	return r.db.Create(key).Error
}

func (r *Repository) DeletePasskey(userID int64, id string) (bool, error) {
	result := r.db.Where("user_id = ? AND id = ?", userID, id).Delete(&model.Passkey{})
	return result.RowsAffected == 1, result.Error
}

// DeletePasskeyAndRevokeSessions commits credential removal and the session
// watermark together. A failed revocation must leave the credential available
// for a retry rather than silently retaining sessions issued with that key.
func (r *Repository) DeletePasskeyAndRevokeSessions(userID int64, id string, now int64) (bool, error) {
	return r.deletePasskeyAndRevokeSessions(userID, id, now, nil)
}

func (r *Repository) deletePasskeyAndRevokeSessions(userID int64, id string, now int64, verified *model.User) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	deleted := false
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if verified != nil {
			if err := lockUserAuthSnapshot(tx, verified); err != nil {
				return err
			}
		}
		result := tx.Where("user_id = ? AND id = ?", userID, id).Delete(&model.Passkey{})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		result = tx.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
			"password_changed_at": nextUserRevocationTime(now),
			"updated_time":        sql.NullInt64{Int64: now, Valid: true},
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("passkey owner no longer exists")
		}
		deleted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// Compare-and-swap prevents a concurrent assertion from reusing an old sign counter.
func (r *Repository) UpdatePasskeyCredential(userID int64, id, oldJSON, newJSON string, now int64) (bool, error) {
	result := r.db.Model(&model.Passkey{}).
		Where("user_id = ? AND id = ? AND credential_json = ?", userID, id, oldJSON).
		Updates(map[string]interface{}{"credential_json": newJSON, "last_used_at": now})
	return result.RowsAffected == 1, result.Error
}
