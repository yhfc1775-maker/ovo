package ws

import (
	"net/http"
	"strings"
)

// nodeSecretHeader 是节点 secret 的分阶段迁移头（F08 第 ① 步，
// 与 handler 包同名 helper 同口径）：WS 实时连接此前只把 secret
// 放在 URL query 里，反向代理的默认 access log 会完整记录 query。
// 面板先同时接受本头与 query，头优先、query 回退；query 暂不下
// 掉——agent 未发头，见 doc/guide/install.md“反向代理”节；待最低
// 支持 agent 版本都发头后，面板再下掉 query 支持。头值与 query
// 值一样，绝不进日志。
const nodeSecretHeader = "X-OVO-Node-Secret"

// nodeSecretFromRequest 从请求取节点 secret：请求头优先、URL
// query 回退；两者皆空即空串（调用方按未知 secret 处理）。
func nodeSecretFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if v := strings.TrimSpace(r.Header.Get(nodeSecretHeader)); v != "" {
		return v
	}
	return r.URL.Query().Get("secret")
}

// validNodeBinarySHA 校验握手自报的运行中二进制 SHA-256（NU-07）：
// 64 位 hex 才收，空串与非法值一律按未自报处理（返回 ""），不让
// 脏值落库污染核验比对。哈希是公开的 release 资产哈希，不是凭据，
// 校验失败也不进日志值。
func validNodeBinarySHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 64 {
		return ""
	}
	for _, c := range s {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return ""
	}
	return s
}
