package unifi_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

func TestDryRunSendsReadsOnly(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "unifi-os", APIKey: "k"})
	defer m.Close()
	m.On("POST", "^/api/s/default/stat/report/daily.site$", func(any, []string, url.Values) (int, any) {
		return unifitest.OK([]any{ojson.New("time", 1.0)})
	})
	m.On("POST", "^/v2/api/site/default/system-log/all$", func(any, []string, url.Values) (int, any) {
		return 200, ojson.New("data", []any{})
	})
	c := newClient(t, map[string]string{"UNIFI_URL": m.URL, "UNIFI_API_KEY": "k"})
	call := &unifi.Call{}
	call.SetDryRun()
	dctx := unifi.WithCall(ctx, call)

	// Reads, including the ones marked as reads that POST, are sent.
	if _, err := c.SiteGet(dctx, "default", "rest/wlanconf/w1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SitePostRead(dctx, "default", "stat/report/daily.site", nil); err != nil {
		t.Fatalf("read POST blocked: %v", err)
	}
	if _, err := c.V2PostRead(dctx, "default", "system-log/all", ojson.New()); err != nil {
		t.Fatalf("v2 read POST blocked: %v", err)
	}
	if _, err := c.CmdRead(dctx, "default", "sitemgr", "get-admins", nil); err != nil {
		t.Fatalf("read command blocked: %v", err)
	}
	sentBefore := len(m.Requests())

	// The first change is stopped and remembered, with the object read earlier.
	_, err := c.SitePut(dctx, "default", "rest/wlanconf/w1", ojson.New("_id", "w1", "name", "Guest"))
	var dry *unifi.DryRunError
	if !errors.As(err, &dry) || dry.Method != "PUT" {
		t.Fatalf("want a DryRunError, got %v", err)
	}
	if _, err := c.Cmd(dctx, "default", "devmgr", "restart", nil); !errors.As(err, &dry) {
		t.Fatalf("a command was not stopped: %v", err)
	}
	if n := len(m.Requests()); n != sentBefore {
		t.Errorf("%d requests sent after the first change", n-sentBefore)
	}
	req, blocked := call.Intercepted()
	if req == nil || req.Path != "/api/s/default/rest/wlanconf/w1" || blocked != 2 {
		t.Fatalf("intercepted %+v, %d", req, blocked)
	}
	if ojson.Get(req.Current, "name") != "Home" {
		t.Errorf("current object %v", req.Current)
	}
	if len(call.Sent()) != 0 {
		t.Errorf("sent %v", call.Sent())
	}
}

func TestDryRunNeverLogsInForAChange(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "legacy"})
	defer m.Close()
	c := newClient(t, userEnv(m.URL, "UNIFI_CONTROLLER_TYPE", "legacy"))
	if _, err := c.SiteGet(ctx, "default", "stat/device", nil); err != nil {
		t.Fatal(err)
	}
	m.ClearSessions()
	m.ClearRequests()
	call := &unifi.Call{}
	call.SetDryRun()
	dctx := unifi.WithCall(ctx, call)
	m.Observe(func(r unifitest.Request) {
		if r.Method != "GET" && r.Path != "/api/login" {
			t.Errorf("a dry run sent %s %s", r.Method, r.Path)
		}
	})
	if _, err := c.SitePut(dctx, "default", "rest/wlanconf/w1", ojson.New("name", "x")); err == nil {
		t.Fatal("the change was not stopped")
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("stopping a change sent %d requests", n)
	}
	// A read with an expired session logs in again (a POST that changes nothing) and is retried.
	if _, err := c.SiteGet(dctx, "default", "rest/wlanconf", nil); err != nil {
		t.Fatal(err)
	}
}

func TestCallRecordsSentChanges(t *testing.T) {
	m := unifitest.New(unifitest.Options{Mode: "unifi-os", APIKey: "k"})
	defer m.Close()
	c := newClient(t, map[string]string{"UNIFI_URL": m.URL, "UNIFI_API_KEY": "k"})
	call := &unifi.Call{}
	cctx := unifi.WithCall(ctx, call)
	_, _ = c.SiteGet(cctx, "default", "rest/wlanconf", nil)
	_, _ = c.SitePostRead(cctx, "default", "stat/voucher", nil)
	_, _ = c.SitePut(cctx, "default", "rest/wlanconf/w1", ojson.New("name", "x"))
	_, _ = c.SitePut(cctx, "default", "rest/wlanconf/nope", ojson.New("name", "x"))
	got := call.Sent()
	want := []unifi.SentRequest{{"PUT", "/api/s/default/rest/wlanconf/w1", 200}, {"PUT", "/api/s/default/rest/wlanconf/nope", 400}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("sent %v", got)
	}
}

func TestDiff(t *testing.T) {
	current := ojson.MustParse(`{"_id":"a","name":"Home","x_passphrase":"old","vlan":10,"radius":{"secret":"s","port":1812},"tags":["a","b"]}`)
	body := ojson.MustParse(`{"_id":"a","name":"Guest","x_passphrase":"new","radius":{"secret":"s","port":1813},"tags":["a","c"],"enabled":true}`)
	got, _ := ojson.Stringify(unifi.Diff(current, body))
	want := `{"changed":{"name":{"from":"Home","to":"Guest"},"x_passphrase":{"from":"[redacted]","to":"[redacted]"},"radius.port":{"from":1812,"to":1813},"tags[1]":{"from":"b","to":"c"}},"added":{"enabled":true},"removed":{"vlan":10}}`
	if got != want {
		t.Errorf("diff\n got  %s\n want %s", got, want)
	}
	// A partial body (no _id) leaves out fields it doesn't mention.
	got, _ = ojson.Stringify(unifi.Diff(current, ojson.MustParse(`{"name":"Home"}`)))
	if got != `{}` {
		t.Errorf("partial diff %s", got)
	}
}
