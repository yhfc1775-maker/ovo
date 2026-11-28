package local

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/hop"
	xlogger "github.com/go-gost/x/logger"
	xmd "github.com/go-gost/x/metadata"
)

type auditRetryHop struct {
	nodes []*chain.Node
	next  int
}

func (h *auditRetryHop) Select(context.Context, ...hop.SelectOption) *chain.Node {
	n := h.nodes[h.next]
	h.next++
	return n
}
func (h *auditRetryHop) Nodes() []*chain.Node { return h.nodes }

type auditRetryRouter struct{ networks []string }

func (r *auditRetryRouter) Options() *chain.RouterOptions { return &chain.RouterOptions{} }
func (r *auditRetryRouter) Dial(_ context.Context, network, address string) (net.Conn, error) {
	r.networks = append(r.networks, network)
	return nil, errors.New("audit failed target")
}
func (r *auditRetryRouter) Bind(context.Context, string, string, ...chain.BindOption) (net.Listener, error) {
	return nil, net.ErrClosed
}

type auditRetryConn struct {
	net.Conn
	network string
}

func (c auditRetryConn) RemoteAddr() net.Addr {
	if c.network == "udp" {
		return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10000}
	}
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10000}
}
func (c auditRetryConn) LocalAddr() net.Addr { return c.RemoteAddr() }

func TestAuditFailoverResetsTargetTransport(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			r := &auditRetryRouter{}
			h := NewHandler(handler.RouterOption(r), handler.LoggerOption(xlogger.Nop()))
			if err := h.Init(xmd.NewMetadata(nil)); err != nil {
				t.Fatal(err)
			}
			h.(handler.Forwarder).Forward(&auditRetryHop{nodes: []*chain.Node{
				chain.NewNode("unix", "/audit/not-a-real-socket", chain.NetworkNodeOption("unix")),
				chain.NewNode("network", net.JoinHostPort("127.0.0.1", fmt.Sprint(10001))),
			}})
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			_ = h.Handle(context.Background(), auditRetryConn{Conn: a, network: network})
			if !reflect.DeepEqual(r.networks, []string{"unix", network}) {
				t.Fatalf("one target's network leaked into the next target: %v", r.networks)
			}
		})
	}
}
