package socket

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"syscall"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/service"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/service"
	kill "github.com/go-gost/x/internal/util/port"
	"github.com/go-gost/x/registry"
	xservice "github.com/go-gost/x/service"
)

var (
	ErrServiceInvalid   = errors.New("invalid service request")
	ErrServiceDuplicate = errors.New("duplicate service")
	ErrServiceNotFound  = errors.New("service not found")
)

type serviceReplacement struct {
	config    config.ServiceConfig
	oldConfig *config.ServiceConfig
}

func createServices(req createServicesRequest) error {

	if len(req.Data) == 0 {
		return fmt.Errorf("services list cannot be empty: %w", ErrServiceInvalid)
	}

	// 第一阶段：验证所有服务配置
	var parsedServices []struct {
		config  config.ServiceConfig
		service service.Service
	}

	defer func() {
		for _, ps := range parsedServices {
			if ps.service != nil {
				_ = ps.service.Close()
			}
		}
	}()
	seen := make(map[string]bool, len(req.Data))
	for _, serviceConfig := range req.Data {
		name := strings.TrimSpace(serviceConfig.Name)
		if name == "" {
			return fmt.Errorf("service name is required: %w", ErrServiceInvalid)
		}
		serviceConfig.Name = name
		if seen[name] {
			return fmt.Errorf("%w: %s", ErrServiceDuplicate, name)
		}
		seen[name] = true

		if registry.ServiceRegistry().IsRegistered(name) {
			return fmt.Errorf("service %s already exists: %w", name, ErrServiceDuplicate)
		}

		svc, err := parser.ParseService(&serviceConfig)
		if err != nil {
			return errors.New("create service " + name + " failed: " + err.Error())
		}

		parsedServices = append(parsedServices, struct {
			config  config.ServiceConfig
			service service.Service
		}{serviceConfig, svc})
	}

	// 第二阶段：注册所有服务
	var registeredServices []string
	for i := range parsedServices {
		ps := &parsedServices[i]
		if err := registry.ServiceRegistry().Register(ps.config.Name, ps.service); err != nil {
			// 如果注册失败，回滚已注册的服务
			for _, regName := range registeredServices {
				if registry.ServiceRegistry().Get(regName) != nil {
					registry.ServiceRegistry().Unregister(regName)
				}
			}
			return fmt.Errorf("service %s already exists: %w", ps.config.Name, ErrServiceDuplicate)
		}
		registeredServices = append(registeredServices, ps.config.Name)
		ps.service = nil // registry owns Close from this point onward
	}

	// 第三阶段：启动所有服务
	for _, ps := range parsedServices {
		if svc := registry.ServiceRegistry().Get(ps.config.Name); svc != nil {
			if ps.config.Metadata["paused"] == true {
				_ = svc.Close()
			} else {
				go svc.Serve()
			}
		}
	}

	// 第四阶段：更新配置。若落盘失败必须回滚运行时（D1-06）：否则面板
	// 收到失败、服务却已在跑且内存配置已包含它，agent 重启后又消失，
	// 面板/内存/磁盘三方状态不一致（updateServices 已有同口径回滚）。
	// R17-A：同名则替换（与 updateServices 第三阶段同口径），不用无去重
	// append——服务已在配置中、但不在注册表里时（例如暂停后 loader 跳过
	// 注册），重复 AddService 下发会把第二份同名服务写进配置并落盘、
	// 毒化 gost.json。配置里已有的多余同名副本也一并清除。
	originalConfig := config.Global()
	if err := config.OnUpdate(func(c *config.Config) error {
		for _, ps := range parsedServices {
			cfgCopy := ps.config
			replaced := false
			kept := make([]*config.ServiceConfig, 0, len(c.Services)+1)
			for _, existing := range c.Services {
				if existing != nil && existing.Name == cfgCopy.Name {
					if replaced {
						continue
					}
					kept = append(kept, &cfgCopy)
					replaced = true
					continue
				}
				kept = append(kept, existing)
			}
			if !replaced {
				kept = append(kept, &cfgCopy)
			}
			c.Services = kept
		}
		return nil
	}); err != nil {
		config.Set(originalConfig)
		for _, regName := range registeredServices {
			if registry.ServiceRegistry().Get(regName) != nil {
				registry.ServiceRegistry().Unregister(regName)
			}
		}
		return err
	}
	return nil
}

