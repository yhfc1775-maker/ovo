package handler

import (
	"net/http/httptest"
	"testing"
)

// F08（P2）：节点 secret 仍经 WS URL query（?secret=）传输——反向
// 代理的默认 access log 会完整记录 query，面板前置代理的日志文件
// 即成为 secret 泄露面。分阶段迁移第 ① 步（doc/guide/install.md
// “反向代理”节）：面板先同时接受新请求头 X-OVO-Node-Secret
// 与 query，头优先、query 回退；query 暂不下掉（agent 未发头）。
//
// 先红后绿：修复前 nodeSecretFromRequest 不存在（编译失败）且
// 各通道只读 query，头里的 secret 不被接受。
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
			url := "/flow/upload?seq=1"
			if tc.query != "" {
				url = "/flow/upload?secret=" + tc.query + "&seq=1"
			}
			req := httptest.NewRequest("POST", url, nil)
			if tc.header != "" {
				req.Header.Set("X-OVO-Node-Secret", tc.header)
			}
			if got := nodeSecretFromRequest(req); got != tc.want {
				t.Fatalf("nodeSecretFromRequest() = %q, want %q", got, tc.want)
			}
		})
	}
}
