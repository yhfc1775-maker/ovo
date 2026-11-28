package chain

import (
	"context"
	"net"
	"testing"

	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/metadata"
	"github.com/go-gost/x/config"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

type auditResource struct {
	t        *testing.T
	listener net.Listener
	addr     string
	closes   int
}

func (r *auditResource) Init(metadata.Metadata) error {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	r.listener = ln
	r.addr = ln.Addr().String()
	return nil
}
func (r *auditResource) Close() error {
	r.closes++
	if r.listener != nil {
		return r.listener.Close()
	}
	return nil
}

type auditConnector struct{ *auditResource }

func (c *auditConnector) Connect(context.Context, net.Conn, string, string, ...connector.ConnectOption) (net.Conn, error) {
	return nil, nil
}

type auditDialer struct{ *auditResource }

func (d *auditDialer) Dial(context.Context, string, ...dialer.DialOption) (net.Conn, error) {
	return nil, nil
}

func TestAuditParserResourceOwnershipClosesWholeBatch(t *testing.T) {
	const kind = "audit-resource"
	var resources []*auditResource
	makeResource := func() *auditResource { r := &auditResource{t: t}; resources = append(resources, r); return r }
	if err := registry.ConnectorRegistry().Register(kind, func(...connector.Option) connector.Connector { return &auditConnector{makeResource()} }); err != nil {
		t.Fatal(err)
	}
	if err := registry.DialerRegistry().Register(kind, func(...dialer.Option) dialer.Dialer { return &auditDialer{makeResource()} }); err != nil {
		t.Fatal(err)
	}
	defer registry.ConnectorRegistry().Unregister(kind)
	defer registry.DialerRegistry().Unregister(kind)
	good := func() *config.NodeConfig {
		return &config.NodeConfig{Name: "good", Addr: "8.8.8.8:443", Connector: &config.ConnectorConfig{Type: kind}, Dialer: &config.DialerConfig{Type: kind}}
	}
	bad := func() *config.NodeConfig {
		return &config.NodeConfig{Name: "bad", Addr: "8.8.8.8:443", Connector: &config.ConnectorConfig{Type: kind}, Dialer: &config.DialerConfig{Type: "audit-unregistered"}}
	}
	for _, mode := range []string{"node-in-hop", "hop-in-chain", "nil-hop", "success"} {
		t.Run(mode, func(t *testing.T) {
			resources = nil
			cfg := &config.ChainConfig{Name: "audit-chain", Hops: []*config.HopConfig{{Name: "first", Nodes: []*config.NodeConfig{good()}}}}
			switch mode {
			case "node-in-hop":
				cfg.Hops[0].Nodes = append(cfg.Hops[0].Nodes, bad())
			case "hop-in-chain":
				cfg.Hops = append(cfg.Hops, &config.HopConfig{Name: "second", Nodes: []*config.NodeConfig{good(), bad()}})
			case "nil-hop":
				cfg.Hops = append(cfg.Hops, nil)
			}
			parsed, err := ParseChain(cfg, xlogger.Nop())
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if c, ok := parsed.(interface{ Close() error }); !ok {
					t.Fatal("chain does not own resources")
				} else {
					if err = c.Close(); err != nil {
						t.Fatal(err)
					}
				}
			} else if err == nil {
				t.Fatal("invalid batch succeeded")
			}
			for _, r := range resources {
				if r.closes != 1 {
					if r.listener != nil {
						r.listener.Close()
					}
					t.Errorf("resource closed %d times", r.closes)
				}
				if r.addr != "" {
					ln, err := net.Listen("tcp4", r.addr)
					if err != nil {
						t.Errorf("resource still bound: %v", err)
					} else {
						ln.Close()
					}
				}
			}
		})
	}
}
