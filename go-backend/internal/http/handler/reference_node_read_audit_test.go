package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// Current server projections are ID-based at the pinned baseline; stale client
// snapshots are exercised by the separate production-callback contracts.
func TestAudit2131NodeRenameReadLayers(t *testing.T) {
	a := newCleanupAgent(t)
	f := seedR19F03Forward(t, a, 0, 0)
	before, err := a.h.repo.GetNodeRecord(1)
	if err != nil || before == nil {
		t.Fatalf("node fixture: %v", err)
	}
	segment := tunnelRuntimeNode{NodeID: 1, ChainType: 3, Port: 22000, Protocol: "tls"}
	configBefore := a.h.buildTunnelChainServiceConfig(f.TunnelID, segment, before, 1, nil, nil, "tls")
	postNodeUpdateOK(t, a, map[string]interface{}{"id": 1, "name": "reference-renamed-node"})
	after, err := a.h.repo.GetNodeRecord(1)
	if err != nil || after == nil || after.Name != "reference-renamed-node" {
		t.Fatalf("node read: %+v %v", after, err)
	}
	chains, err := a.h.repo.ListChainNodesForTunnel(f.TunnelID)
	if err != nil || len(chains) == 0 {
		t.Fatalf("chain read: %+v %v", chains, err)
	}
	for _, c := range chains {
		if c.NodeID == 1 && c.NodeName != "reference-renamed-node" {
			t.Fatalf("stale chain projection: %+v", c)
		}
	}
	for label, read := range map[string]func() ([]map[string]interface{}, error){"node-list": a.h.repo.ListNodes, "tunnel-list": a.h.repo.ListTunnels} {
		rows, e := read()
		if e != nil || len(rows) == 0 {
			t.Fatalf("%s: %v", label, e)
		}
		raw, _ := json.Marshal(rows)
		if label == "node-list" && !strings.Contains(string(raw), "reference-renamed-node") {
			t.Fatalf("node-list stale: %s", raw)
		}
		if label == "tunnel-list" && strings.Contains(string(raw), before.Name) {
			t.Fatalf("tunnel-list stored obsolete node name: %s", raw)
		}
	}
	configAfter := a.h.buildTunnelChainServiceConfig(f.TunnelID, segment, after, 1, nil, nil, "tls")
	left, _ := json.Marshal(configBefore)
	right, _ := json.Marshal(configAfter)
	if len(configBefore) == 0 || string(left) != string(right) {
		t.Fatalf("display rename changed generated traffic config: %s != %s", left, right)
	}
	a.probe(t)
	t.Log("Handler rename, current node/chain/list projections and stable generated segment config verified; browser cache is a separate contract")
}
