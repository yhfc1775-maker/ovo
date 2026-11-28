package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// --- 节点定时重启 agent 服务（保险丝） ---
//
// 面板是设置的权威源，经 "SetRestartSchedule" 命令下发
// {enabled, time, jitterMinutes}（interval 模式再加
// {mode, intervalMinutes}）；本文件把设置落成一对 systemd
// 单元（ovo-agent-restart.service + .timer）持久化执行：
//   - daily 模式：timer 的 OnCalendar 按节点本机时间每日触发；
//   - interval 模式：timer 以 OnBootSec＋OnUnitActiveSec＋
//     OnActiveSec 每 intervalMinutes 分钟触发——首次在开机/
//     启用后一个间隔，停机错过的拍不补、不成簇；
//   - OnActiveSec 是 interval 模式的再武装锚（T07）：仅
//     OnBootSec＋OnUnitActiveSec 时，timer 停启一次后
//     systemd 即将其置为 active (elapsed)、两路 next_elapse
//     均为 0，永不再触发——死锁的根因是 OnUnitActiveSec 的
//     下一拍只随被触发单元的激活重算，而激活它的唯一来源
//     就是该 timer 自身。OnActiveSec 以 timer 自身激活时刻
//     为锚，停启后必在一间隔内再触发一次，该次触发激活服
//     务、随即由 OnUnitActiveSec 接管稳态节拍；
//   - RandomizedDelaySec 承担随机抖动（两模式同口径，上限即
//     抖动窗口：实际触发在名义时刻后 0..jitter 内均匀随机）；
//   - service 是 oneshot，ExecStart 只做 systemctl restart 本机
//     agent 服务（安装布局名，见 agentServiceNameFor），不整机
//     重启、不依赖面板连接——面板离线、面板被墙都不影响触发；
//   - 单元由设置机械生成、幂等重写（内容不变时不重写、不
//     daemon-reload）；关开关即 disable --now 并删除两件单元。
//
// 校验边界与面板 validateRestartSchedule 逐字一致（面板判＋
// agent 复判）：时刻 "HH:MM"（00:00–23:59），抖动 0–720 分钟，
// interval 模式间隔 15–10080 分钟。旧面板不下发 mode 字段，
// 空 mode 按 daily 读，行为与引入模式前一致。

const (
	restartTimerUnitName            = "ovo-agent-restart.timer"
	restartServiceUnitName          = "ovo-agent-restart.service"
	restartReloadMarkerName         = "." + restartTimerUnitName + ".reload-pending"
	restartScheduleMaxJitterMinutes = 720
	restartModeDaily                = "daily"
	restartModeInterval             = "interval"
	restartIntervalMinMinutes       = 15
	restartIntervalMaxMinutes       = 10080
)

var restartScheduleUnitDir = "/etc/systemd/system"

// --- IL-NU-01：升级锁（timer 与升级的互斥） ---
//
// ovo-agent-restart.service 是无条件的 oneshot restart：升级
// 下载（最长 240s）或换版健康检查窗口内撞上 timer 触发，agent
// 会被中途重启——下载变幽灵升级、健康检查误判回滚。锁文件是
// 互斥点：
//   - 升级/回退 handler 在下载前建锁（内容为建锁时刻 Unix 秒），
//     重启脚本末尾删锁（脱离式脚本接管所有权；handler 失败路
//     径自行删锁）；
//   - timer service 的 ExecStart 先查锁：锁新鲜（30 分钟内）
//     时跳过本次触发并记日志，锁过期或内容非法时照常重启——
//     崩溃残留的锁不得永久压住保险丝；
//   - 锁在 /run（tmpfs）：机器重启即清零，永不跨启动残留。
//
// 包级变量形态供测试改道到临时目录。
var agentUpgradeTimerLockPath = "/run/ovo-agent-upgrade.lock"

// agentUpgradeTimerLockTTLSeconds 是锁的新鲜窗口：一次升级
// （下载 240s＋换版＋60s 回连门）远短于 30 分钟；超过即视为
// 残留锁，timer 照常触发。
const agentUpgradeTimerLockTTLSeconds = 1800

