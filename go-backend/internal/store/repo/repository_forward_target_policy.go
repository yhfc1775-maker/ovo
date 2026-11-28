package repo

import (
	"errors"

	"go-backend/internal/store/model"
	"gorm.io/gorm"
)

// ErrForwardTargetChanged reports that a forward's target moved while its
// policy was being frozen. It is a retry signal, not a sync failure: the
// caller re-reads the row and freezes the policy against the newest
// target instead of aborting the sync (a stale in-memory record used to
// surface this error and break the C-04 pause self-heal path).
var ErrForwardTargetChanged = errors.New("forward target changed during synchronization")

// Freeze the inferred policy of a legacy/old-backup rule once. New rules carry
// the policy captured at admission, and later setting changes cannot alter it.
func (r *Repository) EnsureForwardTargetPolicy(id int64, remote, policy string) (string, error) {
	if policy != "public" && policy != "any" {
		return "", errors.New("invalid forward target policy")
	}
	var current model.Forward
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Forward{}).Where("id = ? AND remote_addr = ? AND target_policy = ?", id, remote, "").Update("target_policy", policy).Error; err != nil {
			return err
		}
		if err := tx.Select("remote_addr", "target_policy").Where("id = ?", id).Take(&current).Error; err != nil {
			return err
		}
		if current.RemoteAddr != remote {
			return ErrForwardTargetChanged
		}
		return nil
	})
	return current.TargetPolicy, err
}

// UpdateForwardTargetPolicy overwrites the stored target policy of a
// forward. It is the sync funnel's repair path for a policy frozen before
// a system retarget moved the target to a non-public literal (F03): the
// stored "public" no longer matches the stored target, and the row must
// be healed to "any" so the relay is not fail-closed by its own guard.
// It never clears a policy and never writes an unknown value.
func (r *Repository) UpdateForwardTargetPolicy(id int64, policy string) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if policy != "public" && policy != "any" {
		return errors.New("invalid forward target policy")
	}
	return r.db.Model(&model.Forward{}).Where("id = ?", id).Update("target_policy", policy).Error
}
