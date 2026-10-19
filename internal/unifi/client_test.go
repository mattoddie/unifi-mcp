package unifi_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

var ctx = context.Background()

func newClient(t *testing.T, env map[string]string) *unifi.Client {
	t.Helper()
	cfg, err := config.Load(config.MapEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	c, err := unifi.New(cfg.Unifi)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func userEnv(url string, extra ...string) map[string]string {
	env := map[string]string{"UNIFI_URL": url, "UNIFI_USERNAME": "admin", "UNIFI_PASSWORD": "secret"}
	for i := 0; i+1 < len(extra); i += 2 {
		env[extra[i]] = extra[i+1]
	}
	return env
}

func TestUnifiOSWithUsernamePassword(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "unifi-os"})
	defer m.Close()
	c := newClient(t, userEnv(m.URL))

	// Detects UniFi OS, logs in and prefixes /proxy/network.
	sites, err := c.Request(ctx, "GET", "/api/self/sites", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ojson.Get(sites.([]any)[0], "name") != "default" {
		t.Errorf("sites: %v", sites)
	}
	var paths []string
	for _, r := range m.Requests() {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, " ") != "/ /api/auth/login /proxy/network/api/self/sites" {
		t.Errorf("requests: %v", paths)
	}
	login := m.Requests()[1].Body
	if s, _ := ojson.Stringify(login); s != `{"username":"admin","password":"secret","remember":true}` {
		t.Errorf("login body %s", s)
	}

	// Sends the CSRF token on writes.
	if _, err := c.Cmd(ctx, "default", "stamgr", "block-sta", ojson.New("mac", "11:22:33:44:55:66")); err != nil {
		t.Fatal(err)
	}
	last := m.Last()
	if last.Header.Get("X-CSRF-Token") != "csrf-tok1" || last.Header.Get("Content-Type") != "application/json" || last.Header.Get("Accept") != "application/json" {
		t.Errorf("headers %v", last.Header)
	}
	if s, _ := ojson.Stringify(last.Body); s != `{"cmd":"block-sta","mac":"11:22:33:44:55:66"}` {
		t.Errorf("body %s", s)
	}

	// Re-authenticates once when the session expires.
	m.ClearSessions()
	devices, err := c.SiteGet(ctx, "default", "stat/device", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := ojson.Len(devices); n != 2 || m.LoginCount() != 2 {
		t.Errorf("devices %d, logins %d", n, m.LoginCount())
	}

	// Surfaces controller error messages.
	if _, err := c.SiteGet(ctx, "missing", "stat/health", nil); err == nil || err.Error() != "GET /api/s/missing/stat/health failed with HTTP 400: api.err.NoSiteContext" {
		t.Errorf("error: %v", err)
	}
	if _, err := c.SiteGet(ctx, "default", "nope", nil); err == nil || !strings.Contains(err.Error(), "failed with HTTP 404: no mock for GET /api/s/default/nope (endpoint not found; it may not exist on this Network application version)") {
		t.Errorf("404 error: %v", err)
	}
	if e, ok := func() (*unifi.Error, bool) {
		_, err := c.SiteGet(ctx, "missing", "stat/health", nil)
		ue, ok := err.(*unifi.Error)
		return ue, ok
	}(); !ok || e.Status != 400 {
		t.Errorf("error type %#v", e)
	}

	// Returns unwrapped v2 responses.
	rules, err := c.V2(ctx, "GET", "default", "trafficrules", ojson.Undefined)
	if err != nil || ojson.Get(rules.([]any)[0], "_id") != "t1" {
		t.Errorf("v2: %v %v", rules, err)
	}
}

func TestLegacyController(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "legacy"})
	defer m.Close()
	c := newClient(t, userEnv(m.URL))
	if _, err := c.Cmd(ctx, "default", "devmgr", "restart", ojson.New("mac", "aa:bb:cc:00:00:01")); err != nil {
		t.Fatal(err)
	}
	if m.Requests()[1].Path != "/api/login" {
		t.Errorf("login path %s", m.Requests()[1].Path)
	}
	last := m.Last()
	if last.Path != "/api/s/default/cmd/devmgr" || last.Header.Get("X-CSRF-Token") != "csrf-tok1" {
		t.Errorf("legacy request %s %v", last.Path, last.Header)
	}
	if !strings.Contains(last.Header.Get("Cookie"), "unifises=tok1") || !strings.Contains(last.Header.Get("Cookie"), "csrf_token=csrf-tok1") {
		t.Errorf("cookies %q", last.Header.Get("Cookie"))
	}

	bad := newClient(t, userEnv(m.URL, "UNIFI_PASSWORD", "wrong"))
	if _, err := bad.SiteGet(ctx, "default", "stat/device", nil); err == nil || !strings.HasPrefix(err.Error(), "Login to UniFi controller failed (HTTP 401): ") {
		t.Errorf("bad credentials: %v", err)
	}
}

