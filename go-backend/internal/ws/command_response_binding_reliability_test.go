package ws

import (
	"testing"
)

func TestReliabilityForeignNodeCannotConsumePending(t *testing.T) {
	s := NewServer(nil, "test")
	ch := make(chan CommandResult, 1)
	s.pending["61_UpgradeAgent_100000000"] = pendingRequest{nodeID: 61, ch: ch}
	s.tryResolvePending(21, `{"type":"UpgradeAgentResponse","requestId":"61_UpgradeAgent_100000000","success":false,"message":"spoof"}`)
	if _, ok := s.pending["61_UpgradeAgent_100000000"]; !ok {
		t.Fatal("foreign node consumed another node's pending receipt")
	}
	select {
	case <-ch:
		t.Fatal("foreign response settled victim command")
	default:
	}
}
