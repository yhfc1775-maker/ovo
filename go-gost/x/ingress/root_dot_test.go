package ingress

import (
	"context"
	"testing"

	"github.com/go-gost/core/ingress"
	"github.com/go-gost/x/logger"
)

func TestGetRuleDomainRootDot(t *testing.T) {
	for _, rule := range []string{"example.test", "*.example.test"} {
		for _, dot := range []string{"", "."} {
			ing := NewIngress(LoggerOption(logger.Nop()), RulesOption([]*ingress.Rule{{Hostname: rule + dot, Endpoint: "192.0.2.9:8443"}}))
			for _, query := range []string{"example.test", "example.test.", "example.test.:443"} {
				got := ing.GetRule(context.Background(), query)
				if got == nil || got.Endpoint != "192.0.2.9:8443" {
					t.Errorf("rule %q query %q: %+v", rule+dot, query, got)
				}
			}
			if got := ing.GetRule(context.Background(), "other.test."); got != nil {
				t.Errorf("unrelated host matched: %+v", got)
			}
			ing.(*localIngress).Close()
		}
	}
}
