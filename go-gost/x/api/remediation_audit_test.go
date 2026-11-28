package api

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/x/config"
	authparser "github.com/go-gost/x/config/parsing/auth"
	"github.com/go-gost/x/registry"
)

func auditAPIRequest(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestAuditAPIAuthenticationFailsClosed(t *testing.T) {
	for _, configured := range []bool{false, true} {
		r := gin.New()
		opts := &Options{}
		if configured {
			opts.Auther = authparser.ParseAutherFromAuth(&config.AuthConfig{Username: "admin", Password: "synthetic-password"})
		}
		Register(r, opts)
		for _, credentials := range []string{"missing", "empty", "wrong", "correct"} {
			req := httptest.NewRequest(http.MethodGet, "/config", nil)
			if credentials != "missing" {
				password := "wrong"
				if credentials == "empty" {
					password = ""
				}
				if credentials == "correct" {
					password = "synthetic-password"
				}
				req.SetBasicAuth("admin", password)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			allowed := configured && credentials == "correct"
			if (w.Code == 200) != allowed {
				t.Fatalf("configured=%v credentials=%s status=%d", configured, credentials, w.Code)
			}
		}
	}
}

func TestAuditNamedConfigPersistenceFailureMatrix(t *testing.T) {
	for _, tc := range r18F04Cases {
		for _, operation := range []string{"create", "update", "delete"} {
			t.Run(tc.kind+"/"+operation, func(t *testing.T) {
				r := r19ApiRouter()
				name := "audit-" + tc.kind
				tc.unregister(name)
				t.Cleanup(func() { tc.unregister(name) })
				disk := r18SeedConfig(t, &config.Config{})
				if operation != "create" {
					if w := auditAPIRequest(r, "POST", tc.path, `{"name":"`+name+`"}`); w.Code != 200 {
						t.Fatalf("seed failed %d %s", w.Code, w.Body.String())
					}
				} else {
					if err := config.PersistNow(config.Global()); err != nil {
						t.Fatal(err)
					}
				}
				before := config.Global()
				original, err := os.ReadFile(disk)
				if err != nil {
					t.Fatal(err)
				}
				config.SetPersistPath(filepath.Join(t.TempDir(), "missing", "gost.json"))
				method, path := "POST", tc.path
				if operation == "update" {
					method = "PUT"
					path += "/" + name
				}
				if operation == "delete" {
					method = "DELETE"
					path += "/" + name
				}
				w := auditAPIRequest(r, method, path, `{"name":"`+name+`","metadata":{"audit":true}}`)
				if w.Code < 400 {
					t.Fatalf("failed persistence returned success: %d", w.Code)
				}
				if !reflect.DeepEqual(before, config.Global()) {
					t.Fatal("in-memory config changed")
				}
				current, err := os.ReadFile(disk)
				if err != nil || !bytes.Equal(current, original) {
					t.Fatal("persisted config changed")
				}
				// Restore persistence and prove the old registry membership remains usable.
				config.SetPersistPath(disk)
				if operation == "create" {
					if w := auditAPIRequest(r, "POST", tc.path, `{"name":"`+name+`"}`); w.Code != 200 {
						t.Fatal("failed create leaked registry entry")
					}
				} else {
					if w := auditAPIRequest(r, "PUT", tc.path+"/"+name, `{"name":"`+name+`"}`); w.Code != 200 {
						t.Fatalf("failed mutation lost old registry entry: %d", w.Code)
					}
				}
			})
		}
	}
}

func TestAuditServicePersistenceFailureMatrix(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete", "pause", "resume"} {
		for _, batch := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/single", true: "/batch"}[batch], func(t *testing.T) {
				r := r19ApiRouter()
				name := "audit-service"
				registry.ServiceRegistry().Unregister(name)
				t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })
				disk := r18SeedConfig(t, &config.Config{})
				if operation != "create" {
					w := auditAPIRequest(r, "POST", "/config/services", `{"name":"`+name+`","addr":"127.0.0.1:0"}`)
					if w.Code != 200 {
						t.Fatalf("seed=%s", w.Body.String())
					}
					if operation == "resume" {
						w = auditAPIRequest(r, "POST", "/config/services/"+name+"/pause", "")
						if w.Code != 200 {
							t.Fatalf("pause seed=%s", w.Body.String())
						}
					}
				} else {
					if err := config.PersistNow(config.Global()); err != nil {
						t.Fatal(err)
					}
				}
				before := config.Global()
				original, err := os.ReadFile(disk)
				if err != nil {
					t.Fatal(err)
				}
				method, path := "POST", "/config/services"
				body := `{"name":"` + name + `","addr":"127.0.0.1:0","metadata":{"new":true}}`
				if operation == "update" {
					method = "PUT"
				}
				if operation == "delete" {
					method = "DELETE"
				}
				if operation != "create" {
					path += "/" + name
				}
				if operation == "pause" || operation == "resume" {
					path += "/" + operation
				}
				if batch {
					path = "/config/services/batch"
					if operation == "pause" || operation == "resume" {
						path += "/" + operation
					}
					if operation == "create" || operation == "update" {
						body = "[" + body + "]"
					} else {
						body = `{"services":["` + name + `"]}`
					}
				}
				config.SetPersistPath(filepath.Join(t.TempDir(), "missing", "gost.json"))
				w := auditAPIRequest(r, method, path, body)
				if w.Code < 400 {
					t.Fatalf("persist error returned success: %d %s", w.Code, w.Body.String())
				}
				if !reflect.DeepEqual(before, config.Global()) {
					t.Fatal("config changed on failure")
				}
				current, err := os.ReadFile(disk)
				if err != nil || !bytes.Equal(current, original) {
					t.Fatal("disk changed on failure")
				}
				svc := registry.ServiceRegistry().Get(name)
				if operation == "create" {
					if svc != nil {
						t.Fatal("failed create left listener")
					}
				} else if operation != "resume" {
					if svc == nil {
						t.Fatal("old runtime missing")
					}
					conn, err := net.DialTimeout("tcp", svc.Addr().String(), time.Second)
					if err != nil {
						t.Fatalf("old listener not restored: %v", err)
					}
					conn.Close()
				}
			})
		}
	}
}

func TestAuditConfigReadDoesNotPersist(t *testing.T) {
	r := gin.New()
	r.GET("/config", getConfig)
	disk := r18SeedConfig(t, &config.Config{})
	if err := config.PersistNow(config.Global()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(disk)
	if err != nil {
		t.Fatal(err)
	}
	config.SetPersistPath(filepath.Join(t.TempDir(), "missing", "gost.json"))
	w := auditAPIRequest(r, "GET", "/config", "")
	if w.Code != 200 {
		t.Fatalf("read=%d", w.Code)
	}
	var decoded config.Config
	if err = json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(disk)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read persisted config")
	}
}