// agentUpgradeTimerUnlockSegment 是每条升级/回退重启脚本末尾
// 追加的删锁段：换版 verdict 走完即释放 timer，所有路径都流
// 到脚本末尾（自重启版的停服失败 exit 是唯一例外——该处旧进
// 程仍在正常运行，30 分钟 TTL 兜底恢复 timer）。
func agentUpgradeTimerUnlockSegment() string {
	return fmt.Sprintf("; rm -f %s 2>/dev/null", quoteShellPath(agentUpgradeTimerLockPath))
}

// acquireAgentUpgradeTimerLock 建升级锁（尽力而为：锁只抑制
// timer 守卫，写失败——如非 root 的 scratch agent 写不了
// /run——不得让升级本身失败，只记一行日志）。
func acquireAgentUpgradeTimerLock() {
	if err := os.WriteFile(agentUpgradeTimerLockPath, []byte(fmt.Sprintf("%d\n", time.Now().Unix())), 0o644); err != nil {
		fmt.Printf("⚠️ 写入升级锁失败（定时重启在升级期间可能仍会触发）: %v\n", err)
	}
}

// releaseAgentUpgradeTimerLock 删升级锁：handler 的失败路径在
// 返回前调用（脚本未启动、无接管方）；成功路径由脚本末尾删锁。
func releaseAgentUpgradeTimerLock() {
	_ = os.Remove(agentUpgradeTimerLockPath)
}

var restartTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// restartScheduleSetting is the wire shape of the panel's
// SetRestartSchedule payload (field names are the contract — do
// not rename). Mode/IntervalMinutes are absent from daily-mode
// payloads and from pre-mode panels entirely.
type restartScheduleSetting struct {
	Enabled         bool   `json:"enabled"`
	Time            string `json:"time"`
	JitterMinutes   int    `json:"jitterMinutes"`
	Mode            string `json:"mode"`
	IntervalMinutes int    `json:"intervalMinutes"`
}

// intervalMode reports whether the setting selects the interval
// cadence; anything but the explicit "interval" value (including
// the empty pre-mode value) is the daily cadence.
func (s restartScheduleSetting) intervalMode() bool {
	return s.Mode == restartModeInterval
}

func validateRestartScheduleSetting(s restartScheduleSetting) error {
	if s.Mode != "" && s.Mode != restartModeDaily && s.Mode != restartModeInterval {
		return fmt.Errorf("重启模式须为 daily 或 interval: %q", s.Mode)
	}
	if !restartTimePattern.MatchString(s.Time) {
		return fmt.Errorf("重启时刻须为 HH:MM 格式（00:00–23:59）: %q", s.Time)
	}
	if s.JitterMinutes < 0 || s.JitterMinutes > restartScheduleMaxJitterMinutes {
		return fmt.Errorf("随机抖动须在 0–%d 分钟之间: %d", restartScheduleMaxJitterMinutes, s.JitterMinutes)
	}
	if s.intervalMode() &&
		(s.IntervalMinutes < restartIntervalMinMinutes || s.IntervalMinutes > restartIntervalMaxMinutes) {
		return fmt.Errorf("重启间隔须在 %d–%d 分钟之间: %d", restartIntervalMinMinutes, restartIntervalMaxMinutes, s.IntervalMinutes)
	}
	return nil
}

func agentRestartGuard(agentServiceName string) string {
	return fmt.Sprintf(`if [ -f %s ]; then _lu=$(cat %s 2>/dev/null); [ -z "$_lu" ] && _lu=0; case "$_lu" in *[!0-9]*) _lu=0;; esac; if [ $(( $(date +%%s) - _lu )) -lt %d ]; then echo "ovo-agent-restart: agent upgrade in progress, skipping scheduled restart"; exit 0; fi; fi; exec /bin/systemctl restart %s`,
		agentUpgradeTimerLockPath, agentUpgradeTimerLockPath, agentUpgradeTimerLockTTLSeconds, agentServiceName)
}

