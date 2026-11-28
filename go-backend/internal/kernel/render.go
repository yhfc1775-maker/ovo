package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// File is one configuration file the agent writes before (re)starting a
// kernel. Name is relative to the kernel's run directory.
type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Process is one supervised process; Bin is "" for the kernel's own binary
// or the name of an extra binary.
type Process struct {
	Name string   `json:"name"`
	Bin  string   `json:"bin"`
	Args []string `json:"args"`
}

// Rendered is everything the agent needs to run one kernel.
type Rendered struct {
	Files     []File    `json:"files"`
	Args      []string  `json:"args"`
	Processes []Process `json:"processes,omitempty"`
	// Idle means there is nothing to run (a per-inbound kernel without
	// inbounds); the kernel is applied disabled.
	Idle bool `json:"idle,omitempty"`
	// UserMarks lists the users whose outgoing traffic carries UserMark, so
	// the agent can count it per user.
	UserMarks []int64 `json:"userMarks,omitempty"`
	Hash      string  `json:"hash"`
}

// RenderOptions are panel settings a kernel config depends on.
type RenderOptions struct {
	// RuleSetMirror prefixes the GitHub URLs sing-box downloads 分流 rule
	// sets from ("" downloads directly).
	RuleSetMirror string
	// SingboxHTTPClient defines the HTTP client rule sets download with
	// (sing-box 1.14+; older versions reject the field, newer ones drop
	// the implicit default in 1.16).
	SingboxHTTPClient bool
	// SingboxSnell lets sing-box dial Snell targets (chained outbounds and
	// 分流 targets); sing-box 1.14+.
	SingboxSnell bool
	// SingboxNaive lets sing-box dial Naive targets (chained outbounds and
	// 分流 targets); the cronet-based naive outbound exists only in
	// sing-box 1.13+ (R9X-005).
	SingboxNaive bool
	// ConnLogEnabled is the connection-log node gate (master
	// connlog_enabled AND the node's conn_log_enabled switch,
	// default off): only when it is on does the kernel log shape
	// rise (sing-box debug level, Xray access log). Off means the
	// render falls back to the plain log shape — otherwise debug
	// chatter floods the troubleshooting ring while collection
	// itself is off (D-04). Fail-closed default: false.
	ConnLogEnabled bool
	// UserMarkBase is the socket-mark base user marks are minted
	// with (F2-DBL-01: per-agent base, so co-located agents count
	// only their own kernels' traffic). 0 means the legacy
	// host-global base.
	UserMarkBase int64
	// Companion is set by RenderWith when the kernel being rendered
	// is the companion instance (TypeSingboxCompanion): only the
	// companion exposes the Clash API (C-(4) selective kill).
	Companion bool
	// ClashAPIPort is the companion's Clash API listen port (R2C-01:
	// per-agent, derived from the node secret — see
	// ClashAPIPortForSecret). 0 means not configured: the render
	// omits the Clash API block and the agent falls back to a config
	// reload for user removals (metering unaffected).
	ClashAPIPort int
	// KernelVersion is the node's kernel version string ("" = unknown):
	// RenderWith enforces the in-place SS multi-user version gates
	// against it (see CheckSSInplaceUsersVersion). "" and "latest" are
	// exempt (no version context — the same contract as the save-time
	// gates); a concrete version below the minimum fails the render.
	KernelVersion string
}

// Render produces the kernel configuration for the given enabled inbounds.
// Inbounds must already be normalized.
func Render(kernelType string, inbounds []Inbound) (Rendered, error) {
	return RenderWith(kernelType, inbounds, RenderOptions{})
}

