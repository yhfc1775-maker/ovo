package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

const importPreviewClash = `proxies:
  - name: Shared Name
    type: socks5
    server: 203.0.113.10
    port: 1080
    username: preview-user
    password: do-not-return-this-secret
  - name: Drop Me
    type: socks5
    server: 203.0.113.11
    port: 1081
    username: preview-user-2
    password: another-secret
  - name: Broken
    type: unsupported-type
    server: invalid.example
    port: 1
`

const importPreviewPluginClash = `proxies:
  - name: Plugin VMess
    type: vmess
    server: 203.0.113.20
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    alterId: 0
    cipher: auto
    tls: true
    network: ws
    ws-opts:
      path: /ws-path
      headers:
        Host: ws.example.com
    plugin: v2ray-plugin
    plugin-opts:
      mode: websocket
      tls: true
      host: plugin-host.example
      path: /plugin-secret-do-not-leak
  - name: Plugin SS
    type: ss
    server: 203.0.113.21
    port: 8388
    cipher: aes-256-gcm
    password: ss-password-do-not-leak
    plugin: shadow-tls
    plugin-opts:
      host: shadow-host.example
      password: shadow-tls-secret-do-not-leak
      version: 3
`

type importPreviewEnvelope struct {
	Code int `json:"code"`
	Data struct {
		Fingerprint string `json:"fingerprint"`
		Nodes       []struct {
			IdentityKey     string `json:"identityKey"`
			Name            string `json:"name"`
			Protocol        string `json:"protocol"`
			Address         string `json:"address"`
			Port            int    `json:"port"`
			Description     string `json:"description"`
			NameDuplicated  bool   `json:"nameDuplicated"`
			AlreadyImported bool   `json:"alreadyImported"`
		} `json:"nodes"`
		Errors []string `json:"errors"`
	} `json:"data"`
}

func TestInboundImportPreviewRecognizesExistingIdentityWithoutFalseNameCollision(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	nodes, errs := kernel.ParseImport(importPreviewClash)
	if len(nodes) != 2 || len(errs) != 1 {
		t.Fatalf("fixture ParseImport nodes=%d errors=%v", len(nodes), errs)
	}
	row, err := externalRow(nodes[0], false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatal(err)
	}
	rec := postJSONToHandler(t, h.inboundImportPreview, map[string]interface{}{"links": importPreviewClash})
	var envelope importPreviewEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Nodes) != 2 || !envelope.Data.Nodes[0].AlreadyImported || envelope.Data.Nodes[0].NameDuplicated {
		t.Fatalf("existing node should be marked duplicate-by-identity only: %+v", envelope.Data.Nodes)
	}
	if envelope.Data.Nodes[0].Name != "Shared Name" {
		t.Fatalf("existing node preview name changed despite import being skipped: %q", envelope.Data.Nodes[0].Name)
	}
}