func TestMFAAccountsAreExplained(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "unifi-os"})
	defer m.Close()
	m.On("POST", `^/api/auth/login$`, func(any, []string, url.Values) (int, any) {
		return 499, ojson.New("errors", []any{"ubic_2fa_token required"})
	})
	c := newClient(t, userEnv(m.URL, "UNIFI_CONTROLLER_TYPE", "unifi-os"))
	_, err := c.SiteGet(ctx, "default", "stat/device", nil)
	if err == nil || err.Error() != "Login to UniFi controller failed (HTTP 499): the account requires multi-factor authentication; use a local-only admin account or an API key" {
		t.Errorf("error: %v", err)
	}
}

func TestParallelRequestsLogInOnce(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "unifi-os"})
	defer m.Close()
	m.Delay(`^/api/auth/login$`, 50*time.Millisecond)
	c := newClient(t, userEnv(m.URL))
	run := func(n int) {
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := c.SiteGet(ctx, "default", "stat/device", nil)
				if err == nil {
					if l, _ := ojson.Len(res); l != 2 {
						err = &unifi.Error{Message: "wrong result"}
					}
				}
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
	}
	run(8)
	if m.LoginCount() != 1 {
		t.Errorf("logins after first batch: %d", m.LoginCount())
	}
	m.ClearSessions()
	run(8)
	if m.LoginCount() != 2 {
		t.Errorf("logins after expiry: %d", m.LoginCount())
	}
}

func TestStaleFailureKeepsFreshSession(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "unifi-os"})
	defer m.Close()
	c := newClient(t, userEnv(m.URL))
	if _, err := c.SiteGet(ctx, "default", "stat/device", nil); err != nil {
		t.Fatal(err)
	}
	m.Delay(`^/api/s/default/stat/sta$`, 250*time.Millisecond)
	logins := m.LoginCount()
	m.ClearSessions()
	// Both requests go out with the expired session. The fast one re-logs in; the
	// slow one's 401 arrives afterwards and must reuse the new session.
	var wg sync.WaitGroup
	var devices, clients any
	var errD, errC error
	wg.Add(2)
	go func() { defer wg.Done(); devices, errD = c.SiteGet(ctx, "default", "stat/device", nil) }()
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond) // make sure it is sent with the old session too
		clients, errC = c.SiteGet(ctx, "default", "stat/sta", nil)
	}()
	wg.Wait()
	if errD != nil || errC != nil {
		t.Fatal(errD, errC)
	}
	if n, _ := ojson.Len(devices); n != 2 {
		t.Errorf("devices %d", n)
	}
	if n, _ := ojson.Len(clients); n != 2 {
		t.Errorf("clients %d", n)
	}
	if m.LoginCount() != logins+1 {
		t.Errorf("logins: %d, want %d", m.LoginCount(), logins+1)
	}
	// The fresh session is still in use.
	if _, err := c.SiteGet(ctx, "default", "stat/device", nil); err != nil || m.LoginCount() != logins+1 {
		t.Errorf("fresh session discarded: %v, %d logins", err, m.LoginCount())
	}
}

var traversals = []string{
	"/%2E%2E/%2e%2e/api/system",
	"/.%2e/api/system",
	"/%2e./api/system",
	"/api/s/default/../../../api/system",
	"/api/./s/default/stat/device",
	"/api/s/%252e%252e/stat/device",
	"/api/s/default/stat/device/..",
	`/api\..\system`,
	"/api/s/default/stat/device\x00",
	"/api/s/default/stat/device%0a",
	"/api/s/default/stat/device?x=1",
	"/api/s/default/stat/device#x",
	"/api/s/%zz/stat",
	"/api/s/%c3%28/stat",
	"/v2/api/site/default/firewall-policies/batch-d%65lete",
	"/api/s/default/cmd/%73tamgr",
	"/api/s/default/rest/user/u%2D1",
	"/api/s/default/rest/user/%5F%7e%30%5a",
	"api/s/default",
}

