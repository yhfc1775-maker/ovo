package net

import "testing"

func TestTrimDomainRootDotPreservesAddressShape(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"example.com.", "example.com"},
		{"example.com.:443", "example.com:443"},
		{"*.example.com.:80-90", "*.example.com:80-90"},
		{".example.com.", ".example.com"},
		{"[2001:db8::1]:443", "[2001:db8::1]:443"},
		{"2001:db8::1", "2001:db8::1"},
		{"fe80::1%eth0.", "fe80::1%eth0."},
		{"[fe80::1%eth0.]:443", "[fe80::1%eth0.]:443"},
		{"/tmp/name.", "/tmp/name."},
		{"192.0.2.1:080", "192.0.2.1:080"},
		{"example.com", "example.com"},
		{"", ""}, {".", "."}, {".:80", ".:80"},
	} {
		if got := TrimDomainRootDot(tc.in); got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}