// RenderWith is Render with panel settings.
func RenderWith(kernelType string, inbounds []Inbound, opts RenderOptions) (Rendered, error) {
	sorted := make([]Inbound, len(inbounds))
	copy(sorted, inbounds)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	// F2-DBL-01: the per-agent mark base rides on every inbound so
	// the chain/split renderers (which take no RenderOptions) mint
	// the same marks as the direct outbounds. 0 keeps the legacy
	// host-global base.
	for i := range sorted {
		sorted[i].UserMarkBase = opts.UserMarkBase
	}
	// C-(4): only the companion instance exposes the Clash API
	// (selective per-user connection kill); the main sing-box
	// config is unchanged. OR (not assign): the handler sets
	// Companion from the raw kernel type, because by the time
	// RenderWith runs the type has been folded to sing-box by
	// EffectiveType; a direct caller passing the raw companion
	// type sets it here instead.
	opts.Companion = opts.Companion || kernelType == TypeSingboxCompanion
	// In-place SS version gates: an origin with the switch on renders
	// per-user credentials its kernel may not understand; a concrete
	// version below the minimum fails the render instead of shipping
	// a config that silently drops (or misattributes) user traffic.
	// The save-time gate is the first line: kernelVersionGate calls
	// CheckSSInplaceUsersVersion, and the switch reaches it from the
	// stored row — inboundFromModel maps it at apply/install time,
	// and inboundSave supplies it from the row after the F1 gate at
	// save time (the validation-shaped inbound itself never carries
	// the switch). This render check is the backstop for rows that
	// reach the render through any other path, and it only bites when
	// the caller passes the real version in RenderOptions.KernelVersion
	// (buildKernelApplyPayload does; "" / "latest" stay exempt).
	for _, in := range sorted {
		if in.Protocol == ProtocolShadowsocks && in.InplaceUsers {
			if err := CheckSSInplaceUsersVersion(kernelType, opts.KernelVersion, in.Settings.Method); err != nil {
				return Rendered{}, fmt.Errorf("入站 %d: %v", in.ID, err)
			}
		}
	}
	// 渲染期复检（与保存期 normalizeExtra 同口径）：指定出站 IP 策略的
	// 入站，其 Extra 不得覆盖 tag/detour（S4）。
	for _, in := range sorted {
		if err := ValidateOutboundIPExtra(kernelType, in.OutboundIPStrategy, in.Extra); err != nil {
			return Rendered{}, err
		}
	}

	var out Rendered
	switch kernelType {
	case TypeXray:
		content, err := renderXray(sorted, opts)
		if err != nil {
			return Rendered{}, err
		}
		out.Files = []File{{Name: "config.json", Content: content}}
		out.Args = []string{"run", "-c", "{RUN_DIR}/config.json"}
	case TypeSingbox, TypeSingboxCompanion:
		content, err := renderSingbox(sorted, opts)
		if err != nil {
			return Rendered{}, err
		}
		out.Files = []File{{Name: "config.json", Content: content}}
		out.Args = []string{"run", "-c", "{RUN_DIR}/config.json"}
	default:
		return Rendered{}, fmt.Errorf("暂不支持内核 %s", kernelType)
	}
	out.UserMarks = markedUsers(sorted)
	out.Hash = hashRendered(out)
	return out, nil
}

