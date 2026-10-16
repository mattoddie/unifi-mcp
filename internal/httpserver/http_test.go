package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/tools"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

func newServer(t *testing.T, cfg config.HTTP) *httptest.Server {
	t.Helper()
	m := unifitest.New(unifitest.Options{Mode: "unifi-os", APIKey: "key123"})
	t.Cleanup(m.Close)
	client, err := unifi.New(config.Unifi{URL: m.URL, APIKey: "key123", Site: "default", TimeoutMs: 5000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	tl := config.AllTools()
	tl.AllowWrites, tl.AllowDeletes = false, false
	srv := httptest.NewServer(Handler(cfg, tools.NewServer(client, tl, nil)))
	t.Cleanup(srv.Close)
	return srv
}

const listTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

func send(t *testing.T, method, url, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestHealthAndNotFound(t *testing.T) {
	srv := newServer(t, config.HTTP{AuthToken: "x"})
	for _, p := range []string{"/health", "/healthz"} {
		res, body := send(t, "GET", srv.URL+p, "", nil)
		if res.StatusCode != 200 || body != `{"status":"ok"}` || res.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: %d %s", p, res.StatusCode, body)
		}
	}
	res, body := send(t, "GET", srv.URL+"/other", "", nil)
	if res.StatusCode != 404 || body != `{"error":"Not found. The MCP endpoint is /mcp"}` {
		t.Errorf("other: %d %s", res.StatusCode, body)
	}
}

func TestBearerAuth(t *testing.T) {
	srv := newServer(t, config.HTTP{AuthToken: "s3cret"})
	res, body := send(t, "POST", srv.URL+"/mcp", listTools, nil)
	if res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") != "Bearer" || body != `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Unauthorized"},"id":null}` {
		t.Errorf("no token: %d %s", res.StatusCode, body)
	}
	for _, h := range []string{"Bearer wrong", "Bearer s3cret2", "Basic s3cret", "s3cret"} {
		if res, _ := send(t, "POST", srv.URL+"/mcp", listTools, map[string]string{"Authorization": h}); res.StatusCode != 401 {
			t.Errorf("%q: %d", h, res.StatusCode)
		}
	}
	for _, h := range []string{"Bearer s3cret", "bearer s3cret", "BEARER   s3cret"} {
		res, body := send(t, "POST", srv.URL+"/mcp", listTools, map[string]string{"Authorization": h})
		if res.StatusCode != 200 {
			t.Errorf("%q: %d", h, res.StatusCode)
		}
		var out struct {
			Result struct {
				Tools []struct{ Name string } `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Result.Tools) != 54 {
			t.Errorf("tools/list: %v %d", err, len(out.Result.Tools))
		}
	}
}

func TestMethodsAndBodies(t *testing.T) {
	srv := newServer(t, config.HTTP{AuthToken: "x"})
	auth := map[string]string{"Authorization": "Bearer x"}
	for _, m := range []string{"GET", "DELETE"} {
		res, body := send(t, m, srv.URL+"/mcp", "", auth)
		if res.StatusCode != 405 || res.Header.Get("Allow") != "POST" || !strings.Contains(body, "Method not allowed") {
			t.Errorf("%s: %d %s", m, res.StatusCode, body)
		}
	}
	if res, body := send(t, "POST", srv.URL+"/mcp", "{not json", auth); res.StatusCode != 400 || !strings.Contains(body, "Invalid JSON body") {
		t.Errorf("bad JSON: %d %s", res.StatusCode, body)
	}
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"pad":"` + strings.Repeat("a", MaxBodyBytes) + `"}}}`
	if res, body := send(t, "POST", srv.URL+"/mcp", big, auth); res.StatusCode != 400 || !strings.Contains(body, "Request body too large") {
		t.Errorf("large body: %d %s", res.StatusCode, body)
	}
	near := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"pad":"` + strings.Repeat("a", 3<<20) + `"}}}`
	if res, _ := send(t, "POST", srv.URL+"/mcp", near, auth); res.StatusCode != 200 {
		t.Errorf("3 MiB body: %d", res.StatusCode)
	}
}

