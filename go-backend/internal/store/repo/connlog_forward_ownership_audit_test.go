package repo

import (
	"testing"

	"go-backend/internal/store/model"
)

func TestConnlogForwardAttributionRequiresEntryNodeOwnership(t *testing.T) {
	r := newConnlogTestRepo(t)
	if err := r.db.AutoMigrate(&model.ForwardPort{}); err != nil {
		t.Fatal(err)
	}
	inbound := model.NodeInbound{KernelID: 1, NodeID: 42, Name: "landing", Protocol: "vless", Port: 443}
	if err := r.db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Create(&model.NodeInboundRelay{InboundID: inbound.ID, ForwardID: 77}).Error; err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []int64{7, 8} {
		if err := r.db.Create(&model.ForwardPort{ForwardID: 77, NodeID: nodeID, Port: 8443}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, nodeID := range []int64{7, 8, 9} {
		gotNode, gotInbound := r.ResolveConnlogDisplayKeys(nodeID, ConnlogEntryForward, "77_2_3_tcp")
		if nodeID == 9 {
			if gotNode != 0 || gotInbound != 0 {
				t.Errorf("foreign node %d attributed to landing %d/%d, want unassigned", nodeID, gotNode, gotInbound)
			}
		} else if gotNode != inbound.NodeID || gotInbound != inbound.ID {
			t.Errorf("own entry node %d attributed to %d/%d, want %d/%d", nodeID, gotNode, gotInbound, inbound.NodeID, inbound.ID)
		}
	}
}
