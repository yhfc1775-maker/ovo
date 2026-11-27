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

	"go-backend/internal/auth"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

// Exercise the real WebAuthn verifier with a locally signed assertion. Delete
// its key after the sign-counter CAS, before the final user lookup: that
// already-verified assertion must not escape the deletion's session revocation.
func TestPasskeyLoginCannotOutliveConcurrentCredentialDeletion(t *testing.T) {
	resetLoginLimitersForTest()
	t.Setenv("OVO_WEBAUTHN_ORIGIN", "http://localhost")
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	hash, err := security.HashPassword("passkey-login-race-password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.CreateUser("passkey-login-race", hash, 1, 0, 0, 0, 0, 1, 0, time.Now().Add(-time.Second).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := cbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: 2, Algorithm: -7}, Curve: 1,
		XCoord: privateKey.X.FillBytes(make([]byte, 32)), YCoord: privateKey.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialID := []byte("local-audit-credential")
	credentialJSON, err := json.Marshal(webauthn.Credential{ID: credentialID, PublicKey: publicKey, AttestationType: "none"})
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	if err := r.CreatePasskey(&model.Passkey{ID: encode(credentialID), UserID: id, CredentialJSON: string(credentialJSON)}); err != nil {
		t.Fatal(err)
	}
	h := New(r, "test-secret")
	wa, origin := passkeyConfig()
	if wa == nil {
		t.Fatal("local WebAuthn fixture is disabled")
	}
	_, session, err := wa.BeginDiscoverableLogin()
	if err != nil {
		t.Fatal(err)
	}
	sessionID, ok := h.putPasskeyCeremony(passkeyCeremony{kind: "login-discoverable", origin: origin, session: *session})
	if !ok {
		t.Fatal("store local login ceremony")
	}
	clientJSON, err := json.Marshal(map[string]interface{}{"type": "webauthn.get", "challenge": session.Challenge, "origin": origin})
	if err != nil {
		t.Fatal(err)
	}
	rpHash := sha256.Sum256([]byte("localhost"))
	authData := append(append([]byte{}, rpHash[:]...), byte(5)) // user present and verified
	authData = append(authData, 0, 0, 0, 1)
	clientHash := sha256.Sum256(clientJSON)
	messageHash := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, messageHash[:])
	if err != nil {
		t.Fatal(err)
	}
	userHandle := make([]byte, 8)
	binary.BigEndian.PutUint64(userHandle, uint64(id))
	assertion := map[string]interface{}{
		"id": encode(credentialID), "rawId": encode(credentialID), "type": "public-key", "clientExtensionResults": map[string]interface{}{},
		"response": map[string]interface{}{"authenticatorData": encode(authData), "clientDataJSON": encode(clientJSON), "signature": encode(signature), "userHandle": encode(userHandle)},
	}
	body, err := json.Marshal(map[string]interface{}{"sessionId": sessionID, "credential": assertion})
	if err != nil {
		t.Fatal(err)
	}
	deleted := false
	var deletionErr error
	const callback = "audit_delete_key_after_valid_assertion"
	if err := r.DB().Callback().Update().After("gorm:commit_or_rollback_transaction").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != (model.Passkey{}).TableName() || deleted {
			return
		}
		deleted, deletionErr = r.DeletePasskeyAndRevokeSessions(id, encode(credentialID), time.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Update().Remove(callback)
	w := httptest.NewRecorder()
	rq := httptest.NewRequest(http.MethodPost, "/api/v1/user/passkey/login/finish", bytes.NewReader(body))
	rq.RemoteAddr = "198.51.100.245:4000"
	h.passkeyLoginFinish(w, rq)
	if !deleted || deletionErr != nil {
		t.Fatalf("fixture did not reach validated credential CAS: deleted=%v err=%v response=%s", deleted, deletionErr, w.Body.String())
	}
	response := decodeEnvelope(t, w)
	if response.Code != 0 {
		return
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseClaims(data.Token, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.GetUserAuthState(id)
	if err != nil {
		t.Fatal(err)
	}
	if !state.TokenStale(claims.IatMs) {
		t.Fatalf("deleted passkey assertion minted a live session after revocation: iat=%d watermark=%d", claims.IatMs, state.RevocationWatermark())
	}
}
