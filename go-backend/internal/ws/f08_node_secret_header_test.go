package ws

import (
	"net/http/httptest"
	"testing"
)

// F08（P2）WS 通道同 handler 面：分阶段迁移第 ① 步——面板先同时
// 接受 X-OVO-Node-Secret 头与 ?secret= query，头优先、query
// 回退；query 暂不下掉（agent 未发头，见 doc/guide/install.md）。
//
// 先红后绿：修复前 nodeSecretFromRequest 不存在（编译失败），
// WS 握手只读 query。
func TestF08NodeSecretFromRequest(t *testing.T) {
	cases := []struct {
		name   string
		header string
		query  string
		want   string
	}{
		{"header wins over query", "header-secret", "query-secret", "header-secret"},
		{"query fallback when no header", "", "query-secret", "query-secret"},
		{"empty header falls back to query", "", "query-secret", "query-secret"},
		{"neither yields empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "/system-info?type=1"
			if tc.query != "" {
				url = "/system-info?type=1&secret=" + tc.query
			}
			req := httptest.NewRequest("GET", url, nil)
			if tc.header != "" {
				req.Header.Set("X-OVO-Node-Secret", tc.header)
			}
			if got := nodeSecretFromRequest(req); got != tc.want {
				t.Fatalf("nodeSecretFromRequest() = %q, want %q", got, tc.want)
			}
		})
	}
}
