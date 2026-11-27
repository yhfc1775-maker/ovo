package handler

import (
	"net/http"
	"strings"
)

// nodeSecretHeader 是节点 secret 的分阶段迁移头（F08 第 ① 步）：
// agent 到面板的四个通道（实时连接 /system-info、流量上报
// /flow/upload、配置拉取 /flow/config、连接日志 /flow/connlog）
// 此前只把 secret 放在 URL query 里，反向代理的默认 access log
// 会完整记录 query。面板先同时接受本头与 query，头优先、query
// 回退；query 暂不下掉——agent 未发头，见 doc/guide/install.md
// “反向代理”节；待最低支持 agent 版本都发头后，面板再下掉 query
// 支持。头值与 query 值一样，绝不进日志（各调用面的日志口径不变）。
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
