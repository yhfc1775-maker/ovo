package socket

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/resolver"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/service"
	_ "github.com/go-gost/x/connector/http"
	_ "github.com/go-gost/x/dialer/tcp"
	_ "github.com/go-gost/x/handler/forward/local"
	_ "github.com/go-gost/x/listener/udp"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

func TestAuditBatchCreateReleasesEveryPreparedListener(t *testing.T) {
	logger.SetDefault(xlogger.Nop())
	for _, position := range []int{1, 2} {
		for _, reason := range []string{"parse", "blank", "duplicate"} {
			t.Run(reason+string(rune('0'+position)), func(t *testing.T) {
				var configs []config.ServiceConfig
				var addresses []string
				for i := 0; i < position; i++ {
					ln, err := net.Listen("tcp4", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					addr := ln.Addr().String()
					ln.Close()
					addresses = append(addresses, addr)
					name := "audit-prepare-" + string(rune('a'+i))
					registry.ServiceRegistry().Unregister(name)
					t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })
					configs = append(configs, config.ServiceConfig{Name: name, Addr: addr})
				}
				bad := config.ServiceConfig{Name: "audit-bad", Addr: "127.0.0.1:0", Listener: &config.ListenerConfig{Type: "audit-unknown-listener"}}
				if reason == "blank" {
					bad.Name = " "
				}
				if reason == "duplicate" {
					bad = configs[0]
				}
				configs = append(configs, bad)
				for repeat := 0; repeat < 3; repeat++ {
					if err := createServices(createServicesRequest{Data: configs}); err == nil {
						t.Fatal("invalid batch succeeded")
					}
					for _, addr := range addresses {
						ln, err := net.Listen("tcp4", addr)
						if err != nil {
							t.Fatalf("unowned listener remains: %v", err)
						}
						ln.Close()
					}
					for _, sc := range configs[:position] {
						if registry.ServiceRegistry().IsRegistered(sc.Name) {
							t.Fatal("failed batch registered service")
						}
					}
				}
			})
		}
	}
}

func TestAuditForwardExecutionRejectsPrivateDNS(t *testing.T) {
	logger.SetDefault(xlogger.Nop())
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	_, port, _ := net.SplitHostPort(target.Addr().String())
	for _, guarded := range []bool{false, true} {
		cfg := &config.ServiceConfig{Name: "audit-rebinding", Addr: "127.0.0.1:0", Listener: &config.ListenerConfig{Type: "tcp"}, Handler: &config.HandlerConfig{Type: "tcp"}, Forwarder: &config.ForwarderConfig{Nodes: []*config.ForwardNodeConfig{{Name: "target", Addr: "localhost:" + port}}}}
		if guarded {
			cfg.Bypass = "ovo-no-private"
		}
		svc, err := parser.ParseService(cfg)
		if err != nil {
			t.Fatal(err)
		}
		go svc.Serve()
		conn, err := net.DialTimeout("tcp", svc.Addr().String(), time.Second)
		if err != nil {
			svc.Close()
			t.Fatal(err)
		}
		_, err = conn.Write([]byte("audit-tcp"))
		if err != nil {
			t.Fatal(err)
		}
		target.(*net.TCPListener).SetDeadline(time.Now().Add(300 * time.Millisecond))
		accepted, err := target.Accept()
		if err == nil {
			accepted.Close()
		}
		conn.Close()
		svc.Close()
		if guarded && err == nil {
			t.Fatal("guarded TCP reached private target")
		}
		if !guarded && err != nil {
			t.Fatalf("authorized TCP failed: %v", err)
		}
	}
}

func TestAuditUDPForwardPrivateDNSGuardAndAdminException(t *testing.T) {
	const resolverName = "audit-udp-ipv4"
	if err := registry.ResolverRegistry().Register(resolverName, auditIPv4Resolver{}); err != nil {
		t.Fatal(err)
	}
	defer registry.ResolverRegistry().Unregister(resolverName)
	logger.SetDefault(xlogger.Nop())
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	_, port, _ := net.SplitHostPort(target.LocalAddr().String())
	for _, guarded := range []bool{false, true} {
		cfg := &config.ServiceConfig{Name: "audit-udp", Resolver: resolverName, Addr: "127.0.0.1:0", Listener: &config.ListenerConfig{Type: "udp"}, Handler: &config.HandlerConfig{Type: "udp"}, Forwarder: &config.ForwarderConfig{Nodes: []*config.ForwardNodeConfig{{Name: "target", Addr: "localhost:" + port}}}}
		if guarded {
			cfg.Bypass = "ovo-no-private"
		}
		svc, err := parser.ParseService(cfg)
		if err != nil {
			t.Fatal(err)
		}
		go svc.Serve()
		conn, err := net.DialTimeout("udp", svc.Addr().String(), time.Second)
		if err != nil {
			svc.Close()
			t.Fatal(err)
		}
		_, err = conn.Write([]byte("audit-udp"))
		if err != nil {
			t.Fatal(err)
		}
		target.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 16)
		_, _, err = target.ReadFrom(buf)
		conn.Close()
		svc.Close()
		if guarded && err == nil {
			t.Fatal("guarded UDP reached private target")
		}
		if !guarded && err != nil {
			t.Fatalf("authorized UDP failed: %v", err)
		}
	}
}

type auditIPv4Resolver struct{}

func (auditIPv4Resolver) Resolve(context.Context, string, string, ...resolver.Option) ([]net.IP, error) {
	return []net.IP{net.ParseIP("127.0.0.1")}, nil
}

func TestAuditPauseRetryRemainsIdempotent(t *testing.T) {
	logger.SetDefault(xlogger.Nop())
	original, path := config.Global(), config.PersistPath()
	defer func() { config.Set(original); config.SetPersistPath(path) }()
	config.Set(&config.Config{})
	config.SetPersistPath("")
	name := "audit-pause-retry"
	defer registry.ServiceRegistry().Unregister(name)
	if err := createServices(createServicesRequest{Data: []config.ServiceConfig{{Name: name, Addr: "127.0.0.1:0"}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := pauseServices(pauseServicesRequest{Services: []string{name}}); err != nil {
			t.Fatalf("pause retry %d: %v", i, err)
		}
	}
}
