package download

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// B-1（2.1.26）：面板源线路升第一优先。公开仓库下直连与 9 个公共
// 代理匿名拉全 404，面板线路是唯一能成的腿——它必须先被单独
// 探测、准入即独占 stage 1，而不是像额外线路那样只在直连短路
// 失败后才进阶段 2 排名。直连/代理保留作后备。

func setPriorityShortCircuitGateForTest(t *testing.T, gate time.Duration) {
	t.Helper()
	old := DirectShortCircuitMaxLatency
	DirectShortCircuitMaxLatency = gate
	t.Cleanup(func() { DirectShortCircuitMaxLatency = old })
}

// 面板优先线路快速准入 -> 独占 stage 1，直连根本不被探测。
func TestPlanPriorityRouteWinsStage1WhenFast(t *testing.T) {
	var directHits, panelHits atomic.Int32
	direct := delayedServer(10*time.Millisecond, &directHits)
	defer direct.Close()
	panel := delayedServer(0, &panelHits)
	defer panel.Close()
	setMirrorPrefixes(t, "https://mirror.invalid")

	plan := NewPlanWithPriority(context.Background(), direct.URL+"/file.sha256", okProbe, Route{Prefix: panel.URL})
	routes := plan.Routes()
	if len(routes) != 1 || routes[0].Prefix != panel.URL {
		t.Fatalf("面板优先线路准入时计划应只有面板线路, got %v", routeNames(routes))
	}
	if directHits.Load() != 0 {
		t.Fatalf("面板优先线路准入后直连不得被探测, direct hits=%d", directHits.Load())
	}
	if panelHits.Load() != 1 {
		t.Fatalf("面板优先线路应被探测一次, panel hits=%d", panelHits.Load())
	}
}

// 面板优先线路已死（回 500，探测不准入）-> 回落既有直连短路
// 口径，计划只有直连；且优先线路确实被先探测过一次。
func TestPlanPriorityRouteDeadFallsBackToDirect(t *testing.T) {
	var panelHits atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panelHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // 探针要 200，500 = 不准入
	}))
	defer panel.Close()
	direct := delayedServer(10*time.Millisecond, nil)
	defer direct.Close()
	setMirrorPrefixes(t, "https://mirror.invalid")

	plan := NewPlanWithPriority(context.Background(), direct.URL+"/file.sha256", okProbe, Route{Prefix: panel.URL})
	routes := plan.Routes()
	if len(routes) != 1 || routes[0].Prefix != "" {
		t.Fatalf("面板优先线路已死时应回落直连短路, got %v", routeNames(routes))
	}
	if panelHits.Load() != 1 {
		t.Fatalf("面板优先线路应被探测一次（判死后才回落）, panel hits=%d", panelHits.Load())
	}
}

// 面板优先线路慢准入（>门）+ 直连已死 + 快镜像 -> 慢面板不独
// 占，按实测延迟进阶段 2 排名：快镜像 > 慢面板。
func TestPlanPriorityRouteSlowJoinsStage2Ranking(t *testing.T) {
	setPriorityShortCircuitGateForTest(t, 100*time.Millisecond)
	direct := delayedServer(0, nil)
	directURL := direct.URL
	direct.Close() // dead
	panel := delayedServer(300*time.Millisecond, nil)
	defer panel.Close()
	mirror := delayedServer(20*time.Millisecond, nil)
	defer mirror.Close()
	setMirrorPrefixes(t, mirror.URL)

	plan := NewPlanWithPriority(context.Background(), directURL+"/file.sha256", okProbe, Route{Prefix: panel.URL})
	routes := plan.Routes()
	if len(routes) != 2 {
		t.Fatalf("排名应含快镜像与慢面板两条, got %v", routeNames(routes))
	}
	if routes[0].Prefix != mirror.URL || routes[1].Prefix != panel.URL {
		t.Fatalf("排名应为 快镜像 > 慢面板, got %v", routeNames(routes))
	}
}

// 面板优先线路慢准入 + 直连已死 + 镜像全死 -> 慢面板是唯一准入
// 线路，阶段 2 只有它。
func TestPlanPriorityRouteSlowIsOnlyAdmittedRoute(t *testing.T) {
	setPriorityShortCircuitGateForTest(t, 100*time.Millisecond)
	direct := delayedServer(0, nil)
	directURL := direct.URL
	direct.Close() // dead
	panel := delayedServer(300*time.Millisecond, nil)
	defer panel.Close()
	setMirrorPrefixes(t, "https://mirror.invalid")

	plan := NewPlanWithPriority(context.Background(), directURL+"/file.sha256", okProbe, Route{Prefix: panel.URL})
	routes := plan.Routes()
	if len(routes) != 1 || routes[0].Prefix != panel.URL {
		t.Fatalf("慢面板为唯一准入线路时阶段 2 应只有它, got %v", routeNames(routes))
	}
}

