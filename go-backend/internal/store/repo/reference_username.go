package repo

import (
	"errors"
	"go-backend/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// currentForwardUsernameTx prevents a delayed create/import/rollback snapshot
// from undoing a committed rename. Lock the parent before writing its copy, in
// the same order used by user rename and forward creation. Preserve the legacy
// fallback for orphan rows rather than changing backup acceptance semantics.
func currentForwardUsernameTx(tx *gorm.DB, userID int64, fallback string) (string, error) {
	var user model.User
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "user").Where("id = ?", userID).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	return user.User, nil
}

// syncForwardUsernameTx also serves users-only backup restoration: the backup
// need not include forwards for existing copies to follow the parent rename.
func syncForwardUsernameTx(tx *gorm.DB, id int64, username string) error {
	return tx.Model(&model.Forward{}).Where("user_id = ? AND user_name <> ?", id, username).Update("user_name", username).Error
}
