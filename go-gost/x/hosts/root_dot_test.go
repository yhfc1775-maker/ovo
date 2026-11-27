package hosts

import (
	"context"
	"net"
	"testing"

	"github.com/go-gost/x/logger"
)

func TestLookupDomainRootDot(t *testing.T) {
	for _, rule := range []string{"example.test", ".example.test"} {
		for _, ruleDot := range []string{"", "."} {
			h := NewHostMapper(LoggerOption(logger.Nop()), MappingsOption([]Mapping{{Hostname: rule + ruleDot, IP: net.ParseIP("192.0.2.8")}}))
			for _, query := range []string{"example.test", "example.test."} {
				ips, _ := h.Lookup(context.Background(), "ip4", query)
				if len(ips) != 1 || !ips[0].Equal(net.ParseIP("192.0.2.8")) {
					t.Errorf("rule %q query %q: %v", rule+ruleDot, query, ips)
				}
			}
			if ips, _ := h.Lookup(context.Background(), "ip", "other.test."); len(ips) != 0 {
				t.Errorf("unrelated host matched: %v", ips)
			}
			h.(*hostMapper).Close()
		}
	}
}