// isAddrInUseError reports whether err is a bind EADDRINUSE (listener
// port already held by another process). The error may arrive as a
// wrapped syscall error or flattened into text by the config parser,
// so both forms are recognised.
func isAddrInUseError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	return strings.Contains(err.Error(), "address already in use")
}

func updateServices(req updateServicesRequest) error {

	if len(req.Data) == 0 {
		return fmt.Errorf("services list cannot be empty: %w", ErrServiceInvalid)
	}

	names := make([]string, len(req.Data))
	for i := range req.Data {
		names[i] = req.Data[i].Name
	}
	if _, err := normalizedServiceNames(names); err != nil {
		return err
	}

	// 第一阶段：验证所有服务名称有效性
	for i := range req.Data {
		name := strings.TrimSpace(req.Data[i].Name)
		if name == "" {
			return fmt.Errorf("service name is required: %w", ErrServiceInvalid)
		}
		req.Data[i].Name = name
	}

	// 第二阶段：逐个更新服务（Upsert模式：存在则更新，不存在则创建）。
	// 配置变更命令由 WebSocket reporter 串行调度，但这里仍保留完整回滚，
	// 避免新配置解析或监听失败后旧服务永久消失。
	originalConfig := config.Global()
	changedServices := make([]serviceReplacement, 0, len(req.Data))
	for i := range req.Data {
		serviceConfig := &req.Data[i]
		name := serviceConfig.Name
		if registry.ServiceRegistry().Get(name) != nil && serviceConfigUnchanged(name, *serviceConfig) {
			continue
		}

		var oldConfig *config.ServiceConfig
		if originalConfig != nil {
			for _, current := range originalConfig.Services {
				if current != nil && strings.TrimSpace(current.Name) == name {
					oldConfig = current
					break
				}
			}
		}

		// 1. 关闭并移除旧服务（如果存在）。同名监听必须先释放端口，
		// 才能创建新 listener。
		if registry.ServiceRegistry().Get(name) != nil {
			registry.ServiceRegistry().Unregister(name)
		}

		// 2. 解析新服务配置。
		svc, err := parser.ParseService(serviceConfig)
		if err != nil {
			// C-04：端口冲突须可与其它创建失败区分——面板侧据此判定
			// 孤儿监听者占口，而不是把一切创建失败都当作“服务不存
			// 在”去自愈重建（自愈重建撞同一端口是登记转发 168 的
			// 两次 PauseService 失败根因）。标记进错误文本
			// （port_conflict:），其余内容不变。
			createErr := err
			if isAddrInUseError(err) {
				createErr = fmt.Errorf("port_conflict: %w", err)
			}
			rollbackErr := restoreServiceRuntime(name, oldConfig)
			rollbackErr = errors.Join(rollbackErr, rollbackServiceReplacements(changedServices))
			if rollbackErr != nil {
				return fmt.Errorf("create service %s failed: %v; restore previous service failed: %w", name, createErr, rollbackErr)
			}
			return errors.New("create service " + name + " failed: " + createErr.Error())
		}

		// 3. 注册并启动新服务。
		if err := registry.ServiceRegistry().Register(name, svc); err != nil {
			svc.Close()
			rollbackErr := restoreServiceRuntime(name, oldConfig)
			rollbackErr = errors.Join(rollbackErr, rollbackServiceReplacements(changedServices))
			if rollbackErr != nil {
				return fmt.Errorf("service %s already exists; restore previous service failed: %w", name, rollbackErr)
			}
			return fmt.Errorf("service %s already exists: %w", name, ErrServiceDuplicate)
		}
		if serviceConfig.Metadata["paused"] == true {
			_ = svc.Close()
		} else {
			go svc.Serve()
		}
		changedServices = append(changedServices, serviceReplacement{
			config:    *serviceConfig,
			oldConfig: oldConfig,
		})
	}
	if len(changedServices) == 0 {
		return nil
	}

	// 第三阶段：更新配置
	if err := config.OnUpdate(func(c *config.Config) error {
		for i := range changedServices {
			// 创建副本以确保指针安全
			cfgCopy := changedServices[i].config
			found := false
			for j := range c.Services {
				// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
				if c.Services[j] == nil {
					logger.Default().Warnf("update services: skipped nil service entry at index %d", j)
					continue
				}
				if c.Services[j].Name == cfgCopy.Name {
					c.Services[j] = &cfgCopy
					found = true
					break
				}
			}
			if !found {
				c.Services = append(c.Services, &cfgCopy)
			}
		}
		return nil
	}); err != nil {
		config.Set(originalConfig)
		if rollbackErr := rollbackServiceReplacements(changedServices); rollbackErr != nil {
			return fmt.Errorf("%w; restore previous services failed: %v", err, rollbackErr)
		}
		return err
	}

	return nil
}

