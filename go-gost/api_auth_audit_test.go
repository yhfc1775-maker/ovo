package main

import (
	"github.com/go-gost/x/config"
	"testing"
)

func TestAuditManagementAPIRequiresConfiguredAuthenticator(t *testing.T) {
	for _, cfg := range []*config.APIConfig{nil, {Addr: "127.0.0.1:0"}, {Addr: "127.0.0.1:0", Auth: &config.AuthConfig{}}, {Addr: "127.0.0.1:0", Auth: &config.AuthConfig{Username: "admin"}}, {Addr: "127.0.0.1:0", Auther: "audit-nonexistent"}, {Addr: "127.0.0.1:0", Auther: "audit-nonexistent", Auth: &config.AuthConfig{Username: "admin", Password: "fixture"}}} {
		svc, err := buildApiService(cfg)
		if err == nil {
			if svc != nil {
				svc.Close()
			}
			t.Fatal("unconfigured API started")
		}
	}
	svc, err := buildApiService(&config.APIConfig{Addr: "127.0.0.1:0", Auth: &config.AuthConfig{Username: "admin", Password: "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
}