func TestAssertSafePath(t *testing.T) {
	for _, p := range traversals {
		if err := unifi.AssertSafePath(p); err == nil || !strings.Contains(err.Error(), "Invalid API path") {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range []string{"/api/s/default/stat/device/aa:bb:cc:00:00:01", "/api/s/default/stat/report/5minutes.site", "/v2/api/site/default/trafficrules/my%20rule", "/api/s/default/rest/user/a%2Fb%7C", "/x/%C3%A9"} {
		if err := unifi.AssertSafePath(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	for p, want := range map[string]string{
		"/a/../b":              `Invalid API path "/a/../b": "." and ".." segments are not allowed`,
		"/a%2e":                `Invalid API path "/a%2e": encoded dots (%2e) are not allowed`,
		"/a?b":                 `Invalid API path "/a?b": pass query parameters separately, not in the path`,
		"/a\\b":                `Invalid API path "/a\b": backslashes and control characters are not allowed`,
		"/a%zz":                `Invalid API path "/a%zz": malformed percent-encoding`,
		"/v2/x/batch-d%65lete": `Invalid API path "/v2/x/batch-d%65lete": %65 encodes a character that doesn't need encoding; write it unencoded`,
		"relative/x":           `Invalid API path "relative/x": it must start with /`,
	} {
		if err := unifi.AssertSafePath(p); err == nil || err.Error() != want {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestPathSegment(t *testing.T) {
	for _, v := range []string{"..", ".", ""} {
		if _, err := unifi.PathSegment(v, ""); err == nil || !strings.Contains(err.Error(), "Invalid path segment") {
			t.Errorf("%q: %v", v, err)
		}
	}
	if s, _ := unifi.PathSegment("a/b", ""); s != "a%2Fb" {
		t.Errorf("a/b: %s", s)
	}
	if s, _ := unifi.PathSegment("x y!'()*~é", ""); s != "x%20y!'()*~%C3%A9" {
		t.Errorf("encodeURIComponent: %s", s)
	}
	if _, err := unifi.PathSegment("..", "site"); err == nil || err.Error() != `Invalid site ".."` {
		t.Errorf("site: %v", err)
	}
}

func TestTraversalNeverReachesTheController(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "unifi-os", APIKey: "key123"})
	defer m.Close()
	m.On("GET", `^/api/system$`, func(any, []string, url.Values) (int, any) { return 200, ojson.New("leaked", true) })
	c := newClient(t, map[string]string{"UNIFI_URL": m.URL, "UNIFI_API_KEY": "key123"})
	before := len(m.Requests())
	for _, p := range traversals {
		if !strings.HasPrefix(p, "/") {
			continue
		}
		if _, err := c.Request(ctx, "GET", p, nil); err == nil || !strings.Contains(err.Error(), "Invalid API path") {
			t.Errorf("%q: %v", p, err)
		}
	}
	if _, err := c.SiteGet(ctx, "..", "stat/device", nil); err == nil || !strings.Contains(err.Error(), "Invalid site") {
		t.Errorf("site ..: %v", err)
	}
	if _, err := c.V2(ctx, "GET", ".", "trafficrules", ojson.Undefined); err == nil || !strings.Contains(err.Error(), "Invalid site") {
		t.Errorf("site .: %v", err)
	}
	// Anything the URL parser would rewrite is refused rather than sent somewhere unexpected.
	for _, p := range []string{"/api/s/default/stat/my device", "/api/s/default/stat/{x}", "/api/s/default/stat/é"} {
		if _, err := c.Request(ctx, "GET", p, nil); err == nil || !strings.Contains(err.Error(), "Refusing to send GET /proxy/network"+p+": it resolves to") {
			t.Errorf("%q: %v", p, err)
		}
	}
	if n := len(m.Requests()); n != before {
		t.Errorf("%d requests reached the controller", n-before)
	}
	// Encoded characters that are safe are sent exactly as given.
	_, _ = c.Request(ctx, "GET", "/api/s/default/rest/user/a%2Fb|c", nil)
	if p := m.Last().Path; p != "/proxy/network/api/s/default/rest/user/a%2Fb|c" {
		t.Errorf("sent path %s", p)
	}
}

func TestBasePath(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "legacy", APIKey: ""})
	defer m.Close()
	c := newClient(t, map[string]string{"UNIFI_URL": m.URL + "/unifi/", "UNIFI_USERNAME": "admin", "UNIFI_PASSWORD": "secret", "UNIFI_CONTROLLER_TYPE": "legacy"})
	_, _ = c.SiteGet(ctx, "default", "stat/device", nil)
	if p := m.Requests()[0].Path; p != "/unifi/api/login" {
		t.Errorf("path with base: %s", p)
	}
}

func TestNetworkErrors(t *testing.T) {
	c := newClient(t, map[string]string{"UNIFI_URL": "http://127.0.0.1:1", "UNIFI_API_KEY": "k"})
	if _, err := c.SiteGet(ctx, "default", "stat/device", nil); err == nil || err.Error() != "Could not reach UniFi controller at http://127.0.0.1:1: ECONNREFUSED" {
		t.Errorf("refused: %v", err)
	}
	m := unifitest.New(unifitest.Options{Mode: "unifi-os", APIKey: "k"})
	defer m.Close()
	m.Delay(`^/api/s/default/stat/device$`, 300*time.Millisecond)
	slow := newClient(t, map[string]string{"UNIFI_URL": m.URL, "UNIFI_API_KEY": "k", "UNIFI_TIMEOUT_MS": "100"})
	if _, err := slow.SiteGet(ctx, "default", "stat/device", nil); err == nil || err.Error() != "Timed out after 100ms talking to "+m.URL {
		t.Errorf("timeout: %v", err)
	}
	m.On("GET", `^/api/s/default/text$`, func(any, []string, url.Values) (int, any) { return 200, "plain" })
	fast := newClient(t, map[string]string{"UNIFI_URL": m.URL, "UNIFI_API_KEY": "k"})
	res, err := fast.SiteGet(ctx, "default", "text", nil)
	if err != nil || res != "plain" {
		t.Errorf("JSON string response: %v %v", res, err)
	}
}

func writeFile(t *testing.T, name string, data []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
