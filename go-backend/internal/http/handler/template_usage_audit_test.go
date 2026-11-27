package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"gorm.io/gorm"
)

func TestAudit2131TemplateUsageReadFailureFamily(t *testing.T) {
	for _, mode := range []string{"delete", "kind", "metadata", "list", "refresh"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
			h, r := tplTestHandler(t)
			var hits atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); fmt.Fprint(w, tplTestClash) }))
			defer server.Close()
			tpl := &model.SubscriptionTemplate{Name: "usage-before", Kind: "clash", Content: tplTestClash, SourceURL: server.URL, CreatedTime: time.Now().UnixMilli()}
			if err := r.SaveSubscriptionTemplate(tpl); err != nil {
				t.Fatal(err)
			}
			sub := &model.Subscription{Name: "usage-sub", Kind: model.SubscriptionShare, Token: strings.Repeat("d", 32), Options: `{"templates":{"clash":` + tplIDStr(tpl.ID) + `}}`, Enabled: 1}
			if err := r.SaveSubscription(sub); err != nil {
				t.Fatal(err)
			}
			if err := r.DB().Callback().Query().Before("gorm:query").Register("audit2131_usage_read_error", func(tx *gorm.DB) {
				if tx.Statement.Table == "subscription" {
					tx.AddError(fmt.Errorf("fixture subscription read unavailable"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer r.DB().Callback().Query().Remove("audit2131_usage_read_error")
			var env map[string]interface{}
			switch mode {
			case "delete":
				_, env = tplPost(t, h.subscriptionTemplateDelete, `{"id":`+tplIDStr(tpl.ID)+`}`)
			case "kind":
				_, env = tplPost(t, h.subscriptionTemplateSave, `{"id":`+tplIDStr(tpl.ID)+`,"name":"usage-after","kind":"singbox","content":"{\"outbounds\":[]}"}`)
			case "metadata":
				_, env = tplPost(t, h.subscriptionTemplateSave, `{"id":`+tplIDStr(tpl.ID)+`,"name":"usage-after","kind":"clash","content":`+tplJSONStr(tplTestClash)+`}`)
			case "list":
				_, env = tplPost(t, h.subscriptionTemplateList, `{}`)
			case "refresh":
				_, env = tplPost(t, h.subscriptionTemplateRefresh, `{"id":`+tplIDStr(tpl.ID)+`}`)
			}
			if env["code"] == float64(0) {
				t.Errorf("%s treated unreadable usage as unused/success: %v", mode, env)
			}
			after, err := r.GetSubscriptionTemplate(tpl.ID)
			if err != nil || after == nil || after.Name != tpl.Name || after.Kind != tpl.Kind || after.Content != tpl.Content || after.LastFetch != tpl.LastFetch {
				t.Errorf("%s mutated template while usage was unknown: %+v %v", mode, after, err)
			}
			if hits.Load() != 0 {
				t.Errorf("%s downloaded before usage safety read succeeded", mode)
			}
			if strings.Contains(fmt.Sprint(env["msg"]), "fixture") {
				t.Error("internal repository detail leaked")
			}
		})
	}
}

func TestAudit2131SelectedTemplateCannotSilentlyFallBack(t *testing.T) {
	for _, mode := range []string{"read-error", "missing", "wrong-kind"} {
		t.Run(mode, func(t *testing.T) {
			h, r, _ := subFixture(t)
			tpl := &model.SubscriptionTemplate{Name: "selected-policy", Kind: "clash", Content: tplTestClash}
			if err := r.SaveSubscriptionTemplate(tpl); err != nil {
				t.Fatal(err)
			}
			sub := createShareSub(t, r, &model.Subscription{Name: "template-policy", Kind: model.SubscriptionShare, Token: "audit2131-selected-" + mode, SourceAll: 1, Enabled: 1, Options: `{"templates":{"clash":` + tplIDStr(tpl.ID) + `}}`})
			if status, body := fetchSub(t, h, sub.Token, "clash-verge/1.0"); status != 200 {
				t.Fatalf("healthy selected template control: %d %s", status, body)
			}
			switch mode {
			case "read-error":
				if err := r.DB().Callback().Query().Before("gorm:query").Register("audit2131_template_read_error", func(tx *gorm.DB) {
					if tx.Statement.Table == "subscription_template" {
						tx.AddError(fmt.Errorf("fixture selected template unavailable"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer r.DB().Callback().Query().Remove("audit2131_template_read_error")
			case "missing":
				if err := r.DeleteSubscriptionTemplate(tpl.ID); err != nil {
					t.Fatal(err)
				}
			case "wrong-kind":
				tpl.Kind = "singbox"
				if err := r.SaveSubscriptionTemplate(tpl); err != nil {
					t.Fatal(err)
				}
			}
			if status, body := fetchSub(t, h, sub.Token, "clash-verge/1.0"); status != 500 {
				t.Errorf("selected template %s silently produced fallback: %d %.180s", mode, status, body)
			} else if strings.Contains(body, "fixture") {
				t.Error("repository detail leaked")
			}
			// A format without this template must keep working.
			if status, body := fetchSub(t, h, sub.Token, "v2rayN/6.0"); status != 200 {
				t.Errorf("unrelated format broken: %d %s", status, body)
			}
			sub.Enabled = 0
			if err := r.SaveSubscription(sub); err != nil {
				t.Fatal(err)
			}
			if status, body := fetchSub(t, h, sub.Token, "clash-verge/1.0"); status != 200 {
				t.Errorf("inactive notice broken: %d %s", status, body)
			}
		})
	}
}

func TestAudit2131TemplateSelectionReadFailureIsNotMissing(t *testing.T) {
	h, r, _ := subFixture(t)
	tpl := &model.SubscriptionTemplate{Name: "selection-check", Kind: "clash", Content: tplTestClash}
	if err := r.SaveSubscriptionTemplate(tpl); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Callback().Query().Before("gorm:query").Register("audit2131_selection_read_error", func(tx *gorm.DB) {
		if tx.Statement.Table == "subscription_template" {
			tx.AddError(fmt.Errorf("fixture template selection read unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Query().Remove("audit2131_selection_read_error")
	_, env := tplPost(t, h.subscriptionSave, `{"name":"selection-check-sub","kind":"share","sourceAll":true,"enabled":true,"options":{"templates":{"clash":`+tplIDStr(tpl.ID)+`}}}`)
	if env["code"] == float64(0) || strings.Contains(fmt.Sprint(env["msg"]), "不存在") || strings.Contains(fmt.Sprint(env["msg"]), "fixture") {
		t.Fatalf("repository failure misclassified as missing/success/leaked detail: %v", env)
	}
}

func TestAudit2131TemplateLookupReadFailureFamily(t *testing.T) {
	for _, mode := range []string{"save", "refresh"} {
		t.Run(mode, func(t *testing.T) {
			h, r := tplTestHandler(t)
			tpl := &model.SubscriptionTemplate{Name: "lookup-before", Kind: "clash", Content: tplTestClash, SourceURL: "https://example.invalid/profile"}
			if err := r.SaveSubscriptionTemplate(tpl); err != nil {
				t.Fatal(err)
			}
			if err := r.DB().Callback().Query().Before("gorm:query").Register("audit2131_lookup_read_error", func(tx *gorm.DB) {
				if tx.Statement.Table == "subscription_template" {
					tx.AddError(fmt.Errorf("fixture template lookup read unavailable"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer r.DB().Callback().Query().Remove("audit2131_lookup_read_error")
			var env map[string]interface{}
			if mode == "save" {
				_, env = tplPost(t, h.subscriptionTemplateSave, `{"id":`+tplIDStr(tpl.ID)+`,"name":"lookup-after","kind":"clash","content":`+tplJSONStr(tplTestClash)+`}`)
			} else {
				_, env = tplPost(t, h.subscriptionTemplateRefresh, `{"id":`+tplIDStr(tpl.ID)+`}`)
			}
			if env["code"] == float64(0) || strings.Contains(fmt.Sprint(env["msg"]), "不存在") || strings.Contains(fmt.Sprint(env["msg"]), "fixture") {
				t.Errorf("%s mislabeled repository failure: %v", mode, env)
			}
		})
	}
}
