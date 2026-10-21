package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

// forbidChanges fails the test the moment anything but a GET reaches the controller.
func forbidChanges(t *testing.T, m *unifitest.Mock) {
	t.Helper()
	m.Observe(func(r unifitest.Request) {
		if r.Method != "GET" {
			t.Errorf("a dry run sent %s %s", r.Method, r.Path)
		}
	})
}

func TestDryRunUpdateShowsDiff(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	forbidChanges(t, env.mock)
	res := env.call("unifi_save_wlan", `{"id":"w1","name":"Guest","passphrase":"correct horse","dry_run":true}`)
	eq(t, "dry_run", get(res, "dry_run"), true)
	eq(t, "would_send", get(res, "would_send"), js(`{"method":"PUT","path":"/api/s/default/rest/wlanconf/w1"}`))
	eq(t, "name", get(res, "changes", "changed", "name"), js(`{"from":"Home","to":"Guest"}`))
	eq(t, "passphrase", get(res, "changes", "changed", "x_passphrase"), js(`{"from":"[redacted]","to":"[redacted]"}`))
	contains(t, "note", ojson.String(get(res, "note")), "Nothing was changed")
	if strings.Contains(text(callRaw(t, env.mcp, "unifi_save_wlan", `{"id":"w1","passphrase":"correct horse","dry_run":true}`)), "correct horse") {
		t.Error("the preview leaks the passphrase")
	}
	eq(t, "store unchanged", get(env.mock.Store("rest/wlanconf"), 0, "name"), "Home")
}

func TestDryRunNoChange(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	forbidChanges(t, env.mock)
	res := env.call("unifi_save_wlan", `{"id":"w1","name":"Home","dry_run":true}`)
	eq(t, "changes", get(res, "changes"), js(`{}`))
	contains(t, "note", ojson.String(get(res, "note")), "would not change any field")
}

func TestDryRunCreate(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	forbidChanges(t, env.mock)
	res := env.call("unifi_save_bandwidth_profile", `{"name":"Kids","download_kbps":2000,"dry_run":true}`)
	eq(t, "would_send", get(res, "would_send"), js(`{"method":"POST","path":"/api/s/default/rest/usergroup","body":{"name":"Kids","qos_rate_max_down":2000,"qos_rate_max_up":-1}}`))
	eq(t, "no changes", get(res, "changes"), ojson.Undefined)
}

func TestDryRunDelete(t *testing.T) {
	env := startMock(t, toolOptions(true, true))
	forbidChanges(t, env.mock)
	res := env.call("unifi_delete_wlan", `{"id":"w1","dry_run":true}`)
	eq(t, "would_send", get(res, "would_send"), js(`{"method":"DELETE","path":"/api/s/default/rest/wlanconf/w1"}`))
	eq(t, "current", get(res, "current", "name"), "Home")
	eq(t, "current redacted", get(res, "current", "x_passphrase"), "[redacted]")
	eq(t, "still there", len(env.mock.Store("rest/wlanconf")), 1)
}

