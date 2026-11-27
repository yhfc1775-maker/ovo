package socket

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// IL-NU-01：定时重启 timer 可在升级中途重启 agent，升级脚本对
// 它毫无防备；且 onNodeOnline 每次重连都按面板 DB 存量重新
// enable --now timer，节点上手工停 timer 不是持久状态（Lightsail
// 教训的代码根因）。修复（三件）：
//   - timer service 的 ExecStart 前置升级锁检查：锁新鲜（30 分
//     钟内）时跳过本次触发并记日志，锁过期/无锁时照常重启；
//   - 升级/回退 handler 入口建锁、重启脚本末尾删锁（脱离式脚
//     本接管锁的所有权；handler 失败路径自行删锁）；
//   - applyRestartSchedule 在单元内容无变化时不再 enable --now
//     （重连补发不得复活一个被手工停用的 timer；设置真变更时
//     单元必变，仍正常 enable）。

func TestILNU01RestartServiceUnitSkipsRestartDuringUpgrade(t *testing.T) {
	body := renderRestartServiceUnit("ovo_agent")
	if !strings.Contains(body, agentUpgradeTimerLockPath) {
		t.Fatalf("service unit must check the upgrade lock before restarting:\n%s", body)
	}
	if !strings.Contains(body, "skipping scheduled restart") {
		t.Fatalf("service unit must log the skip:\n%s", body)
	}
	if !strings.Contains(body, "systemctl restart ovo_agent") {
		t.Fatalf("service unit must still restart the agent service when no upgrade is in progress:\n%s", body)
	}
	if !strings.Contains(body, "1800") {
		t.Fatalf("lock freshness window must be bounded (stale locks must not suppress the fuse forever):\n%s", body)
	}
}

func TestILNU01RestartScriptsRemoveUpgradeLock(t *testing.T) {
	unlock := agentUpgradeTimerUnlockSegment()
	if !strings.Contains(unlock, agentUpgradeTimerLockPath) {
		t.Fatalf("unlock segment must name the lock path: %q", unlock)
	}
	upgrade, err := buildAgentRestartScriptForCap(serviceManagerSystemd, "/etc/ovo_agent/ovo_agent.new", "/etc/ovo_agent/ovo_agent", "/etc/ovo_agent/ovo_agent.old", agentTargetMarkerCapable, "2.2.0")
	if err != nil {
		t.Fatalf("build upgrade script: %v", err)
	}
	if !strings.Contains(upgrade, unlock) {
		t.Fatalf("upgrade restart script must remove the upgrade lock at the end:\n%s", upgrade)
	}
	self, err := buildAgentRestartScriptSelfCap("/root/scratch/.ovo_agent.upgrade-1", "/root/scratch/ovo_agent", "/root/scratch/ovo_agent.old", selfCtxForTest(), agentTargetMarkerCapable, "2.2.0")
	if err != nil {
		t.Fatalf("build self upgrade script: %v", err)
	}
	if !strings.Contains(self, unlock) {
		t.Fatalf("self upgrade restart script must remove the upgrade lock at the end:\n%s", self)
	}
	rollback, err := buildAgentRollbackScriptForCap(serviceManagerSystemd, "/etc/ovo_agent/ovo_agent.old", "/etc/ovo_agent/ovo_agent", agentTargetMarkerCapable, "2.2.0")
	if err != nil {
		t.Fatalf("build rollback script: %v", err)
	}
	if !strings.Contains(rollback, unlock) {
		t.Fatalf("rollback script must remove the upgrade lock at the end:\n%s", rollback)
	}
	rollbackSelf, err := buildAgentRollbackScriptSelfCap("/root/scratch/ovo_agent.old", "/root/scratch/ovo_agent", selfCtxForTest(), agentTargetMarkerCapable, "2.2.0")
	if err != nil {
		t.Fatalf("build self rollback script: %v", err)
	}
	if !strings.Contains(rollbackSelf, unlock) {
		t.Fatalf("self rollback script must remove the upgrade lock at the end:\n%s", rollbackSelf)
	}
}

func TestILNU01UpgradeTimerLockAcquireRelease(t *testing.T) {
	oldPath := agentUpgradeTimerLockPath
	agentUpgradeTimerLockPath = filepath.Join(t.TempDir(), "ovo-agent-upgrade.lock")
	t.Cleanup(func() { agentUpgradeTimerLockPath = oldPath })

	acquireAgentUpgradeTimerLock()
	raw, err := os.ReadFile(agentUpgradeTimerLockPath)
	if err != nil {
		t.Fatalf("lock file must exist after acquire: %v", err)
	}
	// 内容是创建时刻的 Unix 秒：与现在相差数秒内。
	written, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		t.Fatalf("lock content must be a unix epoch, got %q: %v", raw, err)
	}
	if now := time.Now().Unix(); written < now-10 || written > now+10 {
		t.Fatalf("lock epoch %d is not near now %d", written, now)
	}
	releaseAgentUpgradeTimerLock()
	if _, err := os.Stat(agentUpgradeTimerLockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file must be gone after release")
	}
}

func TestILNU01ApplyRestartScheduleSkipsEnableWhenUnchanged(t *testing.T) {
	calls := stubRestartScheduleEnv(t)
	ctx := context.Background()
	s := restartScheduleSetting{Enabled: true, Time: "03:15", JitterMinutes: 45}

	if err := applyRestartSchedule(ctx, s); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if countCall(*calls, "enable --now "+restartTimerUnitName) != 1 {
		t.Fatalf("first apply must enable once, got %v", *calls)
	}
	// 同设置重推（面板重连补发）：单元无变化时不得再次
	// enable --now——否则节点上手工停用的 timer 会在每次重连
	// 时被复活（Lightsail 教训）。
	*calls = nil
	if err := applyRestartSchedule(ctx, s); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if countCall(*calls, "enable --now "+restartTimerUnitName) != 0 {
		t.Fatalf("unchanged re-apply must not re-enable the timer, got %v", *calls)
	}
	if countCall(*calls, "daemon-reload") != 0 {
		t.Fatalf("unchanged re-apply must not daemon-reload, got %v", *calls)
	}
	// 设置真变更时仍正常 enable（单元必变）。
	*calls = nil
	if err := applyRestartSchedule(ctx, restartScheduleSetting{Enabled: true, Time: "05:00", JitterMinutes: 0}); err != nil {
		t.Fatalf("change: %v", err)
	}
	if countCall(*calls, "enable --now "+restartTimerUnitName) != 1 {
		t.Fatalf("changed setting must enable again, got %v", *calls)
	}
}
