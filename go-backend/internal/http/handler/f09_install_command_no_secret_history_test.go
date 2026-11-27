package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F09（P2）：安装命令本身进节点 shell history。W8-R-C4ARGV 只把
// secret 从 argv 移到了环境变量赋值，但粘贴进 bash 的整条命令
// （含 OVO_NODE_SECRET='<secret>'）仍原样写入 ~/.bash_history，
// 而 secret 有效期是永久（F03 之前无轮换），history 里的任何一
// 份都长期有效。修复：生成的安装命令文本里不再出现 secret——
// 脚本形态去掉 OVO_NODE_SECRET 赋值，install.sh 在安装开始时
// 经 get_config_params 交互 read -r -s 读密钥（不回显、不进
// history）；容器形态在写 config.json 之前从 /dev/tty 交互读入
// （已预置 OVO_NODE_SECRET 环境变量时跳过提示，供自动化），写
// 完立即 unset。secret 只经 F03 回执的 secret 框一次显示。
//
// 先红后绿：修复前三形态命令文本都含 secret 明文。

const f09Secret = "0123456789abcdef0123456789abcdef"

func TestF09ScriptCommandHasNoSecret(t *testing.T) {
	cmd := buildNodeInstallCommand("1.9.4", "wss://panel.example.com:443")
	if strings.Contains(cmd, f09Secret) {
		t.Fatalf("script-form command must not contain the node secret:\n%s", cmd)
	}
	if strings.Contains(cmd, "OVO_NODE_SECRET") {
		t.Fatalf("script-form command must not reference OVO_NODE_SECRET:\n%s", cmd)
	}
	// 执行形态不变：版本赋值 + -a 地址 + 校验段仍在。
	for _, want := range []string{
		"VERSION=1.9.4 ./install.sh -a 'wss://panel.example.com:443'",
		"sha256sum ./install.sh",
		"chmod +x ./install.sh",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("script-form command missing %q:\n%s", want, cmd)
		}
	}
}

func TestF09DockerCommandReadsSecretFromTTY(t *testing.T) {
	t.Setenv(panelInstanceEnv, "")
	cmd := buildNodeDockerInstallCommand(agentImageRef("1.9.4"), "wss://panel.example.com:443", false)
	if strings.Contains(cmd, f09Secret) {
		t.Fatalf("docker-form command must not contain the node secret:\n%s", cmd)
	}
	// 交互读密钥：/dev/tty（粘贴时 stdin 是粘贴流，只有 /dev/tty
	// 才指向终端）、不回显；已预置环境变量时跳过提示。
	if !strings.Contains(cmd, "read -r -s OVO_NODE_SECRET < /dev/tty") {
		t.Fatalf("docker-form command must read the secret from /dev/tty:\n%s", cmd)
	}
	// 写配置只从变量取值，写完立即 unset，不在 shell 里滞留。
	if !strings.Contains(cmd, `OVO_AGENT_SECRET="$OVO_NODE_SECRET"`) {
		t.Fatalf("config write must take the secret from $OVO_NODE_SECRET:\n%s", cmd)
	}
	if !strings.Contains(cmd, "unset OVO_NODE_SECRET") {
		t.Fatalf("docker-form command must unset the secret after writing config:\n%s", cmd)
	}
	// 顺序：读密钥在写 config.json 之前，unset 在写配置之后、
	// 拉起容器之前。
	if i, j := strings.Index(cmd, "read -r -s OVO_NODE_SECRET"), strings.Index(cmd, "mkdir -p /etc/ovo_agent"); i < 0 || j < 0 || i > j {
		t.Fatalf("secret read must precede config write:\n%s", cmd)
	}
	if i, j := strings.Index(cmd, "unset OVO_NODE_SECRET"), strings.Index(cmd, "docker run"); i < 0 || j < 0 || i > j {
		t.Fatalf("secret unset must precede docker run:\n%s", cmd)
	}
}

