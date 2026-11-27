package handler

import (
	"strings"
	"testing"
)

// B3-01（生成面）: 存量 secret 字符集门 + 安装命令全插值安全
// 引用。导入面字符集校验在恢复路（FIX-B5 域），此处只钉生成面
// 两堵。

func TestB301SecretCharsetGate(t *testing.T) {
	if err := validateNodeSecretForInstall("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("canonical 32-hex secret must pass: %v", err)
	}
	for _, bad := range []string{
		"",
		"secret-x",
		"0123456789ABCDEF0123456789abcdef",  // 大写
		"0123456789abcdef0123456789abcde",   // 31 位
		"0123456789abcdef0123456789abcdef0", // 33 位
		"'; rm -rf /; echo '",               // 注入形态
		"0123456789abcdef0123456789abcdeg",  // 非 hex 字符
		"0123456789abcdef 0123456789abcdef", // 内嵌空格
	} {
		if err := validateNodeSecretForInstall(bad); err == nil {
			t.Fatalf("secret %q must be rejected", bad)
		}
	}
}

func TestB301InstallCommandsQuoteInterpolants(t *testing.T) {
	hostile := `a'b;$(touch /tmp/pwned)`
	// F09：secret 不再进入任何形态的生成命令文本（hostile 值即
	// 使含单引号/$()/分号也无从注入——命令里根本没有它可插值的
	// 位置）；-a 地址插值仍须是转义后的单引号形态。
	cmd := buildNodeInstallCommand("1.9.9", "wss://panel.example.com:443")
	if strings.Contains(cmd, hostile) || strings.Contains(cmd, "OVO_NODE_SECRET") {
		t.Fatalf("script-form command must not contain the secret at all, got %q", cmd)
	}
	if !strings.Contains(cmd, "./install.sh -a 'wss://panel.example.com:443'") {
		t.Fatalf("script-form command must still carry the quoted -a address, got %q", cmd)
	}
	// Docker/Compose 形态共用 agentConfigWriteStep：写配置只从
	// $OVO_NODE_SECRET 取值（内层 printf 仍走 sh -c 环境取
	// 值，不作任何命令的 argv 参数）；hostile 值无处可插。
	step := agentConfigWriteStep("wss://panel.example.com:443")
	if strings.Contains(step, hostile) {
		t.Fatalf("config write step must not contain the secret at all, got %q", step)
	}
	if !strings.Contains(step, `OVO_AGENT_SECRET="$OVO_NODE_SECRET"`) {
		t.Fatalf("config write step must take the secret from $OVO_NODE_SECRET, got %q", step)
	}
	if strings.Contains(step, `printf '{\n`) {
		t.Fatalf("config write step must not pass values as printf argv, got %q", step)
	}
	for _, c := range []string{
		buildNodeDockerInstallCommand(agentImageRef("1.9.9"), "wss://panel.example.com:443", false),
		buildNodeDockerComposeInstallCommand(agentImageRef("1.9.9"), "wss://panel.example.com:443", false),
	} {
		if strings.Contains(c, hostile) {
			t.Fatalf("container-form command must not contain the secret at all, got %q", c)
		}
	}
}

func TestB301ShellQuoteRoundTrip(t *testing.T) {
	if got := shellQuoteForInstall("plain"); got != "'plain'" {
		t.Fatalf("plain value: got %s", got)
	}
	if got := shellQuoteForInstall("it's"); got != `'it'\''s'` {
		t.Fatalf("embedded quote must be escaped: got %s", got)
	}
	if got := shellQuoteForInstall(""); got != "''" {
		t.Fatalf("empty value: got %s", got)
	}
}
