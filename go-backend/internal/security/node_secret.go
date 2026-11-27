// NEVER rename the flmx cryptographic domains or persisted spool prefixes: deployed values depend on them.
package security

import (
	"crypto/sha256"
	"encoding/hex"
)

// 节点 secret 的存储形态（F03）：只存哈希、永不存明文。
//
// HashNodeSecret 是节点 secret 的唯一存储哈希：SHA-256（域分隔
// "flmx:node-secret-v1:" + 明文，hex 64 位）。选用确定性哈希而非
// bcrypt 加盐，原因有三：① 面板须按候选值查库（WHERE secret_hash
// = ?，加盐哈希无法等值查）；② 存量明文行可一次性哈希迁移而不
// 破坏已在线节点的认证（agent 出示明文 → 面板哈希比对）；③ 域分
// 隔使存储哈希与流量 AES 密钥派生（SHA-256(secret)，见
// NewAESCrypto）及内核 mark/端口派生（SHA-256(domain+secret)）
// 均不同值，任一派生值泄露都推不出存储哈希、更推不出明文。
//
// 注意：存储哈希仍是敏感值——持有它不能直接冒充节点（认证须出示
// 明文原值），但它可用于离线字典攻击（secret 仅 32 位 hex、熵
// 128 位，暴力不可行）。备份导出同样只出哈希、不出明文。
const nodeSecretHashDomain = "flmx:node-secret-v1:"

// HashNodeSecret 返回 secret 的存储哈希（64 位小写 hex）。
func HashNodeSecret(secret string) string {
	sum := sha256.Sum256([]byte(nodeSecretHashDomain + secret))
	return hex.EncodeToString(sum[:])
}

// IsNodeSecretHash 报告 s 是否为合法的节点 secret 存储哈希形态
// （64 位小写 hex）。备份恢复面用它识别新格式的 secretHash。
func IsNodeSecretHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}