func restoreServiceRuntime(name string, serviceConfig *config.ServiceConfig) error {
	name = strings.TrimSpace(name)
	if name == "" || serviceConfig == nil {
		return nil
	}
	if registry.ServiceRegistry().Get(name) != nil {
		registry.ServiceRegistry().Unregister(name)
	}

	cfgCopy := *serviceConfig
	cfgCopy.Name = name
	svc, err := parser.ParseService(&cfgCopy)
	if err != nil {
		return err
	}
	if err := registry.ServiceRegistry().Register(name, svc); err != nil {
		svc.Close()
		return err
	}
	if cfgCopy.Metadata["paused"] == true {
		_ = svc.Close()
	} else {
		go svc.Serve()
	}
	return nil
}

func rollbackServiceReplacements(replacements []serviceReplacement) error {
	var rollbackErr error
	for i := len(replacements) - 1; i >= 0; i-- {
		name := strings.TrimSpace(replacements[i].config.Name)
		if name == "" {
			continue
		}
		if registry.ServiceRegistry().Get(name) != nil {
			registry.ServiceRegistry().Unregister(name)
		}
		if err := restoreServiceRuntime(name, replacements[i].oldConfig); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore service %s: %w", name, err))
		}
	}
	return rollbackErr
}

func serviceConfigUnchanged(name string, next config.ServiceConfig) bool {
	cfg := config.Global()
	if cfg == nil {
		return false
	}
	next.Status = nil
	for _, current := range cfg.Services {
		if current == nil || strings.TrimSpace(current.Name) != name {
			continue
		}
		currentCopy := *current
		currentCopy.Status = nil
		return reflect.DeepEqual(currentCopy, next)
	}
	return false
}

func deleteServices(req deleteServicesRequest) error {
	names, err := normalizedServiceNames(req.Services)
	if err != nil {
		return err
	}
	if err := config.OnUpdate(func(c *config.Config) error {
		kept := c.Services[:0]
		for _, sc := range c.Services {
			if sc == nil || !names[sc.Name] {
				kept = append(kept, sc)
			}
		}
		c.Services = kept
		return nil
	}); err != nil {
		return err
	}
	for name := range names {
		registry.ServiceRegistry().Unregister(name)
	}
	xservice.GetGlobalTrafficManager().RemoveServices(req.Services...)
	return nil
}

func pauseKillAddr(svc service.Service, configAddr string) string {
	if svc != nil {
		if addr := svc.Addr(); addr != nil {
			if _, p, err := net.SplitHostPort(addr.String()); err == nil && p != "" && p != "0" {
				return addr.String()
			}
		}
	}
	if _, p, err := net.SplitHostPort(configAddr); err == nil && p != "" && p != "0" {
		return configAddr
	}
	return ""
}

// pauseKillNeeded reports whether the port currently has live or
// draining TCP connections (scan of /proc/net). A scan error returns
// true — the safe side is to attempt the kill and fail loud if it
// cannot run. A-NEW: the scan counts every live/draining state, not
// just ESTABLISHED — a bulk relay flow is in FIN-WAIT-2/CLOSE-WAIT
// by the time the pause lands, with megabytes still buffered in the
// peer's Recv-Q, and counting only ESTABLISHED let the pause skip
// the kill entirely.
func pauseKillNeeded(addr string) (bool, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return true, nil
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 {
		return false, nil
	}
	live, err := scanProcNetConnected(procNetDir, map[int]bool{p: true})
	if err != nil {
		return true, err
	}
	return len(live) > 0, nil
}