func TestF09ComposeCommandReadsSecretFromTTY(t *testing.T) {
	t.Setenv(panelInstanceEnv, "")
	cmd := buildNodeDockerComposeInstallCommand(agentImageRef("1.9.4"), "wss://panel.example.com:443", false)
	if strings.Contains(cmd, f09Secret) {
		t.Fatalf("compose-form command must not contain the node secret:\n%s", cmd)
	}
	if !strings.Contains(cmd, "read -r -s OVO_NODE_SECRET < /dev/tty") {
		t.Fatalf("compose-form command must read the secret from /dev/tty:\n%s", cmd)
	}
	if !strings.Contains(cmd, `OVO_AGENT_SECRET="$OVO_NODE_SECRET"`) {
		t.Fatalf("config write must take the secret from $OVO_NODE_SECRET:\n%s", cmd)
	}
	if !strings.Contains(cmd, "unset OVO_NODE_SECRET") {
		t.Fatalf("compose-form command must unset the secret after writing config:\n%s", cmd)
	}
	if i, j := strings.Index(cmd, "read -r -s OVO_NODE_SECRET"), strings.Index(cmd, "mkdir -p /etc/ovo_agent"); i < 0 || j < 0 || i > j {
		t.Fatalf("secret read must precede config write:\n%s", cmd)
	}
	if i, j := strings.Index(cmd, "unset OVO_NODE_SECRET"), strings.Index(cmd, "docker compose -f "); i < 0 || j < 0 || i > j {
		t.Fatalf("secret unset must precede docker compose up:\n%s", cmd)
	}
	// compose 文件内仍不得出现 secret 取值（只引用变量名）。
	yamlStart := strings.Index(cmd, "services:")
	yamlEnd := strings.Index(cmd, "OVO_AGENT_COMPOSE_EOF\n")
	if yamlStart < 0 || yamlEnd < 0 || strings.Contains(cmd[yamlStart:yamlEnd], f09Secret) {
		t.Fatalf("compose yaml must not embed the node secret:\n%s", cmd)
	}
}

// 实跑：预置 OVO_NODE_SECRET 环境变量时，容器形态命令跳过
// /dev/tty 提示、照常写出 config.json（自动化路径）；未预置且
// 无 TTY 时，读密钥步失败、命令在写 config.json 之前中止，
// 不留带空 secret 的残留配置。
func TestF09DockerCommandFunctionalSecretFromEnv(t *testing.T) {
	t.Setenv(panelInstanceEnv, "")
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, cmd, dataDir string) error {
		t.Helper()
		cmd = strings.ReplaceAll(cmd, "/etc/ovo_agent", dataDir)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "sh", "-c", cmd)
		c.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		_, err := c.CombinedOutput()
		return err
	}

	// 预置环境变量：跳过提示，config.json 落盘正确。
	t.Setenv("OVO_NODE_SECRET", f09Secret)
	dataDir := t.TempDir()
	cmd := buildNodeDockerInstallCommand(agentImageRef("1.9.4"), "wss://panel.example.com:443", false)
	if err := run(t, cmd, dataDir); err != nil {
		t.Fatalf("preset-env docker command failed: %v\ncommand:\n%s", err, cmd)
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "config.json"))
	if err != nil || !strings.Contains(string(b), f09Secret) {
		t.Fatalf("config.json missing or wrong after preset-env run: %v %q", err, b)
	}

	// 未预置且无 TTY：读密钥失败即中止，config.json 不得落盘。
	t.Setenv("OVO_NODE_SECRET", "")
	dataDir2 := t.TempDir()
	if err := run(t, cmd, dataDir2); err == nil {
		t.Fatalf("docker command without TTY and without preset secret must fail at the secret prompt")
	}
	if _, err := os.Stat(filepath.Join(dataDir2, "config.json")); err == nil {
		t.Fatalf("failed secret read must not leave a config.json behind")
	}
}
