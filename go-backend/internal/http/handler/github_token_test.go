package handler

import (
	"net/http"
	"testing"
)

func TestPublicDownloadsNeverReadDeploymentCredentials(t *testing.T) {
	t.Setenv("OVO_GITHUB_TOKEN", "synthetic-current")
	t.Setenv("FLMX_GITHUB_TOKEN", "synthetic-legacy")
	t.Setenv("GITHUB_TOKEN", "synthetic-general")
	if githubPATToken() != "" {
		t.Fatal("public downloads read a credential")
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/yhfc1775-maker/ovo/releases", nil)
	setGitHubPATAuthHeader(req)
	if req.Header.Get("Authorization") != "" {
		t.Fatal("public download sent Authorization")
	}
}