func pauseServices(req pauseServicesRequest) error {
	names, err := normalizedServiceNames(req.Services)
	if err != nil {
		return err
	}
	cfg := config.Global()
	configs := map[string]*config.ServiceConfig{}
	for _, sc := range cfg.Services {
		if sc != nil {
			configs[sc.Name] = sc
		}
	}
	for name := range names {
		if configs[name] == nil || registry.ServiceRegistry().Get(name) == nil {
			return fmt.Errorf("service %s not found: %w", name, ErrServiceNotFound)
		}
	}
	// Persistence failure leaves every listener and existing connection untouched.
	if err := config.OnUpdate(func(c *config.Config) error {
		for _, sc := range c.Services {
			if sc != nil && names[sc.Name] {
				if sc.Metadata == nil {
					sc.Metadata = map[string]any{}
				}
				sc.Metadata["paused"] = true
			}
		}
		return nil
	}); err != nil {
		return err
	}
	var failures error
	for name := range names {
		svc := registry.ServiceRegistry().Get(name)
		addr := pauseKillAddr(svc, configs[name].Addr)
		if err := svc.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			failures = errors.Join(failures, err)
		}
		if addr != "" {
			if needed, _ := pauseKillNeeded(addr); needed {
				if err := kill.ForceClosePortConnections(addr); err != nil {
					failures = errors.Join(failures, fmt.Errorf("pause saved; close connections on %s failed: %w", addr, err))
				} else if still, _ := pauseKillNeeded(addr); still {
					failures = errors.Join(failures, fmt.Errorf("pause saved; connections on %s still alive", addr))
				}
			}
		}
	}
	return failures
}

func resumeServices(req resumeServicesRequest) error {
	names, err := normalizedServiceNames(req.Services)
	if err != nil {
		return err
	}
	cfg := config.Global()
	configs := map[string]*config.ServiceConfig{}
	for _, sc := range cfg.Services {
		if sc != nil {
			configs[sc.Name] = sc
		}
	}
	for name := range names {
		if configs[name] == nil {
			return fmt.Errorf("service %s not found: %w", name, ErrServiceNotFound)
		}
		if configs[name].Metadata["paused"] != true && registry.ServiceRegistry().Get(name) == nil {
			return fmt.Errorf("service %s not found: %w", name, ErrServiceNotFound)
		}
	}
	old := map[string]service.Service{}
	prepared := map[string]service.Service{}
	committed := false
	defer func() {
		if committed {
			return
		}
		for name := range prepared {
			registry.ServiceRegistry().Unregister(name)
		}
		for name, svc := range old {
			if svc != nil {
				_ = registry.ServiceRegistry().Register(name, svc)
			}
		}
	}()
	for name := range names {
		sc := configs[name]
		if sc.Metadata["paused"] != true {
			continue
		}
		old[name] = registry.ServiceRegistry().Get(name)
		registry.ServiceRegistry().Unregister(name)
		svc, err := parser.ParseService(sc)
		if err != nil {
			return fmt.Errorf("resume service %s: %w", name, err)
		}
		if err := registry.ServiceRegistry().Register(name, svc); err != nil {
			_ = svc.Close()
			return err
		}
		prepared[name] = svc
	}
	if err := config.OnUpdate(func(c *config.Config) error {
		for _, sc := range c.Services {
			if sc != nil && names[sc.Name] {
				delete(sc.Metadata, "paused")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	committed = true
	for _, svc := range prepared {
		go svc.Serve()
	}
	return nil
}

type resumeServicesRequest struct {
	Services []string `json:"services"`
}

type pauseServicesRequest struct {
	Services []string `json:"services"`
}

type deleteServicesRequest struct {
	Services []string `json:"services"`
}

type updateServicesRequest struct {
	Data []config.ServiceConfig `json:"data"`
}

type createServicesRequest struct {
	Data []config.ServiceConfig `json:"data"`
}

func normalizedServiceNames(input []string) (map[string]bool, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("services list cannot be empty: %w", ErrServiceInvalid)
	}
	names := make(map[string]bool, len(input))
	for _, raw := range input {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("service name is required: %w", ErrServiceInvalid)
		}
		if names[name] {
			return nil, fmt.Errorf("%w: %s", ErrServiceDuplicate, name)
		}
		names[name] = true
	}
	return names, nil
}

// MutateServices shares complete persistence/ownership handling with the HTTP API.
// Caller must hold config.LockMutation across validation and this operation.
func MutateServices(operation string, data []config.ServiceConfig, names []string) error {
	switch operation {
	case "create":
		return createServices(createServicesRequest{Data: data})
	case "update":
		return updateServices(updateServicesRequest{Data: data})
	case "delete":
		return deleteServices(deleteServicesRequest{Services: names})
	case "pause":
		return pauseServices(pauseServicesRequest{Services: names})
	case "resume":
		return resumeServices(resumeServicesRequest{Services: names})
	default:
		return errors.New("unknown service operation")
	}
}
