package hop

import (
	"testing"

	"github.com/go-gost/core/chain"
)

func TestHopHostFilterDomainRootDot(t *testing.T) {
	p := &chainHop{}
	for _, rule := range []string{"example.test", "example.test.", ".example.test", ".example.test."} {
		node := chain.NewNode("fixture", "192.0.2.1:443", chain.NodeFilterOption(&chain.NodeFilterSettings{Host: rule}))
		for _, host := range []string{"example.test", "example.test.", "example.test.:443"} {
			if !p.checkHost(host, node) {
				t.Errorf("rule %q query %q failed", rule, host)
			}
		}
		if p.checkHost("other.test.", node) {
			t.Errorf("rule %q matched unrelated host", rule)
		}
	}
}
