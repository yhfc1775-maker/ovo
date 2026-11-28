package handler

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

const auditSnapshotPassword = "snapshot-current-password"

func auditSnapshotUser(t *testing.T) (*repo.Repository, *Handler, int64) {
	t.Helper()
	resetLoginLimitersForTest()
	t.Setenv("OVO_WEBAUTHN_ORIGIN", "http://localhost")
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	hash, err := security.HashPassword(auditSnapshotPassword)
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.CreateUser("snapshot-user", hash, 1, 0, 0, 0, 0, 1, 0, time.Now().Add(-time.Second).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return r, New(r, "snapshot-test-secret"), id
}

// This hook runs after a non-transactional read has copied its result. It
// deterministically models another request committing before this one writes.
func auditAfterRead(t *testing.T, r *repo.Repository, table string, mutate func(), onlyList ...bool) *bool {
	t.Helper()
	fired := false
	const name = "audit_auth_snapshot_interleave"
	err := r.DB().Callback().Query().After("gorm:after_query").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != table {
			return
		}
		if len(onlyList) > 0 && onlyList[0] {
			if _, ok := tx.Statement.Dest.(*[]model.Passkey); !ok {
				return
			}
		}
		fired = true
		mutate()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.DB().Callback().Query().Remove(name) })
	return &fired
}

func TestAuditPasswordWritesRejectConcurrentRevocation(t *testing.T) {
	for _, operation := range []string{"change-password", "delete-passkey"} {
		t.Run(operation, func(t *testing.T) {
			r, h, id := auditSnapshotUser(t)
			if err := r.CreatePasskey(&model.Passkey{ID: "owned", UserID: id, CredentialJSON: "{}"}); err != nil {
				t.Fatal(err)
			}
			fired := auditAfterRead(t, r, (model.User{}).TableName(), func() {
				if err := r.RevokeUserTokens(id, time.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
			})
			w := httptest.NewRecorder()
			if operation == "change-password" {
				body := `{"newUsername":"snapshot-user","currentPassword":"snapshot-current-password","newPassword":"attacker-password","confirmPassword":"attacker-password"}`
				h.updatePassword(w, claimsCtxReq(http.MethodPost, "/api/v1/user/updatePassword", "198.51.100.240:4000", body, id, 1))
			} else {
				h.passkeyDelete(w, claimsCtxReq(http.MethodPost, "/api/v1/user/passkey/delete", "198.51.100.240:4000", `{"id":"owned","password":"snapshot-current-password"}`, id, 1))
			}
			if !*fired {
				t.Fatal("interleaved revocation did not run")
			}
			if env := decodeEnvelope(t, w); env.Code == 0 {
				t.Errorf("stale password proof changed credentials after revocation: %s", w.Body.String())
			}
			u, err := r.GetUserByID(id)
			if err != nil {
				t.Fatal(err)
			}
			if ok, _ := security.VerifyPassword(u.Pwd, auditSnapshotPassword); !ok {
				t.Error("revoked request overwrote password")
			}
			key, err := r.GetPasskey(id, "owned")
			if err != nil || key == nil {
				t.Fatalf("revoked request removed passkey: key=%v err=%v", key, err)
			}
		})
	}
}

func auditCredential(t *testing.T) (*ecdsa.PrivateKey, webauthn.Credential) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := cbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: 2, Algorithm: -7}, Curve: 1,
		XCoord: key.X.FillBytes(make([]byte, 32)), YCoord: key.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return key, webauthn.Credential{ID: []byte("snapshot-credential"), PublicKey: publicKey, AttestationType: "none"}
}

func auditCredentialJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAuditPasskeyCounterCannotRollBackAfterVerification(t *testing.T) {
	r, h, id := auditSnapshotUser(t)
	privateKey, credential := auditCredential(t)
	encode := base64.RawURLEncoding.EncodeToString
	stored := &model.Passkey{ID: encode(credential.ID), UserID: id, CredentialJSON: string(auditCredentialJSON(t, credential))}
	if err := r.CreatePasskey(stored); err != nil {
		t.Fatal(err)
	}
	wa, origin := passkeyConfig()
	_, session, err := wa.BeginDiscoverableLogin()
	if err != nil {
		t.Fatal(err)
	}
	sessionID, ok := h.putPasskeyCeremony(passkeyCeremony{kind: "login-discoverable", origin: origin, session: *session})
	if !ok {
		t.Fatal("cannot store login ceremony")
	}
	clientJSON := auditCredentialJSON(t, map[string]interface{}{"type": "webauthn.get", "challenge": session.Challenge, "origin": origin})
	rpHash := sha256.Sum256([]byte("localhost"))
	authData := append(append([]byte{}, rpHash[:]...), byte(5))
	authData = append(authData, 0, 0, 0, 1)
	clientHash := sha256.Sum256(clientJSON)
	messageHash := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, messageHash[:])
	if err != nil {
		t.Fatal(err)
	}
	userHandle := make([]byte, 8)
	binary.BigEndian.PutUint64(userHandle, uint64(id))
	body := auditCredentialJSON(t, map[string]interface{}{
		"sessionId": sessionID,
		"credential": map[string]interface{}{
			"id": encode(credential.ID), "rawId": encode(credential.ID), "type": "public-key", "clientExtensionResults": map[string]interface{}{},
			"response": map[string]interface{}{"authenticatorData": encode(authData), "clientDataJSON": encode(clientJSON), "signature": encode(signature), "userHandle": encode(userHandle)},
		},
	})
	// Another valid assertion has committed counter=10 after our verifier
	// loaded counter=0. Our older counter=1 must not overwrite it.
	fired := auditAfterRead(t, r, (model.Passkey{}).TableName(), func() {
		credential.Authenticator.SignCount = 10
		if err := r.DB().Model(&model.Passkey{}).Where("id = ?", stored.ID).Update("credential_json", string(auditCredentialJSON(t, credential))).Error; err != nil {
			t.Fatal(err)
		}
	}, true)
	w := httptest.NewRecorder()
	h.passkeyLoginFinish(w, httptest.NewRequest(http.MethodPost, "/api/v1/user/passkey/login/finish", bytes.NewReader(body)))
	if !*fired {
		t.Fatal("interleaved assertion did not run")
	}
	if env := decodeEnvelope(t, w); env.Code == 0 {
		t.Error("older verified assertion minted token after a newer credential write")
	}
	row, err := r.GetPasskey(id, stored.ID)
	if err != nil || row == nil {
		t.Fatalf("get credential: row=%v err=%v", row, err)
	}
	var got webauthn.Credential
	if err := json.Unmarshal([]byte(row.CredentialJSON), &got); err != nil {
		t.Fatal(err)
	}
	if got.Authenticator.SignCount != 10 {
		t.Fatalf("sign counter rolled back: got %d want 10", got.Authenticator.SignCount)
	}
}

func TestAuditPasskeyRegistrationProofCannotOutliveRevocation(t *testing.T) {
	for _, revokeAtFinish := range []bool{false, true} {
		t.Run(map[bool]string{false: "between-begin-and-finish", true: "after-finish-user-read"}[revokeAtFinish], func(t *testing.T) {
			r, h, id := auditSnapshotUser(t)
			w := httptest.NewRecorder()
			h.passkeyRegisterBegin(w, claimsCtxReq(http.MethodPost, "/api/v1/user/passkey/register/begin", "198.51.100.240:4000", `{"password":"snapshot-current-password"}`, id, 1))
			env := decodeEnvelope(t, w)
			if env.Code != 0 {
				t.Fatalf("begin failed: %s", w.Body.String())
			}
			var begin struct {
				SessionID string `json:"sessionId"`
				Options   struct {
					PublicKey struct {
						Challenge string `json:"challenge"`
					} `json:"publicKey"`
				} `json:"options"`
			}
			if err := json.Unmarshal(env.Data, &begin); err != nil || begin.Options.PublicKey.Challenge == "" {
				t.Fatalf("decode registration options: %s err=%v", env.Data, err)
			}
			_, credential := auditCredential(t)
			clientJSON := auditCredentialJSON(t, map[string]interface{}{"type": "webauthn.create", "challenge": begin.Options.PublicKey.Challenge, "origin": "http://localhost"})
			rpHash := sha256.Sum256([]byte("localhost"))
			authData := append(append([]byte{}, rpHash[:]...), byte(0x45)) // UP + UV + attested credential
			authData = append(authData, make([]byte, 4+16)...)
			authData = binary.BigEndian.AppendUint16(authData, uint16(len(credential.ID)))
			authData = append(authData, credential.ID...)
			authData = append(authData, credential.PublicKey...)
			attestation, err := cbor.Marshal(map[string]interface{}{"fmt": "none", "authData": authData, "attStmt": map[string]interface{}{}})
			if err != nil {
				t.Fatal(err)
			}
			encode := base64.RawURLEncoding.EncodeToString
			body := string(auditCredentialJSON(t, map[string]interface{}{
				"sessionId": begin.SessionID,
				"credential": map[string]interface{}{
					"id": encode(credential.ID), "rawId": encode(credential.ID), "type": "public-key", "clientExtensionResults": map[string]interface{}{},
					"response": map[string]interface{}{"attestationObject": encode(attestation), "clientDataJSON": encode(clientJSON)},
				},
			}))
			revoke := func() {
				if err := r.RevokeUserTokens(id, time.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			if revokeAtFinish {
				auditAfterRead(t, r, (model.User{}).TableName(), revoke)
			} else {
				revoke()
			}
			w = httptest.NewRecorder()
			h.passkeyRegisterFinish(w, claimsCtxReq(http.MethodPost, "/api/v1/user/passkey/register/finish", "198.51.100.240:4000", body, id, 1))
			if env := decodeEnvelope(t, w); env.Code == 0 {
				t.Errorf("revoked password proof registered persistent credential: %s", w.Body.String())
			}
			rows, err := r.ListPasskeys(id)
			if err != nil || len(rows) != 0 {
				t.Fatalf("stale registration left credentials: rows=%v err=%v", rows, err)
			}
		})
	}
}
