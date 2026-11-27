package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestConnlogIngestForwardFromForeignNodeStaysUnassigned(t *testing.T) {
	rig := newConnlogTestRig(t)
	events := fmt.Sprintf(`[{"type":"open","connId":"foreign-forward","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"tcp_conn","startTime":%d}]`, time.Now().UnixMilli())
	for _, tc := range []struct {
		secret      string
		nodeID      int64
		wantInbound int64
	}{
		{rig.entrySec, rig.entryID, rig.inboundID},
		{rig.landingSec, rig.landingID, 0},
	} {
		if rec := rig.postBatch(t, tc.secret, 1, events); rec.Code != http.StatusOK {
			t.Fatalf("node %d ingest: status %d, body %s", tc.nodeID, rec.Code, rec.Body.String())
		}
		rows, err := rig.r.ListNodeConnLogs(tc.nodeID, "", 0, 10)
		if err != nil || len(rows) != 1 || rows[0].DisplayInboundID != tc.wantInbound {
			t.Fatalf("node %d rows = %+v, err %v, want one row with display inbound %d", tc.nodeID, rows, err, tc.wantInbound)
		}
	}
}