func hashRendered(r Rendered) string {
	h := sha256.New()
	for _, f := range r.Files {
		h.Write([]byte(f.Name))
		h.Write([]byte{0})
		h.Write([]byte(f.Content))
		h.Write([]byte{0})
	}
	for _, a := range r.Args {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	for _, p := range r.Processes {
		h.Write([]byte(p.Name + "\x00" + p.Bin + "\x00" + strings.Join(p.Args, "\x00")))
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func marshalConfig(v interface{}) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// extraProtectedTopKeys 是高级配置（Extra）不得覆盖的顶层键：这些字段是
// 面板在保存期校验过的不变量（端口归属、监听、tag 引用、凭据集合、安全层），
// 被 Extra 改掉会造成「面板显示正常、实际监听/安全层/凭据全变」的隐性不一致
// （F-022）。集合取 Xray 与 sing-box 两种入站对象的并集，两边都不会误伤：
// 预期用途只有 sniffing、streamSettings.sockopt 这类增量项。
var extraProtectedTopKeys = map[string]bool{
	"tag": true, "listen": true, "listen_port": true, "port": true,
	"protocol": true, "type": true,
	"settings": true, "users": true, "tls": true, "transport": true,
	"method": true, "password": true,
	// 凭据/形态类键名（psk、mode 等）亦在并集内（Xray 对象无同名顶层键）。
	"psk": true, "mode": true,
	"detour": true, "handshake": true, "version": true,
}

// extraProtectedStreamSubKeys 是 streamSettings 下同样受保护的子键
// （安全层与传输形态）；sockopt 等增量子键不在其列。
var extraProtectedStreamSubKeys = map[string]bool{
	"security": true, "tlsSettings": true, "realitySettings": true,
	"network":    true,
	"wsSettings": true, "grpcSettings": true, "xhttpSettings": true,
	"splithttpSettings": true, "tcpSettings": true, "kcpSettings": true,
	"quicSettings": true, "httpupgradeSettings": true,
}

// validateExtraProtectedKeys 拒绝含受保护键的 Extra（保存期与渲染期共用）。
func validateExtraProtectedKeys(obj map[string]interface{}) error {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if protectedExtraName(extraProtectedTopKeys, k) {
			return fmt.Errorf("高级配置不能覆盖受保护字段 %q", k)
		}
		if strings.EqualFold(k, "streamSettings") {
			if k != "streamSettings" {
				return fmt.Errorf("高级配置不能用别名覆盖受保护字段 %q", k)
			}
			sub, ok := obj[k].(map[string]interface{})
			if !ok {
				return fmt.Errorf("高级配置不能替换受保护字段 %q 的对象容器", k)
			}
			subKeys := make([]string, 0, len(sub))
			for sk := range sub {
				subKeys = append(subKeys, sk)
			}
			sort.Strings(subKeys)
			for _, sk := range subKeys {
				if protectedExtraName(extraProtectedStreamSubKeys, sk) {
					return fmt.Errorf("高级配置不能覆盖受保护字段 %q", "streamSettings."+sk)
				}
			}
			for sk, value := range sub {
				if !strings.EqualFold(sk, "sockopt") {
					continue
				}
				if sk != "sockopt" {
					return fmt.Errorf("高级配置不能用别名覆盖受保护字段 %q", "streamSettings."+sk)
				}
				if socket, ok := value.(map[string]interface{}); ok {
					for key := range socket {
						if strings.EqualFold(key, "acceptProxyProtocol") {
							return fmt.Errorf("高级配置不能覆盖受保护字段 %q，请使用入站的 PROXY protocol 开关", "streamSettings.sockopt."+key)
						}
					}
				}
			}
		}
	}
	return nil
}

// mergeExtra deep-merges a user supplied JSON object into dst. Objects merge
// recursively; any other value replaces the default.
func mergeExtra(dst map[string]interface{}, extra json.RawMessage) error {
	if len(extra) == 0 {
		return nil
	}
	var src map[string]interface{}
	if err := json.Unmarshal(extra, &src); err != nil {
		return fmt.Errorf("高级配置解析失败: %v", err)
	}
	// F-022: 渲染期兜底（存量/导入行可能绕过 Normalize 的保存期校验）。
	if err := validateExtraProtectedKeys(src); err != nil {
		return err
	}
	managedProxy := false
	if stream, ok := dst["streamSettings"].(map[string]interface{}); ok {
		if socket, ok := stream["sockopt"].(map[string]interface{}); ok {
			managedProxy = socket["acceptProxyProtocol"] == true
		}
	}
	if err := validateExtraManagedProxyContainer(src, managedProxy); err != nil {
		return err
	}
	deepMerge(dst, src)
	return nil
}

func deepMerge(dst, src map[string]interface{}) {
	for k, v := range src {
		srcMap, srcIsMap := v.(map[string]interface{})
		dstMap, dstIsMap := dst[k].(map[string]interface{})
		if srcIsMap && dstIsMap {
			deepMerge(dstMap, srcMap)
			continue
		}
		dst[k] = v
	}
}

// sockopt tuning remains extensible, but replacing its whole container must
// not remove a PROXY receiver declared by the panel.
func validateExtraManagedProxyContainer(obj map[string]interface{}, managed bool) error {
	if !managed {
		return nil
	}
	if stream, ok := obj["streamSettings"].(map[string]interface{}); ok {
		if socket, exists := stream["sockopt"]; exists {
			if _, ok := socket.(map[string]interface{}); !ok {
				return fmt.Errorf("高级配置不能替换受保护字段 %q 的对象容器", "streamSettings.sockopt")
			}
		}
	}
	return nil
}

func protectedExtraName(keys map[string]bool, name string) bool {
	for key := range keys {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