// renderRestartServiceUnit renders the oneshot that restarts only
// the agent service of this install layout. IL-NU-01: ExecStart
// 前置升级锁检查——锁新鲜（30 分钟内）时跳过本次触发并记日
// 志，锁过期或内容非法时照常重启。
func renderRestartServiceUnit(agentServiceName string) string {
	// IL-NU-01b: the guard must contain no inner single quotes: the
	// -c argument is itself wrapped in single quotes in the unit
	// file, and systemd's ExecStart parser splits on them — the old
	// '' empty case branch (and the single-quoted echo) chopped the
	// case statement apart and /bin/sh died with "Syntax error: word
	// unexpected" (exit 2, Lightsail). The empty value is handled
	// with an explicit -z test instead. The date format is %%%%s in
	// this Go format string so the rendered unit carries %%s, which
	// systemd unescapes to % before exec (a bare %s would be a
	// systemd specifier).
	guard := strings.ReplaceAll(agentRestartGuard(agentServiceName), "%", "%%")
	return "[Unit]\n" +
		"Description=OVO scheduled agent restart (panel-set fuse; service only, not a host reboot)\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=/bin/sh -c '" + guard + "'\n"
}

// renderRestartTimerUnit renders the timer for the setting's
// mode. The jitter is a systemd RandomizedDelaySec window: the
// trigger fires uniformly at random between the nominal time and
// that time plus the window. A zero window omits the line
// entirely.
//
// Daily mode keeps the exact pre-mode shape (OnCalendar).
// Interval mode uses monotonic triggers: OnBootSec arms a fire
// one interval after boot, OnUnitActiveSec re-arms one interval
// after each activation of the restarted service unit, and
// OnActiveSec re-arms one interval after each (re-)activation
// of the timer unit itself — the T07 deadlock guard (see the
// file header). Monotonic triggers never fire retroactively,
// so downtime produces no catch-up burst.
func renderRestartTimerUnit(s restartScheduleSetting) string {
	body := "[Unit]\n" +
		"Description=OVO scheduled agent restart timer\n\n" +
		"[Timer]\n"
	if s.intervalMode() {
		body += fmt.Sprintf("OnBootSec=%dmin\n", s.IntervalMinutes) +
			fmt.Sprintf("OnUnitActiveSec=%dmin\n", s.IntervalMinutes) +
			fmt.Sprintf("OnActiveSec=%dmin\n", s.IntervalMinutes)
	} else {
		body += "OnCalendar=*-*-* " + s.Time + ":00\n"
	}
	if s.JitterMinutes > 0 {
		body += fmt.Sprintf("RandomizedDelaySec=%d\n", s.JitterMinutes*60)
	}
	body += "Persistent=true\n" +
		"Unit=" + restartServiceUnitName + "\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
	return body
}

