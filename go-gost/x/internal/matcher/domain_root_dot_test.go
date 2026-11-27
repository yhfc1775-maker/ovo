package matcher

import "testing"

func TestDomainRootDotMatcherFamily(t *testing.T) {
	for _, makeMatcher := range []struct {
		name string
		new  func([]string) Matcher
	}{{"address", AddrMatcher}, {"wildcard", WildcardMatcher}, {"domain", DomainMatcher}} {
		for _, rule := range []string{"example.com", "example.com."} {
			for _, addr := range []string{"example.com", "example.com."} {
				t.Run(makeMatcher.name+"/"+rule+"/"+addr, func(t *testing.T) {
					m := makeMatcher.new([]string{rule})
					if !m.Match(addr) {
						t.Errorf("root-dot equivalent rule %q did not match %q", rule, addr)
					}
					if m.Match("other.example.com") || m.Match("badexample.com") {
						t.Fatal("exact rule broadened")
					}
				})
			}
		}
	}
	for _, rule := range []string{".example.com", ".example.com."} {
		for _, makeMatcher := range []func([]string) Matcher{AddrMatcher, DomainMatcher} {
			m := makeMatcher([]string{rule})
			for _, addr := range []string{"example.com", "example.com.", "a.example.com", "a.example.com."} {
				if !m.Match(addr) {
					t.Errorf("suffix rule %q missed %q", rule, addr)
				}
			}
			if m.Match("badexample.com.") {
				t.Error("suffix boundary lost")
			}
		}
	}
	for _, pattern := range []string{"*.example.com", "*.example.com.", "??.example.com", "??.example.com."} {
		for _, suffix := range []string{"", "."} {
			if !WildcardMatcher([]string{pattern}).Match("ab.example.com" + suffix) {
				t.Errorf("glob %q missed rooted hostname", pattern)
			}
		}
	}
}

func TestDomainRootDotKeepsMatcherPorts(t *testing.T) {
	for _, factory := range []struct {
		name string
		new  func([]string) Matcher
	}{{"address", AddrMatcher}, {"wildcard", WildcardMatcher}} {
		for _, host := range []string{"example.com", "example.com."} {
			for _, patternPort := range []string{"80", "80-90"} {
				rule := host + ":" + patternPort
				if factory.name == "wildcard" {
					rule = "*." + rule
				}
				m := factory.new([]string{rule})
				for _, addrHost := range []string{"example.com", "example.com."} {
					if factory.name == "wildcard" {
						addrHost = "a." + addrHost
					}
					if !m.Match(addrHost + ":80") {
						t.Errorf("%s rule %q missed %q", factory.name, rule, addrHost+":80")
					}
					if m.Match(addrHost+":91") || m.Match(addrHost) {
						t.Errorf("%s rule %q lost port restriction", factory.name, rule)
					}
				}
			}
		}
	}
	for _, rule := range []string{"example.com", "example.com."} {
		if !AddrMatcher([]string{rule}).Match("example.com.:443") {
			t.Errorf("portless rule %q missed address with port", rule)
		}
	}
	for _, addr := range []string{"192.0.2.1:80", "[2001:db8::1]:80"} {
		if !AddrMatcher([]string{addr}).Match(addr) {
			t.Errorf("literal IP behavior changed: %s", addr)
		}
	}
}
