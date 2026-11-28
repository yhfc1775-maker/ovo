package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"gorm.io/gorm"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func batch1List(t *testing.T, h *Handler, node bool, body string) (int, []map[string]interface{}) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/list", strings.NewReader(body))
	if node {
		h.nodeList(w, req)
	} else {
		h.inboundList(w, req)
	}
	var env struct {
		Code int                      `json:"code"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Code, env.Data
}
func batch1SeedLists(t *testing.T, r *repo.Repository, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		node := &model.Node{ID: int64(i), Name: fmt.Sprint("n", i), ServerIP: "203.0.113.1", Inx: i}
		if err := r.DB().Create(node).Error; err != nil {
			t.Fatal(err)
		}
		row := &model.NodeInbound{ID: int64(i), NodeID: node.ID, Name: fmt.Sprint("i", i), Protocol: kernel.ProtocolVLESS, Port: 10000 + i, Inx: i}
		if err := r.DB().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}
func TestBatch1ListPagination(t *testing.T) {
	h, r := newPanelsecHandler(t)
	batch1SeedLists(t, r, 6)
	// An invisible companion sorts first but must not consume a page slot.
	if err := r.DB().Create(&model.NodeInbound{ID: 100, NodeID: 1, Name: "hidden", CompanionOf: 1, Inx: -1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Model(&model.NodeInbound{}).Where("id = ?", 3).Update("outbound_id", 6).Error; err != nil {
		t.Fatal(err)
	}
	for _, node := range []bool{true, false} {
		for _, tc := range []struct {
			body string
			ids  []int
		}{
			{`{"current":2,"size":2}`, []int{3, 4}}, {`{}`, []int{1, 2, 3, 4, 5, 6}}, {``, []int{1, 2, 3, 4, 5, 6}},
			{`{"current":0,"size":0}`, []int{1, 2, 3, 4, 5, 6}}, {`{"current":1,"size":0}`, []int{1, 2, 3, 4, 5, 6}},
			{`{"current":50,"size":2}`, []int{}}, {fmt.Sprintf(`{"current":%d,"size":3}`, math.MaxInt), []int{}},
			{fmt.Sprintf(`{"current":1,"size":%d}`, math.MaxInt), []int{1, 2, 3, 4, 5, 6}},
		} {
			t.Run(fmt.Sprintf("node=%v/%s", node, tc.body), func(t *testing.T) {
				code, items := batch1List(t, h, node, tc.body)
				if code != 0 || len(items) != len(tc.ids) {
					t.Fatalf("code=%d items=%v", code, items)
				}
				if node && len(items) > 0 {
					if peers, ok := items[0]["nicSharedWith"].([]interface{}); !ok || len(peers) != 5 {
						t.Fatalf("cross-page NIC peers lost: %v", items[0])
					}
				}
				for i, id := range tc.ids {
					if int(items[i]["id"].(float64)) != id {
						t.Fatal(items)
					}
				}
				if !node && strings.Contains(tc.body, `"current":2,"size":2`) {
					if items[0]["outboundName"] != "i6" || items[0]["outboundMissing"] == true {
						t.Fatal(items[0])
					}
				}
			})
		}
		code, _ := batch1List(t, h, node, `{"current":"bad"}`)
		if code == 0 {
			t.Fatal("malformed page accepted")
		}
	}
	empty, er := newPanelsecHandler(t)
	_ = er
	for _, node := range []bool{true, false} {
		code, items := batch1List(t, empty, node, `{"current":1,"size":5}`)
		if code != 0 || len(items) != 0 {
			t.Fatalf("empty table %d %v", code, items)
		}
	}
}

func TestBatch1InboundNodeQueryBudget(t *testing.T) {
	for _, n := range []int{1, 40, 501} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			h, r := newPanelsecHandler(t)
			batch1SeedLists(t, r, n)
			queries, singles := 0, 0
			if err := r.DB().Callback().Query().Before("gorm:query").Register("batch1_nodes", func(tx *gorm.DB) {
				if tx.Statement.Table == "node" {
					queries++
				}
			}); err != nil {
				t.Fatal(err)
			}
			// Query callback after SQL construction distinguishes single-ID fallback.
			if err := r.DB().Callback().Query().After("gorm:query").Register("batch1_single", func(tx *gorm.DB) {
				if tx.Statement.Table == "node" && strings.Contains(tx.Statement.SQL.String(), "LIMIT 1") {
					singles++
				}
			}); err != nil {
				t.Fatal(err)
			}
			code, items := batch1List(t, h, false, `{}`)
			if code != 0 || len(items) != n || queries != (n+499)/500 || singles != 0 {
				t.Fatalf("n=%d code=%d rows=%d batches=%d singles=%d", n, code, len(items), queries, singles)
			}
			t.Logf("inbounds=%d node batch queries=%d single-node lookups=%d", n, queries, singles)
			if n == 501 {
				code, page := batch1List(t, h, false, `{"current":1,"size":9999}`)
				if code != 0 || len(page) != 500 {
					t.Fatalf("page size cap: code=%d size=%d", code, len(page))
				}
				code, nodes := batch1List(t, h, true, `{"current":1,"size":9999}`)
				if code != 0 || len(nodes) != 500 {
					t.Fatalf("node size cap: code=%d size=%d", code, len(nodes))
				}
			}
			r.DB().Callback().Query().Remove("batch1_nodes")
			r.DB().Callback().Query().Remove("batch1_single")
		})
	}
}
func TestBatch1InboundMissingNodeAndStorageFailure(t *testing.T) {
	h, r := newPanelsecHandler(t)
	batch1SeedLists(t, r, 1)
	if err := r.DB().Where("id = ?", 1).Delete(&model.Node{}).Error; err != nil {
		t.Fatal(err)
	}
	code, items := batch1List(t, h, false, `{}`)
	if code != 0 || len(items) != 1 || items[0]["nodeName"] != "" {
		t.Fatalf("missing node changed shape: %d %v", code, items)
	}
	if err := r.DB().Callback().Query().Before("gorm:query").Register("batch1_node_fault", func(tx *gorm.DB) {
		if tx.Statement.Table == "node" {
			tx.AddError(errors.New("batch1 private storage detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Query().Remove("batch1_node_fault")
	w := httptest.NewRecorder()
	h.inboundList(w, httptest.NewRequest(http.MethodPost, "/list", strings.NewReader(`{}`)))
	var env struct {
		Code int `json:"code"`
	}
	json.Unmarshal(w.Body.Bytes(), &env)
	if env.Code == 0 || strings.Contains(w.Body.String(), "private storage detail") {
		t.Fatalf("unsafe DB failure response: %s", w.Body.String())
	}
}
func TestBatch1NodeListQueryBudget(t *testing.T) {
	h, r := newPanelsecHandler(t)
	batch1SeedLists(t, r, 8)
	metrics := make([]*model.NodeMetric, 0, 80000)
	for id := int64(1); id <= 8; id++ {
		for j := int64(0); j < 10000; j++ {
			metrics = append(metrics, &model.NodeMetric{NodeID: id, Timestamp: time.Now().UnixMilli() + j})
		}
	}
	if err := r.InsertNodeMetricBatch(metrics); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, cb := range []string{"query", "row", "raw"} {
		f := func(tx *gorm.DB) {
			// GORM also invokes callbacks while compiling a subquery with
			// DryRun=true; count database executions, not SQL construction.
			if !tx.DryRun {
				counts[tx.Statement.Table]++
			}
		}
		switch cb {
		case "query":
			r.DB().Callback().Query().After("gorm:query").Register("batch1_count", f)
		case "row":
			r.DB().Callback().Row().After("gorm:row").Register("batch1_count", f)
		case "raw":
			r.DB().Callback().Raw().After("gorm:raw").Register("batch1_count", f)
		}
	}
	start := time.Now()
	code, items := batch1List(t, h, true, `{"current":1,"size":8}`)
	total := 0
	for _, n := range counts {
		total += n
	}
	if code != 0 || len(items) != 8 || total != 7 {
		t.Fatalf("code=%d rows=%d queries=%v", code, len(items), counts)
	}
	t.Logf("nodeList 8 nodes x 10000 samples: queries=%d by-table=%v elapsed=%s", total, counts, time.Since(start))
}
func TestBatch1MieruOldAgentInstallReceipt(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v3.40.0": http.StatusOK})
	h, r, mock, _, _, nodeID := setupKernelNotifyRig(t)
	mock.setApply("fail", "不支持的内核类型: mieru")
	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeMieru, "3.40.0")
	if code == 0 || !strings.Contains(msg, "节点 Agent 暂不支持 Mieru，请先升级") {
		t.Fatalf("code=%d receipt=%s", code, msg)
	}
	rows, err := r.ListNodeKernels(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.KernelType == kernel.TypeMieru {
			t.Fatalf("failed fresh install retained Mieru row: %+v", row)
		}
	}
}