// writeUnitIfChanged writes body to path unless the file already
// holds exactly it; it reports whether anything changed.
func writeUnitIfChanged(path, body string) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// applyRestartSchedule installs or removes the timer pair per the
// setting. It is the production entry used by the command handler;
// the unit directory and systemctl go through the package seams
// (restartScheduleUnitDir, tuneCommand) so tests can drive it
// against a scratch directory.
func applyRestartSchedule(ctx context.Context, s restartScheduleSetting) error {
	if err := validateRestartScheduleSetting(s); err != nil {
		return err
	}
	timerPath := filepath.Join(restartScheduleUnitDir, restartTimerUnitName)
	servicePath := filepath.Join(restartScheduleUnitDir, restartServiceUnitName)
	reloadPath := filepath.Join(restartScheduleUnitDir, restartReloadMarkerName)

	if !s.Enabled {
		// 关闭：停用并删除两件单元。无 systemd 的环境里不可能装过
		// timer，直接成功返回（不让关开关这条路被环境卡住）。
		if InContainer() || !commandExists("systemctl") {
			return nil
		}
		if _, err := tuneCommand(ctx, "systemctl", "disable", "--now", restartTimerUnitName); err != nil {
			// A failed stop may leave the timer armed. Keep its units for
			// retry rather than reporting a successful disable. An absent
			// timer is safe only when systemd also confirms it is inactive;
			// its leftover companion service can then be cleaned on retry.
			_, timerErr := os.Stat(timerPath)
			absent := false
			if os.IsNotExist(timerErr) {
				state, _ := tuneCommand(ctx, "systemctl", "show", "--property=LoadState", "--property=ActiveState", restartTimerUnitName)
				state = "\n" + strings.TrimSpace(state) + "\n"
				absent = strings.Contains(state, "\nLoadState=not-found\n") && strings.Contains(state, "\nActiveState=inactive\n")
			}
			if !absent {
				return fmt.Errorf("停用定时重启 timer 失败: %w", err)
			}
		}
		removed := false
		for _, p := range []string{timerPath, servicePath} {
			if err := os.Remove(p); err == nil {
				removed = true
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("删除定时重启单元失败 %s: %w", p, err)
			}
		}
		if err := os.Remove(reloadPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理定时重启 reload 状态失败: %w", err)
		}
		if removed {
			if _, err := tuneCommand(ctx, "systemctl", "daemon-reload"); err != nil {
				return fmt.Errorf("systemctl daemon-reload 失败: %w", err)
			}
		}
		return nil
	}

	if InContainer() || !commandExists("systemctl") {
		return fmt.Errorf("节点无 systemd，定时重启无法持久化（容器形态不支持）")
	}
	binaryPath, err := resolveAgentBinaryPath()
	if err != nil {
		return err
	}
	// F-R2B-V2-05: the timer restarts the layout service by name —
	// only a service-managed install may arm it. A self-managed
	// agent has no service of its own; arming the timer would
	// restart another install's service on schedule.
	if !agentInstallServiceManaged(binaryPath) {
		return fmt.Errorf("定时重启需要 agent 运行在服务管理下（/etc/ovo_agent 或 /etc/flux_agent 安装），当前为手动运行的安装，无法定时重启")
	}
	agentService := agentServiceNameFor(binaryPath)
	changed := false
	// Fixed order (R18-D2-08): service first, then timer. The
	// old map iteration wrote them in random order, so a failed
	// second write could leave a half-installed pair behind; the
	// file the first write actually created is removed again when
	// the second write fails (a pre-existing identical file is
	// left alone — writeUnitIfChanged reports it as unchanged).
	// R18-R2-D2-04: a pre-existing service unit with different
	// content (a previous version's rendering, or another agent
	// service name) is NOT removed — it is restored byte for byte,
	// so a failed setting change cannot dismantle a previously
	// working schedule. Only a service file this apply created
	// (no pre-existing file) may be removed again.
	var prevService []byte
	serviceExisted := false
	if cur, err := os.ReadFile(servicePath); err == nil {
		prevService = cur
		serviceExisted = true
	}
	serviceBody := renderRestartServiceUnit(agentService)
	timerBody := renderRestartTimerUnit(s)
	prevTimer, timerReadErr := os.ReadFile(timerPath)
	if !serviceExisted || string(prevService) != serviceBody || timerReadErr != nil || string(prevTimer) != timerBody {
		// Persist the reload obligation before touching either unit. If
		// daemon-reload fails, an identical retry (even after agent
		// restart) must not skip it merely because the files now match.
		if _, err := writeUnitIfChanged(reloadPath, "pending\n"); err != nil {
			return fmt.Errorf("记录定时重启 reload 状态失败: %w", err)
		}
	}
	svcChanged, err := writeUnitIfChanged(servicePath, serviceBody)
	if err != nil {
		return fmt.Errorf("写入定时重启单元失败 %s: %w", servicePath, err)
	}
	changed = changed || svcChanged
	timerChanged, err := writeUnitIfChanged(timerPath, timerBody)
	if err != nil {
		if svcChanged {
			if serviceExisted {
				// Best effort: the apply already fails loudly;
				// a failed restore leaves the half-installed
				// pair, same as before, and is retried on the
				// next apply.
				_ = writeFileAtomic(servicePath, prevService, 0o644)
			} else {
				_ = os.Remove(servicePath)
			}
		}
		return fmt.Errorf("写入定时重启单元失败 %s: %w", timerPath, err)
	}
	changed = changed || timerChanged
	if _, err := os.Stat(reloadPath); err == nil {
		changed = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取定时重启 reload 状态失败: %w", err)
	}
	if changed {
		if _, err := tuneCommand(ctx, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload 失败: %w", err)
		}
		if err := os.Remove(reloadPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理定时重启 reload 状态失败: %w", err)
		}
	} else {
		// IL-NU-01：单元内容无变化时不再 enable --now。重连
		// 补发与显式保存走同一条命令；无变化的重推若每次都
		// enable --now，节点上手工停用的 timer 会在每次重连
		// 时被复活（Lightsail 教训）。设置真变更时单元必变，
		// 仍正常 enable；首次下发单元必新建，同样不受影响。
		return nil
	}
	if _, err := tuneCommand(ctx, "systemctl", "enable", "--now", restartTimerUnitName); err != nil {
		return fmt.Errorf("启用定时重启 timer 失败: %w", err)
	}
	return nil
}

