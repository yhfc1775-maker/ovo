package matcher

import "testing"

func TestRootDotNormalizationDoesNotAliasIPv6ZoneOrPaths(t *testing.T) {
	for _, tc := range []struct{ rule, address string }{
		{"fe80::1%eth0.", "fe80::1%eth0"},
		{"fe80::1%eth0", "fe80::1%eth0."},
		{"[fe80::1%eth0.]:443", "[fe80::1%eth0]:443"},
		{"[fe80::1%eth0]:443", "[fe80::1%eth0.]:443"},
		{"/tmp/name.", "/tmp/name"},
	} {
		for _, m := range []Matcher{AddrMatcher([]string{tc.rule}), WildcardMatcher([]string{tc.rule}), DomainMatcher([]string{tc.rule})} {
			if m.Match(tc.address) {
				t.Errorf("different literal address was normalized as a DNS name: %T %q %q", m, tc.rule, tc.address)
			}
			if !m.Match(tc.rule) {
				t.Errorf("original literal no longer matches itself: %T %q", m, tc.rule)
			}
		}
	}
}