func TestAllowedHostsAndOrigins(t *testing.T) {
	srv := newServer(t, config.HTTP{AuthToken: "s3cret", AllowedHosts: []string{"127.0.0.1", "mcp.example.com"}})
	auth := map[string]string{"Authorization": "Bearer s3cret"}
	with := func(kv ...string) map[string]string {
		h := map[string]string{"Authorization": "Bearer s3cret"}
		for i := 0; i+1 < len(kv); i += 2 {
			h[kv[i]] = kv[i+1]
		}
		return h
	}
	if res, _ := send(t, "POST", srv.URL+"/mcp", listTools, auth); res.StatusCode != 200 {
		t.Errorf("listed host: %d", res.StatusCode)
	}
	if res, body := send(t, "POST", srv.URL+"/mcp", listTools, with("Host", "EVIL.example:80")); res.StatusCode != 403 || !strings.Contains(body, "Host evil.example:80 is not allowed") {
		t.Errorf("unlisted host: %d %s", res.StatusCode, body)
	}
	if res, _ := send(t, "POST", srv.URL+"/mcp", listTools, with("Host", "mcp.example.com:443")); res.StatusCode != 200 {
		t.Errorf("listed host with port: %d", res.StatusCode)
	}
	for origin, want := range map[string]int{
		srv.URL:                          200,
		"http://127.0.0.1":               200,
		"https://MCP.example.com:443":    200,
		"https://mcp.example.com:8443":   200,
		"http://evil.example":            403,
		"null":                           403,
		"file://":                        403,
		"http://127.0.0.1.evil.example":  403,
		"ftp://127.0.0.1":                403,
		"https://mcp.example.com.evil.n": 403,
	} {
		res, body := send(t, "POST", srv.URL+"/mcp", listTools, with("Origin", origin))
		if res.StatusCode != want {
			t.Errorf("Origin %q: %d, want %d", origin, res.StatusCode, want)
		}
		if want == 403 && !strings.Contains(body, "Origin "+origin+" is not allowed") {
			t.Errorf("Origin %q body: %s", origin, body)
		}
	}
	// Host and Origin are checked before the token, as before.
	if res, _ := send(t, "POST", srv.URL+"/mcp", listTools, map[string]string{"Origin": "http://evil.example"}); res.StatusCode != 403 {
		t.Errorf("origin before auth: %d", res.StatusCode)
	}
}

func TestUnauthenticatedOnlyWhenAllowed(t *testing.T) {
	if err := Check(config.HTTP{}); err == nil || err.Error() != "MCP_AUTH_TOKEN is required for the http transport (or set MCP_ALLOW_UNAUTHENTICATED=true)" {
		t.Errorf("Check: %v", err)
	}
	if err := Check(config.HTTP{AllowUnauthenticated: true}); err != nil {
		t.Error(err)
	}
	srv := newServer(t, config.HTTP{AllowUnauthenticated: true})
	if res, _ := send(t, "POST", srv.URL+"/mcp", listTools, nil); res.StatusCode != 200 {
		t.Errorf("unauthenticated: %d", res.StatusCode)
	}
	// Without MCP_ALLOWED_HOSTS any Host is accepted, as before (no built-in localhost rebinding check).
	if res, _ := send(t, "POST", srv.URL+"/mcp", listTools, map[string]string{"Host": "proxy.example"}); res.StatusCode != 200 {
		t.Errorf("other host: %d", res.StatusCode)
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestEndToEndWithStreamableClient(t *testing.T) {
	srv := newServer(t, config.HTTP{AuthToken: "s3cret"})
	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{"s3cret"}}}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	n := 0
	for _, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n <= 20 {
		t.Errorf("tools: %d", n)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "unifi_list_sites", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %v", err, res)
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, `"name": "default"`) {
		t.Errorf("sites: %s", txt)
	}
}

func TestTokenMatches(t *testing.T) {
	for header, want := range map[string]bool{"Bearer abc": true, "bearer abc": true, "Bearer\tabc": true, "Bearer abcd": false, "Bearer": false, "": false, "Bearer abc\n": false} {
		if tokenMatches(header, "abc") != want {
			t.Errorf("%q", header)
		}
	}
}
