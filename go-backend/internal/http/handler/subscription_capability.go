// NEVER rename the deployed HMAC domain: existing previews and subscription tokens depend on it.
package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"go-backend/internal/store/model"
)

const scopedSubscriptionPrefix = "~u1."

// The shared token stays server-side. Removing or altering query parameters
// cannot turn this capability into whole-subscription access. Token rotation
// invalidates capabilities; assignment changes are enforced on every fetch.
func (h *Handler) scopedSubscriptionToken(sub *model.Subscription, userID int64) string {
	mac := hmac.New(sha256.New, []byte(h.jwtSecret))
	fmt.Fprintf(mac, "flmx-scoped-sub-v1:%d:%d:%s", userID, sub.ID, sub.Token)
	return fmt.Sprintf("%s%d.%d.%s", scopedSubscriptionPrefix, userID, sub.ID, hex.EncodeToString(mac.Sum(nil)))
}

func (h *Handler) resolveSubscriptionCapability(token string) (*model.Subscription, int64, error) {
	if !strings.HasPrefix(token, scopedSubscriptionPrefix) {
		sub, err := h.repo.GetSubscriptionByToken(token)
		if sub == nil || !constantTimeEqual(token, sub.Token) {
			return nil, 0, err
		}
		return sub, 0, err
	}
	parts := strings.Split(strings.TrimPrefix(token, scopedSubscriptionPrefix), ".")
	if len(parts) != 3 {
		return nil, 0, nil
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || userID <= 0 {
		return nil, 0, nil
	}
	subID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || subID <= 0 {
		return nil, 0, nil
	}
	sub, err := h.repo.GetSubscription(subID)
	if sub == nil || !constantTimeEqual(token, h.scopedSubscriptionToken(sub, userID)) {
		return nil, 0, err
	}
	return sub, userID, err
}
