package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNodeRotateSecretOneTimeReceipt(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-rotate", "2.1.28")
	old := "secret-x"
	call := func(body string) (int, string, nodeSecretRotateResponse) {
		t.Helper()
		rr := httptest.NewRecorder()
		h.nodeRotateSecret(rr, httptest.NewRequest(http.MethodPost, "/api/v1/node/rotate-secret", strings.NewReader(body)))
		var envelope struct {
			Code    int                      `json:"code"`
			Warning string                   `json:"warning"`
			Data    nodeSecretRotateResponse `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Code, envelope.Warning, envelope.Data
	}
	for _, body := range []string{`{`, `{"id":0}`, `{"id":-1}`, `{"id":999}`} {
		if code, _, _ := call(body); code == 0 {
			t.Fatalf("invalid request succeeded: %s", body)
		}
	}
	for i := 0; i < 2; i++ {
		code, warning, data := call(`{"id":7}`)
		if code != 0 || !data.Rotated || len(data.Secret) != 32 || data.Secret == old || !strings.Contains(warning, "仅显示一次") {
			t.Fatal("rotation must disclose a fresh one-time secret and recovery warning")
		}
		if node, err := r.GetNodeBySecret(old); err != nil || node != nil {
			t.Fatal("old secret still authenticates")
		}
		if node, err := r.GetNodeBySecret(data.Secret); err != nil || node == nil || node.ID != 7 {
			t.Fatal("new secret does not authenticate")
		}
		stored, err := r.GetNodeByID(7)
		if err != nil || stored.Secret != "" || stored.SecretHash == "" {
			t.Fatal("rotation must persist only the hash")
		}
		old = data.Secret
	}
}