// handleSetRestartSchedule handles the panel's SetRestartSchedule
// command: validate (again — the panel already judged), then apply.
// Applying is serialized with the other mutation commands by the
// dispatch layer (isMutationCommand), and the operation itself is
// idempotent, so a reconnect re-push of the same setting is a no-op.
func (w *WebSocketReporter) handleSetRestartSchedule(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化定时重启设置失败: %v", err)
	}
	var s restartScheduleSetting
	if err := json.Unmarshal(jsonData, &s); err != nil {
		return fmt.Errorf("解析定时重启设置失败: %v", err)
	}
	if err := validateRestartScheduleSetting(s); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return applyRestartSchedule(ctx, s)
}

// A delayed transient oneshot survives the agent stopping itself. It uses the
// same layout-derived service name and upgrade-lock guard as scheduled restart.
var immediateRestartUntil atomic.Int64

func agentImmediateRestartPending() bool { return time.Now().Unix() < immediateRestartUntil.Load() }

func refuseDuringAgentUpgrade() error {
	raw, err := os.ReadFile(agentUpgradeTimerLockPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("无法读取升级锁，拒绝操作: %w", err)
	}
	if stamp, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && time.Now().Unix()-stamp < agentUpgradeTimerLockTTLSeconds {
		return fmt.Errorf("agent 升级或回退正在执行，请等待其完成")
	}
	return nil
}

func scheduleImmediateAgentRestart(ctx context.Context) error {
	if upgradeInContainer() || !commandExists("systemctl") || !commandExists("systemd-run") {
		return fmt.Errorf("立即重启需要 systemd（systemctl 和 systemd-run），容器形态不支持")
	}
	binaryPath, err := resolveAgentBinaryPath()
	if err != nil {
		return err
	}
	if !agentInstallServiceManaged(binaryPath) {
		return fmt.Errorf("立即重启需要服务管理下的 /etc/ovo_agent 或 /etc/flux_agent 安装；当前为手动运行，无法立即重启")
	}
	if err := refuseDuringAgentUpgrade(); err != nil {
		return err
	}
	if agentImmediateRestartPending() {
		return fmt.Errorf("agent 正在重启，请等待重连")
	}
	// Reserve until either the new process starts (fresh memory), or the bounded
	// verification window expires. Upgrades arriving during the 2s delay refuse.
	immediateRestartUntil.Store(time.Now().Add(2 * time.Minute).Unix())
	service := agentServiceNameFor(binaryPath)
	command := agentRestartGuard(service)
	_, err = tuneCommand(ctx, "systemd-run", "--unit=ovo-agent-restart-now-"+strconv.FormatInt(time.Now().UnixNano(), 10), "--on-active=2s", "--collect", "/bin/sh", "-c", command)
	if err != nil {
		immediateRestartUntil.Store(0)
		return fmt.Errorf("创建立即重启任务失败: %w", err)
	}
	return nil
}

// Keep download/preflight failures next to restart-script output, with the
// existing 1MiB cap. Never log payloads, URLs, PSKs or request credentials.
func appendAgentUpgradeEvent(message string) {
	binaryPath, err := resolveAgentBinaryPath()
	if err != nil {
		return
	}
	path := filepath.Join(filepath.Dir(binaryPath), "agent-upgrade.log")
	if st, err := os.Stat(path); err == nil && st.Size() > agentUpgradeLogMaxBytes {
		_ = os.Truncate(path, 0)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	message = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, message)
	// Download errors can contain remote URLs. Preserve the category locally;
	// detailed transport errors remain in the correlated command response.
	if strings.Contains(message, "http://") || strings.Contains(message, "https://") {
		message = "upgrade failed: download/HTTP error (see agent journal and panel command receipt)"
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), message)
}
