package health

import (
	"context"
	"go-backend/internal/store/model"
	"net"
	"testing"
	"time"
)

func TestAuditMonitorDNSUsesTotalBudget(t *testing.T) {
	oldLookup, oldDial := monitorLookupIPContext, tcpDialContext
	defer func() { monitorLookupIPContext, tcpDialContext = oldLookup, oldDial }()
	dialed := false
	monitorLookupIPContext = func(ctx context.Context, _, _ string) ([]net.IP, error) { <-ctx.Done(); return nil, ctx.Err() }
	tcpDialContext = func(context.Context, string, string) (net.Conn, error) { dialed = true; return nil, context.Canceled }
	for _, cancelEarly := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelEarly {
			cancel()
		}
		start := time.Now()
		var result model.ServiceMonitorResult
		(&Checker{}).checkTCPContext(ctx, "controlled.example:443", 20*time.Millisecond, &result)
		cancel()
		if result.Success != 0 || dialed || time.Since(start) > 500*time.Millisecond {
			t.Fatalf("DNS escaped budget/cancellation: %+v", result)
		}
	}
}

func TestAuditNodeMonitorDNSUsesRequestBudget(t *testing.T) {
	old := monitorLookupIPContext
	defer func() { monitorLookupIPContext = old }()
	monitorLookupIPContext = func(ctx context.Context, _, _ string) ([]net.IP, error) { <-ctx.Done(); return nil, ctx.Err() }
	commander := &fakeCommander{}
	checker := NewChecker(nil, commander)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := checker.executeCheckContext(ctx, &model.ServiceMonitor{NodeID: 1, Type: "tcp", Target: "controlled.example:443", TimeoutSec: 5}, time.Now().UnixMilli(), checker.loadServiceMonitorLimits())
	if result.Success != 0 || commander.lastType != "" || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("node DNS escaped request budget: %+v", result)
	}
}
