package security

import (
	"strings"
	"testing"
)

// F03（P1）：节点 secret 只存哈希——HashNodeSecret 是节点 secret
// 的唯一存储形态：SHA-256（域分隔 "flmx:node-secret-v1:" + 明文，
// hex 64 位）。确定性哈希（非 bcrypt 加盐）：面板须按候选值查库
// （WHERE secret_hash = ?），且存量明文行可一次性哈希迁移而不
// 破坏已在线节点的认证（agent 出示明文 → 面板哈希比对）。
//
// 先红后绿：修复前 HashNodeSecret / IsNodeSecretHash 不存在。
func TestF03HashNodeSecret(t *testing.T) {
	a := HashNodeSecret("0123456789abcdef0123456789abcdef")
	b := HashNodeSecret("0123456789abcdef0123456789abcdef")
	if a != b || len(a) != 64 || !IsNodeSecretHash(a) {
		t.Fatalf("hash must be deterministic 64-hex, got %q", a)
	}
	// 换一枚 secret 即换哈希（无碰撞口）；域分隔使之与裸 SHA-256
	// 及流量 AES 密钥派生（SHA-256(secret)）均不同值。
	if c := HashNodeSecret("fedcba9876543210fedcba9876543210"); c == a {
		t.Fatalf("distinct secrets must hash distinctly")
	}
	if strings.Contains(a, "0123456789abcdef") {
		t.Fatalf("hash must not embed the plaintext secret: %q", a)
	}
}

func TestF03IsNodeSecretHash(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", true},
		{"0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", false},
		{"0123456789abcdef0123456789abcdef", false}, // 32 位明文不是哈希
		{"", false},
		{"zzzz56789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false},
	}
	for _, tc := range cases {
		if got := IsNodeSecretHash(tc.in); got != tc.want {
			t.Fatalf("IsNodeSecretHash(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
