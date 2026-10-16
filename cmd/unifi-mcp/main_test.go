package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	t.Setenv("MCP_TRANSPORT", "stdio")
	if err := healthcheck(); err != nil {
		t.Errorf("stdio: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("MCP_TRANSPORT", "http")
	t.Setenv("MCP_HTTP_PORT", port)
	if err := healthcheck(); err != nil {
		t.Errorf("http: %v", err)
	}
	srv.Close()
	if err := healthcheck(); err == nil {
		t.Error("a stopped server passed the health check")
	}
}

func TestServeRefusesHTTPWithoutToken(t *testing.T) {
	t.Setenv("UNIFI_URL", "https://192.0.2.1")
	t.Setenv("UNIFI_API_KEY", "k")
	t.Setenv("MCP_TRANSPORT", "http")
	t.Setenv("MCP_AUTH_TOKEN", "")
	t.Setenv("MCP_ALLOW_UNAUTHENTICATED", "")
	if err := serve(); err == nil || !strings.Contains(err.Error(), "MCP_AUTH_TOKEN is required") {
		t.Errorf("serve: %v", err)
	}
}
