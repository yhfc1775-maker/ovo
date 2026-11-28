package bypass

import (
	"context"
	"io"
	"testing"

	"github.com/go-gost/x/logger"
)

func TestBypassDomainRootDotBothDirections(t *testing.T) {
	for _, whitelist := range []bool{false, true} {
		for _, host := range []string{"example.com", "example.com.", "*.example.com", "*.example.com."} {
			for _, port := range []string{"", ":443", ":440-450"} {
				rule := host + port
				bp := NewBypass(MatchersOption([]string{rule}), WhitelistOption(whitelist), LoggerOption(logger.NewLogger(logger.OutputOption(io.Discard))))
				for _, addr := range []string{"example.com", "example.com.", "example.com:443", "example.com.:443", "a.example.com", "a.example.com.", "a.example.com:443", "a.example.com.:443", "other.test:443", "a.example.com.:451"} {
					isSub := len(addr) > 2 && addr[:2] == "a."
					wantMatched := addr != "other.test:443"
					if host[0] == '*' {
						wantMatched = wantMatched && isSub
					} else {
						wantMatched = wantMatched && !isSub
					}
					if port != "" {
						wantMatched = wantMatched && (len(addr) > 4 && addr[len(addr)-4:] == ":443")
					}
					want := wantMatched != whitelist
					if got := bp.Contains(context.Background(), "tcp", addr); got != want {
						t.Errorf("whitelist=%v rule=%q addr=%q got=%v want=%v", whitelist, rule, addr, got, want)
					}
				}
				if closer, ok := bp.(interface{ Close() error }); ok {
					_ = closer.Close()
				}
			}
		}
	}
}
