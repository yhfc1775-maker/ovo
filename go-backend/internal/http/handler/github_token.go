package handler

import "net/http"

// Public releases and packages never read deployment credentials.
func githubPATToken() string                   { return "" }
func setGitHubPATAuthHeader(req *http.Request) {}
func githubPATGitHubHost(host string) bool     { return false }
