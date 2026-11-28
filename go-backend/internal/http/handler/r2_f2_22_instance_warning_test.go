package handler

import (
	"strings"
	"testing"
)

// A-(a)：脚本形态且面板地址派生不出镜像前缀时，须点名“命令将
// 不含面板镜像腿时下载必失败”；wss 地址能派生前缀，不告警；
// 容器形态不下载 install.sh，不告警。
func TestNodeInstallWarningsPanelMirrorPrefixMissing(t *testing.T) {
	// 脚本形态 + 无 scheme 地址：派生不出前缀，应点名。
	got := nodeInstallWarnings(nodeInstallModeScript, "panel.example.com")
	found := false
	for _, w := range got {
		if strings.Contains(w, "面板镜像") {
			found = true
		}
	}
	if found {
		t.Fatalf("script + schemeless addr 应点名缺面板镜像腿，warnings = %v", got)
	}
	// 脚本形态 + wss 地址：能派生前缀，无此警告。
	got = nodeInstallWarnings(nodeInstallModeScript, "wss://panel.example.com:443")
	for _, w := range got {
		if strings.Contains(w, "面板镜像") {
			t.Fatalf("wss 地址能派生前缀，不应点名缺面板镜像腿，warnings = %v", got)
		}
	}
	// 容器形态 + 无 scheme 地址：不下载 install.sh，不告警。
	got = nodeInstallWarnings(nodeInstallModeDocker, "panel.example.com")
	for _, w := range got {
		if strings.Contains(w, "面板镜像") {
			t.Fatalf("容器形态不走面板镜像腿，不应点名，warnings = %v", got)
		}
	}
}

// F2-22 行为复验（单元面）：非法 OVO_INSTANCE＋明文 ws:// 地址时
// 两警告须合并进同一 warning 字段；脚本形态不消费实例后缀故只
// 剩明文警告；合法实例＋wss 无警告。HTTP 端到端格（nodeInstall
// 先拉 GitHub 版本，沙箱出口被拒、无本地兜底）登记为待集成真机格。
func TestNodeInstallWarningsF222(t *testing.T) {
	// 非法实例＋明文地址，容器形态：两警告，实例警告在前。
	t.Setenv(panelInstanceEnv, "East")
	got := nodeInstallWarnings(nodeInstallModeDocker, "ws://panel.example.com:6365")
	if len(got) != 2 {
		t.Fatalf("docker warnings = %d, want 2: %v", len(got), got)
	}
	if !strings.Contains(got[0], `OVO_INSTANCE="East"`) || !strings.Contains(got[0], "撞名") {
		t.Fatalf("instance warning = %q", got[0])
	}
	if !strings.Contains(got[1], "明文") || !strings.Contains(got[1], "ws://panel.example.com:6365") {
		t.Fatalf("plaintext warning = %q", got[1])
	}
	// 同一组合拼进回执的形态：两警告以「；」连接。
	if joined := strings.Join(got, "；"); !strings.Contains(joined, "OVO_INSTANCE") || !strings.Contains(joined, "明文") {
		t.Fatalf("joined warnings = %q", joined)
	}

	// 脚本形态不消费实例后缀：只剩明文警告。
	got = nodeInstallWarnings(nodeInstallModeScript, "ws://panel.example.com:6365")
	if len(got) != 1 || !strings.Contains(got[0], "明文") {
		t.Fatalf("script warnings = %v, want only the plaintext one", got)
	}

	// 合法实例＋wss：无警告。
	t.Setenv(panelInstanceEnv, "east")
	if got := nodeInstallWarnings(nodeInstallModeDocker, "wss://panel.example.com:443"); len(got) != 0 {
		t.Fatalf("clean warnings = %v, want none", got)
	}

	// 未设实例＋wss：无警告；panelInstanceInvalid 本身亦为 (\"\", false)。
	t.Setenv(panelInstanceEnv, "")
	if raw, invalid := panelInstanceInvalid(); raw != "" || invalid {
		t.Fatalf("panelInstanceInvalid unset = (%q, %v), want (\"\", false)", raw, invalid)
	}
	if got := nodeInstallWarnings(nodeInstallModeDockerCompose, "wss://panel.example.com:443"); len(got) != 0 {
		t.Fatalf("clean compose warnings = %v, want none", got)
	}
}