func TestInboundImportPreviewDisambiguatesNamesAgainstOtherIdentities(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	nodes, _ := kernel.ParseImport(importPreviewClash)
	row, err := externalRow(nodes[0], false, 1)
	if err != nil {
		t.Fatal(err)
	}
	row.Name = "Drop Me"
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatal(err)
	}
	rec := postJSONToHandler(t, h.inboundImportPreview, map[string]interface{}{"links": importPreviewClash})
	var envelope importPreviewEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Nodes) != 2 || !envelope.Data.Nodes[1].NameDuplicated || envelope.Data.Nodes[1].Name != "Drop Me (2)" {
		t.Fatalf("same name on another identity should receive a visible suffix: %+v", envelope.Data.Nodes)
	}
	result := postJSONToHandler(t, h.inboundImport, map[string]interface{}{
		"links": importPreviewClash, "previewFingerprint": envelope.Data.Fingerprint,
		"selected": []string{envelope.Data.Nodes[1].IdentityKey},
	})
	var imported struct {
		Code int `json:"code"`
		Data struct {
			Warnings []string `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Code != 0 || len(imported.Data.Warnings) != 1 || !strings.Contains(imported.Data.Warnings[0], "Drop Me (2)") {
		t.Fatalf("confirmation should report the automatic name suffix: %s", result.Body.String())
	}
}

func TestInboundImportPreviewParsesWithoutWritingAndRedactsCredentials(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	rec := postJSONToHandler(t, h.inboundImportPreview, map[string]interface{}{"links": importPreviewClash})
	var envelope importPreviewEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode preview response: %v body=%s", err, rec.Body.String())
	}
	if envelope.Code != 0 || envelope.Data.Fingerprint == "" {
		t.Fatalf("preview response missing success/fingerprint: %s", rec.Body.String())
	}
	if len(envelope.Data.Nodes) != 2 || len(envelope.Data.Errors) != 1 {
		t.Fatalf("preview counts = nodes %d errors %v, want 2 valid and 1 parse error", len(envelope.Data.Nodes), envelope.Data.Errors)
	}
	first := envelope.Data.Nodes[0]
	if first.Name != "Shared Name" || first.Protocol != "socks5" || first.Address != "203.0.113.10" || first.Port != 1080 {
		t.Fatalf("unexpected preview row: %+v", first)
	}
	if strings.Contains(rec.Body.String(), "do-not-return-this-secret") || strings.Contains(rec.Body.String(), "another-secret") || strings.Contains(rec.Body.String(), "preview-user") {
		t.Fatalf("preview leaked credentials: %s", rec.Body.String())
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 0 {
		t.Fatalf("preview must not write nodes: rows=%d err=%v", len(rows), err)
	}
}

func TestInboundImportPreviewRedactsPluginProxyCredentials(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	rec := postJSONToHandler(t, h.inboundImportPreview, map[string]interface{}{"links": importPreviewPluginClash})
	var envelope importPreviewEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode preview response: %v body=%s", err, rec.Body.String())
	}
	if envelope.Code != 0 || len(envelope.Data.Nodes) != 2 {
		t.Fatalf("plugin preview nodes=%d code=%d body=%s", len(envelope.Data.Nodes), envelope.Code, rec.Body.String())
	}
	// The plugin surface is exercised, not skipped: the plugin name is
	// display material and must appear, while every credential the
	// plugin carries must not.
	if !strings.Contains(envelope.Data.Nodes[0].Description, "v2ray-plugin") || !strings.Contains(envelope.Data.Nodes[1].Description, "shadow-tls") {
		t.Fatalf("plugin nodes should name their plugin in the description: %+v", envelope.Data.Nodes)
	}
	for _, secret := range []string{
		"11111111-2222-3333-4444-555555555555",
		"ss-password-do-not-leak",
		"plugin-secret-do-not-leak",
		"shadow-tls-secret-do-not-leak",
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("preview leaked credential %q: %s", secret, rec.Body.String())
		}
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 0 {
		t.Fatalf("preview must not write nodes: rows=%d err=%v", len(rows), err)
	}
}

func TestInboundImportPreviewConfirmationAppliesSelectionNamesAndRelayOnly(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	previewRec := postJSONToHandler(t, h.inboundImportPreview, map[string]interface{}{"links": importPreviewClash})
	var preview importPreviewEnvelope
	if err := json.Unmarshal(previewRec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Data.Nodes) != 2 {
		t.Fatalf("preview nodes=%d, want 2", len(preview.Data.Nodes))
	}
	custom := strings.Repeat("界", 105)
	result := postJSONToHandler(t, h.inboundImport, map[string]interface{}{
		"links":              importPreviewClash,
		"relayOnly":          true,
		"previewFingerprint": preview.Data.Fingerprint,
		"selected":           []string{preview.Data.Nodes[0].IdentityKey},
		"nameOverrides":      map[string]string{preview.Data.Nodes[0].IdentityKey: custom},
	})
	if code, msg := decodeEnvelopeA204(t, result); code != 0 {
		t.Fatalf("confirmed import failed: code=%d msg=%s body=%s", code, msg, result.Body.String())
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 1 {
		t.Fatalf("confirmed selected import rows=%d err=%v, want exactly 1", len(rows), err)
	}
	if rows[0].Name != strings.Repeat("界", 100) || rows[0].RelayOnly != 1 || rows[0].Listen != "203.0.113.10" {
		t.Fatalf("import ignored the confirmed edit/relay choice: %+v", rows[0])
	}
}

func TestInboundImportPreviewConfirmationRejectsChangedContent(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	result := postJSONToHandler(t, h.inboundImport, map[string]interface{}{
		"links":              importPreviewClash,
		"previewFingerprint": "not-the-preview-hash",
		"selected":           []string{"anything"},
		"nameOverrides":      map[string]string{},
	})
	if code, msg := decodeEnvelopeA204(t, result); code == 0 || !strings.Contains(msg, "重新解析") {
		t.Fatalf("changed preview should be rejected, code=%d msg=%q body=%s", code, msg, result.Body.String())
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 0 {
		t.Fatalf("changed preview wrote nodes: rows=%d err=%v", len(rows), err)
	}
}

func TestInboundImportRejectsMissingPreviewReceiptBeforeWriting(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	rec := postJSONToHandler(t, h.inboundImport, map[string]interface{}{"links": importPreviewClash})
	if code, msg := decodeEnvelopeA204(t, rec); code == 0 || !strings.Contains(msg, "预览") {
		t.Fatalf("import without preview receipt should be rejected, code=%d msg=%q body=%s", code, msg, rec.Body.String())
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 0 {
		t.Fatalf("import without preview receipt wrote nodes: rows=%d err=%v", len(rows), err)
	}
}

func TestInboundImportRejectsUnsignedContentHashBeforeWriting(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	digest := sha256.Sum256([]byte(strings.TrimSpace(importPreviewClash)))
	rec := postJSONToHandler(t, h.inboundImport, map[string]interface{}{
		"links":              importPreviewClash,
		"previewFingerprint": hex.EncodeToString(digest[:]),
	})
	if code, msg := decodeEnvelopeA204(t, rec); code == 0 || !strings.Contains(msg, "重新解析") {
		t.Fatalf("unsigned content hash should not count as a preview receipt, code=%d msg=%q body=%s", code, msg, rec.Body.String())
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 0 {
		t.Fatalf("unsigned content hash wrote nodes: rows=%d err=%v", len(rows), err)
	}
}

func TestRemoteSourcePreviewPreferencesApplyToInitialAndLaterSync(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	parsed, parseErrors := kernel.ParseImport(importPreviewClash)
	if len(parsed) != 2 || len(parseErrors) != 1 {
		t.Fatalf("fixture ParseImport nodes=%d errors=%v", len(parsed), parseErrors)
	}
	keepKey := externalIdentityKey(parsed[0].Host, parsed[0].Port, parsed[0].Proxy)
	dropKey := externalIdentityKey(parsed[1].Host, parsed[1].Port, parsed[1].Proxy)
	// IMPORT-PREVIEW-01：写节点同步的保存必须带预览回执——先走
	// 预览拿到指纹再保存（无回执的新建源会被直接拒绝）。
	previewRec := postJSONToHandler(t, h.inboundImportPreview, map[string]interface{}{"links": importPreviewClash})
	var preview importPreviewEnvelope
	if err := json.Unmarshal(previewRec.Body.Bytes(), &preview); err != nil || preview.Code != 0 {
		t.Fatalf("preview import failed: err=%v body=%s", err, previewRec.Body.String())
	}
	rec := postJSONToHandler(t, h.remoteSourceSave, map[string]interface{}{
		"name": "Preview source", "kind": "file", "content": importPreviewClash,
		"intervalHours": 0, "relayOnly": true,
		"previewFingerprint": preview.Data.Fingerprint,
		"nameOverrides":      map[string]string{keepKey: "Chosen name"},
		"excludedIdentities": []string{dropKey},
	})
	if code, msg := decodeEnvelopeA204(t, rec); code != 0 {
		t.Fatalf("save preview source failed: code=%d msg=%s body=%s", code, msg, rec.Body.String())
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 1 {
		t.Fatalf("initial source sync rows=%d err=%v, want one selected row", len(rows), err)
	}
	if rows[0].Name != "Chosen name" || rows[0].RelayOnly != 1 {
		t.Fatalf("initial source sync ignored preview preferences: %+v", rows[0])
	}
	source, err := r.ListRemoteSources()
	if err != nil || len(source) != 1 || source[0].NameOverrides == "" || source[0].ExcludedIdentities == "" {
		t.Fatalf("preview preferences not persisted: sources=%+v err=%v", source, err)
	}
	if _, err := h.syncRemoteSource(t.Context(), &source[0], importPreviewClash); err != nil {
		t.Fatalf("subsequent source sync: %v", err)
	}
	rows, err = r.ListNodeInbounds()
	if err != nil || len(rows) != 1 || rows[0].Name != "Chosen name" || rows[0].RelayOnly != 1 {
		t.Fatalf("later source sync lost preview choices: rows=%+v err=%v", rows, err)
	}
	backup, err := r.ExportPartial([]string{"inbounds"})
	if err != nil || backup.Inbounds == nil || len(backup.Inbounds.Sources) != 1 {
		t.Fatalf("export preview source preferences: backup=%+v err=%v", backup, err)
	}
	restored, err := repo.Open(t.TempDir() + "/import-preview-restored.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if _, err := restored.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatalf("restore preview source preferences: %v", err)
	}
	restoredSources, err := restored.ListRemoteSources()
	if err != nil || len(restoredSources) != 1 || restoredSources[0].NameOverrides != source[0].NameOverrides || restoredSources[0].ExcludedIdentities != source[0].ExcludedIdentities {
		t.Fatalf("backup restore lost preview choices: sources=%+v err=%v", restoredSources, err)
	}
}

func TestRemoteSourceSaveRejectsChangedPreviewBeforeCreatingSource(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	rec := postJSONToHandler(t, h.remoteSourceSave, map[string]interface{}{
		"name": "Stale preview", "kind": "file", "content": importPreviewClash,
		"intervalHours": 0, "previewFingerprint": "stale-fingerprint",
	})
	if code, msg := decodeEnvelopeA204(t, rec); code == 0 || !strings.Contains(msg, "重新解析") {
		t.Fatalf("stale source preview should be rejected, code=%d msg=%q body=%s", code, msg, rec.Body.String())
	}
	sources, err := r.ListRemoteSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("stale source preview created a source: sources=%+v err=%v", sources, err)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 0 {
		t.Fatalf("stale source preview wrote nodes: rows=%+v err=%v", rows, err)
	}
}

// IMPORT-PREVIEW-01：无预览回执的新建源（文件与 URL 两类）在保存
// 入口即被拒绝——源行与节点行一律不得落库。
func TestRemoteSourceSaveRequiresPreviewReceiptBeforeWriting(t *testing.T) {
	h, r := inboundImportPreviewFixture(t)
	rec := postJSONToHandler(t, h.remoteSourceSave, map[string]interface{}{
		"name": "No receipt file", "kind": "file", "content": importPreviewClash,
		"intervalHours": 0, "relayOnly": true,
	})
	if code, msg := decodeEnvelopeA204(t, rec); code == 0 || !strings.Contains(msg, "请先解析预览后再确认导入") {
		t.Fatalf("file source save without preview receipt should be rejected, code=%d msg=%q body=%s", code, msg, rec.Body.String())
	}
	// URL 源无回执时同样在保存前拒绝——拒绝点在任何拉取之前，
	// 不依赖远端可达。
	rec = postJSONToHandler(t, h.remoteSourceSave, map[string]interface{}{
		"name": "No receipt url", "kind": "url", "url": "https://example.com/sub",
		"intervalHours": 12, "relayOnly": false,
	})
	if code, msg := decodeEnvelopeA204(t, rec); code == 0 || !strings.Contains(msg, "请先解析预览后再确认导入") {
		t.Fatalf("url source save without preview receipt should be rejected, code=%d msg=%q body=%s", code, msg, rec.Body.String())
	}
	sources, err := r.ListRemoteSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("receipt-less source saves created sources: sources=%+v err=%v", sources, err)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil || len(rows) != 0 {
		t.Fatalf("receipt-less source saves wrote nodes: rows=%+v err=%v", rows, err)
	}
}

func inboundImportPreviewFixture(t *testing.T) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/import-preview.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "import-preview-secret"), r
}

func postConfirmedInboundImport(t *testing.T, h *Handler, links string) *httptest.ResponseRecorder {
	t.Helper()
	previewRec := postJSONToHandler(t, h.inboundImportPreview, map[string]interface{}{"links": links})
	var preview importPreviewEnvelope
	if err := json.Unmarshal(previewRec.Body.Bytes(), &preview); err != nil || preview.Code != 0 {
		t.Fatalf("preview import failed: err=%v body=%s", err, previewRec.Body.String())
	}
	selected := make([]string, 0, len(preview.Data.Nodes))
	for _, node := range preview.Data.Nodes {
		selected = append(selected, node.IdentityKey)
	}
	return postJSONToHandler(t, h.inboundImport, map[string]interface{}{
		"links": links, "previewFingerprint": preview.Data.Fingerprint, "selected": selected,
	})
}