// 面板优先线路慢准入 + 直连快速准入 -> 直连仍独占 stage 1；直
// 连中途失败补测时，慢面板应进阶段 2 排名（不得被遗忘）。
func TestPlanPriorityRouteSlowThenDirectFastShortCircuits(t *testing.T) {
	setPriorityShortCircuitGateForTest(t, 100*time.Millisecond)
	direct := delayedServer(10*time.Millisecond, nil)
	defer direct.Close()
	panel := delayedServer(300*time.Millisecond, nil)
	defer panel.Close()
	mirror := delayedServer(20*time.Millisecond, nil)
	defer mirror.Close()
	setMirrorPrefixes(t, mirror.URL)

	plan := NewPlanWithPriority(context.Background(), direct.URL+"/file.sha256", okProbe, Route{Prefix: panel.URL})
	routes := plan.Routes()
	if len(routes) != 1 || routes[0].Prefix != "" {
		t.Fatalf("直连快速准入时 stage 1 应只有直连, got %v", routeNames(routes))
	}
	if !plan.Advance() {
		t.Fatal("直连失败后计划应能补测进入阶段 2")
	}
	routes = plan.Routes()
	found := false
	for _, r := range routes {
		if r.Prefix == panel.URL {
			found = true
		}
	}
	if !found {
		t.Fatalf("补测后的阶段 2 应含慢准入的面板线路, got %v", routeNames(routes))
	}
}

// 全部探测皆死 -> 静态尾部里面板优先线路紧随直连、在镜像之前。
func TestPlanPriorityRouteInStaticTailAfterDirect(t *testing.T) {
	dead := delayedServer(0, nil)
	deadURL := dead.URL
	dead.Close()
	setMirrorPrefixes(t, "https://m1.invalid", "https://m2.invalid")

	plan := NewPlanWithPriority(context.Background(), "http://127.0.0.1:1/file.sha256", okProbe, Route{Prefix: deadURL})
	var attempted []string
	_, err := TryPlan(plan, "https://github.com/x/y", func(route Route, url string) error {
		attempted = append(attempted, route.Prefix)
		return errors.New("dial failed")
	})
	if err == nil {
		t.Fatal("全部线路失败必须报错")
	}
	want := []string{"", deadURL, "https://m1.invalid", "https://m2.invalid"}
	if len(attempted) != len(want) {
		t.Fatalf("静态尾部应逐个试完 %v, 实际 %v", want, attempted)
	}
	for i := range want {
		if attempted[i] != want[i] {
			t.Fatalf("静态尾部顺序应为 直连 > 面板 > 镜像, got %v", attempted)
		}
	}
}

// 优先线路的过滤：空前缀（与直连重复）、与内置镜像同前缀、
// 与额外线路重复，一律剔除，静态尾部不出现重复项。
func TestPlanPriorityRouteFiltering(t *testing.T) {
	setMirrorPrefixes(t, "https://m1.example", "https://m2.example")

	plan := NewPlanWithPriority(context.Background(), "", nil, Route{Prefix: ""})
	routes := plan.Routes() // 无探针 -> 静态尾部
	want := []string{"", "https://m1.example", "https://m2.example"}
	if len(routes) != len(want) {
		t.Fatalf("空优先线路应被剔除，静态尾部 = %v, want %v", routeNames(routes), want)
	}

	plan = NewPlanWithPriority(context.Background(), "", nil, Route{Prefix: "https://m1.example"})
	routes = plan.Routes()
	if len(routes) != len(want) {
		t.Fatalf("与内置镜像同前缀的优先线路应被剔除，静态尾部 = %v, want %v", routeNames(routes), want)
	}

	plan = NewPlanWithPriority(context.Background(), "", nil,
		Route{Prefix: "https://panel.example"},
		Route{Prefix: "https://panel.example"},
	)
	routes = plan.Routes()
	want = []string{"", "https://panel.example", "https://m1.example", "https://m2.example"}
	if len(routes) != len(want) {
		t.Fatalf("与额外线路重复的优先线路应只出现一次，静态尾部 = %v, want %v", routeNames(routes), want)
	}
	for i := range want {
		if routes[i].Prefix != want[i] {
			t.Fatalf("静态尾部 = %v, want %v", routeNames(routes), want)
		}
	}
}