func TestDryRunDeviceCommand(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	forbidChanges(t, env.mock)
	res := env.call("unifi_restart_device", `{"mac":"AA:BB:CC:00:00:01","reboot_type":"hard","dry_run":true}`)
	eq(t, "would_send", get(res, "would_send"), js(`{"method":"POST","path":"/api/s/default/cmd/devmgr","body":{"cmd":"restart","mac":"aa:bb:cc:00:00:01","reboot_type":"hard"}}`))
	if n := len(env.mock.Requests()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

func TestDryRunSwitchPortKeepsOtherOverrides(t *testing.T) {
	env := newSwitchEnv(t)
	forbidChanges(t, env.mock)
	res := env.call("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:01","port_idx":1,"poe_mode":"off","dry_run":true}`)
	eq(t, "would_send", get(res, "would_send"), js(`{"method":"PUT","path":"/api/s/default/rest/device/d1"}`))
	eq(t, "overrides", get(res, "changes", "changed", "port_overrides"), js(`{
		"from":[{"port_idx":2,"name":"Printer","portconf_id":"pc-iot"}],
		"to":[{"port_idx":1,"portconf_id":"pc-all","poe_mode":"off"},{"port_idx":2,"name":"Printer","portconf_id":"pc-iot"}]}`))
	eq(t, "partial update reports no removals", get(res, "changes", "removed"), ojson.Undefined)

	// Changing an existing override diffs inside the array.
	res = env.call("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:01","port_idx":2,"name":"Label printer","dry_run":true}`)
	eq(t, "nested", get(res, "changes"), js(`{"changed":{"port_overrides[0].name":{"from":"Printer","to":"Label printer"}}}`))
}

func TestDryRunRawRequest(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	forbidChanges(t, env.mock)
	res := env.call("unifi_api_request", `{"method":"POST","path":"/api/s/default/rest/radiusprofile","body":{"name":"r","x_secret":"s3cret"},"dry_run":true}`)
	eq(t, "would_send", get(res, "would_send"), js(`{"method":"POST","path":"/api/s/default/rest/radiusprofile","body":{"name":"r","x_secret":"[redacted]"}}`))
	// A GET is just sent.
	get := env.call("unifi_api_request", `{"path":"/api/self/sites","dry_run":true}`)
	eq(t, "read result", ojson.Get(get, "would_send"), nil)
	eq(t, "result", len(arr(ojson.Get(get, "result"))), 1)
}

func TestDryRunMultiStep(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	forbidChanges(t, env.mock)
	res := env.call("unifi_create_vouchers", `{"minutes":60,"count":2,"dry_run":true}`)
	eq(t, "would_send", get(res, "would_send", "body"), js(`{"cmd":"create-voucher","n":2,"expire":60,"quota":1}`))
	contains(t, "note", ojson.String(get(res, "note")), "later steps of the tool")
	if n := len(env.mock.RequestsTo("/api/s/default/stat/voucher")); n != 0 {
		t.Errorf("the follow-up read ran %d times", n)
	}
}

// readEndpoint matches controller endpoints that only read even though they
// are POSTed. If a dry run's preview is one of these, a read wasn't marked
// with Options.Read and the dry run stopped too early.
var readEndpoint = regexp.MustCompile(`/stat/|/system-log/|"cmd":"(get-|list-)`)

// TestDryRunEveryWriteTool runs every tool that changes things with dry_run
// and generated arguments: none may send anything but GETs, and none may stop
// at a read.
func TestDryRunEveryWriteTool(t *testing.T) {
	env := newSwitchEnv(t)
	for _, coll := range []string{"account", "dynamicdns", "firewallgroup", "firewallrule", "hotspotop", "networkconf", "portconf", "portforward", "radiusprofile", "routing", "usergroup", "wlanconf"} {
		env.mock.Seed("rest", coll, `[{"_id":"w1","name":"x","purpose":"wan"}]`)
	}
	for _, coll := range []string{"acl-rules", "apgroups", "content-filtering", "firewall-policies", "firewall/zone", "nat", oonList, "qos-rules", "static-dns", "trafficroutes", "trafficrules"} {
		env.mock.Seed("v2", coll, `[{"_id":"w1","name":"x"}]`)
	}
	env.mock.Seed("rest", "setting", `[{"_id":"s1","key":"mgmt"},{"_id":"s2","key":"guest_access"},{"_id":"s3","key":"ips"},{"_id":"s4","key":"country"},{"_id":"s5","key":"usg"}]`)
	env.mock.On("GET", `^/integration/v1/sites$`, func(any, []string, url.Values) (int, any) {
		return 200, js(`{"data":[{"id":"site-uuid","internalReference":"default"}]}`)
	})
	env.mock.On("GET", `^/integration/v1/sites/site-uuid/firewall/zones$`, func(any, []string, url.Values) (int, any) {
		return 200, js(`{"data":[{"id":"w1","name":"x","metadata":{"origin":"USER_DEFINED"}}]}`)
	})
	env.mock.On("GET", `^/integration/v1/sites/site-uuid/firewall/policies/ordering$`, func(any, []string, url.Values) (int, any) {
		return 200, js(`{"orderedFirewallPolicyIds":{"beforeSystemDefined":["x1"],"afterSystemDefined":[]}}`)
	})
	forbidChanges(t, env.mock)
	// Arguments the generated ones can't guess: something to change, or an existing object.
	overrides := map[string]string{
		"unifi_update_device":                `{"name":"x"}`,
		"unifi_update_guest_portal_settings": `{"enabled":true}`,
		"unifi_update_country_blocking":      `{"enabled":true}`,
		"unifi_update_threat_management":     `{"ips_mode":"ids"}`,
		"unifi_update_site_setting":          `{"key":"mgmt","changes":{"led_enabled":false}}`,
		"unifi_set_switch_port":              `{"name":"x"}`,
		"unifi_set_radio":                    `{"mac":"aa:bb:cc:00:00:02","radio":"ng","channel":1}`,
		"unifi_set_outlet":                   `{"mac":"aa:bb:cc:00:00:03","index":1,"relay_on":true}`,
		"unifi_move_device":                  `{"target_site":"branch"}`,
		"unifi_delete_site":                  `{"site_name":"branch"}`,
		"unifi_update_client":                `{"mac":"11:22:33:44:55:66","name":"x"}`,
		"unifi_save_qos_rule":                `{"name":"y"}`,
		"unifi_save_nat_rule":                `{"description":"y"}`,
		"unifi_save_acl_rule":                `{"name":"y"}`,
		"unifi_save_oon_policy":              `{"name":"y"}`,
	}
	tools := toolMap(t, env.mcp)
	previews := 0
	previewed := map[string]bool{}
	for name, tool := range tools {
		if tool.Annotations.ReadOnlyHint {
			continue
		}
		schema := mustSchema(t, tool.InputSchema)
		if _, ok := ojson.AsObj(ojson.Get(schema, "properties", "dry_run")); !ok {
			t.Errorf("%s has no dry_run argument", name)
			continue
		}
		args := sampleArgs(schema)
		if props, _ := ojson.AsObj(schema.Get("properties")); props.Has("id") {
			args.Set("id", "w1")
		}
		if o, ok := overrides[name]; ok {
			over, _ := ojson.AsObj(js(o))
			args.Assign(over)
		}
		args.Set("dry_run", true)
		res := callRaw(t, env.mcp, name, args)
		if res.IsError {
			t.Logf("%s: %s", name, text(res))
			continue
		}
		v, err := ojson.Parse([]byte(text(res)))
		if err != nil || ojson.Get(v, "dry_run") != true {
			t.Errorf("%s: not a dry run result: %s", name, text(res))
			continue
		}
		send := ojson.Get(v, "would_send")
		if send == nil {
			continue
		}
		previews++
		previewed[name] = true
		s, _ := ojson.Stringify(send)
		if readEndpoint.MatchString(s) {
			t.Errorf("%s: the dry run stopped at a read: %s", name, s)
		}
	}
	// Enough tools must get as far as their first change for the check to mean something.
	t.Logf("%d tools produced a preview", previews)
	if previews < 60 {
		t.Errorf("only %d tools produced a preview", previews)
	}
	// Policy table tools must all get as far as their first change.
	for _, n := range []string{"qos_rule", "nat_rule", "acl_rule", "oon_policy"} {
		for _, verb := range []string{"save", "delete"} {
			if name := "unifi_" + verb + "_" + n; !previewed[name] {
				t.Errorf("%s produced no preview", name)
			}
		}
	}
}

// sampleArgs builds plausible values for a tool's required arguments.
func sampleArgs(schema *ojson.Obj) *ojson.Obj {
	out := ojson.New()
	props, _ := ojson.AsObj(schema.Get("properties"))
	for _, r := range arr(schema.Get("required")) {
		name := ojson.String(r)
		out.Set(name, sampleValue(name, props.Get(name)))
	}
	return out
}

func sampleValue(name string, s any) any {
	if e := arr(ojson.Get(s, "enum")); len(e) > 0 {
		return e[0]
	}
	if anyOf := arr(ojson.Get(s, "anyOf")); len(anyOf) > 0 {
		return sampleValue(name, anyOf[0])
	}
	switch ojson.Get(s, "type") {
	case "boolean":
		return true
	case "integer", "number":
		if m, ok := ojson.Num(ojson.Get(s, "minimum")); ok && m > 1 {
			return m
		}
		return 1.0
	case "array":
		return []any{sampleValue(name, ojson.Get(s, "items"))}
	case "object":
		o, _ := ojson.AsObj(s)
		return sampleArgs(o)
	}
	switch {
	case strings.Contains(name, "mac"):
		return "aa:bb:cc:00:00:01"
	case ojson.Get(s, "format") == "email":
		return "admin@example.com"
	case ojson.Get(s, "format") == "uri":
		return "https://example.com/firmware.bin"
	case name == "id":
		return "w1"
	case name == "path":
		return "/api/s/default/rest/wlanconf/w1"
	}
	return "x1"
}

func mustSchema(t *testing.T, v any) *ojson.Obj {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ojson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := ojson.AsObj(parsed)
	return o
}

// ---------------------------------------------------------------- audit log

func auditEnv(t *testing.T, target string) *testEnv {
	t.Helper()
	opts := toolOptions(true, true)
	opts.AuditLog = target
	env := startMock(t, toolOptions(false, false))
	env.mcp = connect(t, env.client, opts)
	return env
}

func auditLines(t *testing.T, path string) []any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		v, err := ojson.Parse([]byte(line))
		if err != nil {
			t.Fatalf("bad audit line %q: %v", line, err)
		}
		out = append(out, v)
	}
	return out
}

func TestAuditLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	env := auditEnv(t, path)
	env.call("unifi_list_wlans", nil)
	env.call("unifi_save_wlan", `{"id":"w1","name":"Guest","passphrase":"correct horse"}`)
	env.call("unifi_save_wlan", `{"id":"w1","name":"Other","dry_run":true}`)
	env.callErr("unifi_delete_wlan", `{"id":"nope"}`)
	env.callErr("unifi_restart_device", `{"mac":"not-a-mac","bogus":1,"reboot_type":"sideways"}`)
	env.call("unifi_api_request", `{"path":"/api/self/sites"}`)

	lines := auditLines(t, path)
	if len(lines) != 4 {
		t.Fatalf("want 4 audit lines (writes only), got %d", len(lines))
	}
	saved := lines[0]
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`).MatchString(ojson.String(get(saved, "time"))) {
		t.Errorf("time %v", get(saved, "time"))
	}
	eq(t, "saved", ojson.New("tool", get(saved, "tool"), "dry_run", get(saved, "dry_run"), "arguments", get(saved, "arguments"), "requests", get(saved, "requests"), "outcome", get(saved, "outcome")),
		js(`{"tool":"unifi_save_wlan","dry_run":false,"arguments":{"id":"w1","name":"Guest","passphrase":"[redacted]"},
			"requests":[{"method":"PUT","path":"/api/s/default/rest/wlanconf/w1","status":200}],"outcome":"ok"}`))

	dry := lines[1]
	eq(t, "dry", ojson.New("dry_run", get(dry, "dry_run"), "requests", get(dry, "requests"), "previewed", get(dry, "previewed"), "outcome", get(dry, "outcome")),
		js(`{"dry_run":true,"requests":[],"previewed":{"method":"PUT","path":"/api/s/default/rest/wlanconf/w1"},"outcome":"ok"}`))

	failed := lines[2]
	eq(t, "failed outcome", get(failed, "outcome"), "error")
	contains(t, "failed error", ojson.String(get(failed, "error")), "HTTP 400")

	refused := lines[3]
	eq(t, "refused", get(refused, "tool"), "unifi_restart_device")
	eq(t, "refused outcome", get(refused, "outcome"), "error")
	contains(t, "refused error", ojson.String(get(refused, "error")), "Input validation error")
	eq(t, "refused args", get(refused, "arguments", "mac"), "not-a-mac")

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "correct horse") {
		t.Error("the audit log contains a passphrase")
	}
	if strings.Contains(string(raw), "\"data\"") || strings.Contains(string(raw), "Home") {
		t.Error("the audit log contains a response body")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("audit log mode %v, %v", info.Mode().Perm(), err)
	}
}

func TestAuditLogRecordsFailedWriteStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	env := auditEnv(t, path)
	env.callErr("unifi_api_request", `{"method":"PUT","path":"/api/s/default/rest/wlanconf/missing","body":{}}`)
	lines := auditLines(t, path)
	eq(t, "requests", get(lines[0], "requests"), js(`[{"method":"PUT","path":"/api/s/default/rest/wlanconf/missing","status":400}]`))
	eq(t, "outcome", get(lines[0], "outcome"), "error")
}

func TestAuditLogToStderr(t *testing.T) {
	l := newAuditLog(config.AuditLogStderr)
	var buf bytes.Buffer
	l.stderr = &buf
	l.write("{\"tool\":\"x\"}\n")
	if buf.String() != "{\"tool\":\"x\"}\n" {
		t.Errorf("stderr got %q", buf.String())
	}
	if newAuditLog("") != nil {
		t.Error("an empty UNIFI_AUDIT_LOG should turn the log off")
	}
}

func TestAuditLogFailureDoesNotBreakCalls(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	env := auditEnv(t, filepath.Join(t.TempDir(), "missing", "audit.log"))
	env.call("unifi_save_wlan", `{"id":"w1","name":"Guest"}`)
	env.call("unifi_save_wlan", `{"id":"w1","name":"Again"}`)
	if n := strings.Count(logs.String(), "could not write the audit log"); n != 1 {
		t.Errorf("want one warning, got %d: %s", n, logs.String())
	}
}

// The guard also works when a handler is called with a context that has no Call yet.
func TestGuardWithoutCall(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	forbidChanges(t, env.mock)
	c := &Context{Server: mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), Client: env.client, Tools: toolOptions(true, false)}
	d := Def{Name: "t", Gate: Write, Handler: func(ctx context.Context, a Args) (any, error) {
		return env.client.SitePost(ctx, "default", "cmd/devmgr", ojson.New("cmd", "restart"))
	}}
	c.guardWrites(&d)
	out, err := d.Handler(context.Background(), Args{ojson.New("dry_run", true)})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "would_send", ojson.Get(out, "would_send", "path"), "/api/s/default/cmd/devmgr")
}

// ---------------------------------------------------------------- lazy mode

func TestLazyWriteIsAuditedUnderItsOwnName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	opts := lazyOptions(true, false)
	opts.AuditLog = path
	env := startMock(t, toolOptions(false, false))
	env.mcp = connect(t, env.client, opts)

	env.call(callToolName, `{"name":"unifi_list_wlans"}`)
	env.call(callWriteToolName, `{"name":"unifi_save_wlan","arguments":{"id":"w1","name":"Guest","passphrase":"correct horse"}}`)

	lines := auditLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("want 1 audit line, got %d: %v", len(lines), lines)
	}
	eq(t, "entry", ojson.New("tool", get(lines[0], "tool"), "dry_run", get(lines[0], "dry_run"), "arguments", get(lines[0], "arguments"), "requests", get(lines[0], "requests"), "outcome", get(lines[0], "outcome")),
		js(`{"tool":"unifi_save_wlan","dry_run":false,"arguments":{"id":"w1","name":"Guest","passphrase":"[redacted]"},
			"requests":[{"method":"PUT","path":"/api/s/default/rest/wlanconf/w1","status":200}],"outcome":"ok"}`))
	eq(t, "store", get(env.mock.Store("rest/wlanconf"), 0, "name"), "Guest")
}

func TestLazyDryRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	opts := lazyOptions(true, true)
	opts.AuditLog = path
	env := startMock(t, toolOptions(false, false))
	env.mcp = connect(t, env.client, opts)

	schema := env.call(searchToolName, `{"names":["unifi_save_wlan","unifi_delete_wlan","unifi_list_wlans"]}`)
	eq(t, "save dry_run", get(schema, "tools", 0, "input_schema", "properties", "dry_run", "type"), "boolean")
	eq(t, "delete dry_run", get(schema, "tools", 1, "input_schema", "properties", "dry_run", "type"), "boolean")
	eq(t, "read has no dry_run", get(schema, "tools", 2, "input_schema", "properties", "dry_run"), ojson.Undefined)

	forbidChanges(t, env.mock)
	res := env.call(callWriteToolName, `{"name":"unifi_save_wlan","arguments":{"id":"w1","name":"Guest","dry_run":true}}`)
	eq(t, "dry_run", get(res, "dry_run"), true)
	eq(t, "would_send", get(res, "would_send"), js(`{"method":"PUT","path":"/api/s/default/rest/wlanconf/w1"}`))
	eq(t, "name", get(res, "changes", "changed", "name"), js(`{"from":"Home","to":"Guest"}`))
	res = env.call(callWriteToolName, `{"name":"unifi_delete_wlan","arguments":{"id":"w1","dry_run":true}}`)
	eq(t, "delete would_send", get(res, "would_send", "method"), "DELETE")
	eq(t, "store unchanged", get(env.mock.Store("rest/wlanconf"), 0, "name"), "Home")

	lines := auditLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("want 2 audit lines, got %d", len(lines))
	}
	for i, tool := range []string{"unifi_save_wlan", "unifi_delete_wlan"} {
		eq(t, "tool", get(lines[i], "tool"), tool)
		eq(t, "dry_run", get(lines[i], "dry_run"), true)
		eq(t, "requests", get(lines[i], "requests"), js(`[]`))
	}
}
