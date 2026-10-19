package tools

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

func TestTraversalThroughToolsIsRejected(t *testing.T) {
	env := startMock(t, toolOptions(true, true))
	env.mock.On("GET", `^/api/system$`, func(any, []string, url.Values) (int, any) { return 200, ojson.New("leaked", true) })
	before := len(env.mock.Requests())
	for _, path := range []string{"/%2E%2E/%2e%2e/api/system", "/.%2e/api/system", "/api/../api/system", `/api\..\system`, "/api/s/default/stat/device%0a"} {
		contains(t, path, env.callErr("unifi_api_request", ojson.New("path", path)), "Invalid API path")
	}
	contains(t, "site", env.callErr("unifi_get_site_health", `{"site":".."}`), "Invalid site")
	contains(t, "site .", env.callErr("unifi_list_traffic_rules", `{"site":"."}`), "Invalid site")
	contains(t, "id", env.callErr("unifi_save_wlan", `{"id":"..","enabled":false}`), "Invalid path segment")
	contains(t, "id", env.callErr("unifi_delete_port_forward", `{"id":"."}`), "Invalid path segment")
	if len(env.mock.Requests()) != before {
		t.Errorf("traversal attempts reached the controller: %+v", env.mock.Requests()[before:])
	}
	for _, r := range env.mock.Requests() {
		if strings.HasPrefix(r.Path, "/api/system") {
			t.Errorf("request escaped: %s", r.Path)
		}
	}
	// Normal raw requests still work.
	sites := env.call("unifi_api_request", `{"path":"/api/self/sites"}`)
	eq(t, "sites", get(sites, 0, "name"), "default")
	// Query parameters are sent separately and encoded like URLSearchParams.
	env.call("unifi_api_request", `{"path":"/api/self/sites","query":{"a":"b c*~","z":"1"}}`)
	if got := env.mock.Last().Path; got != "/proxy/network/api/self/sites?a=b+c*%7E&z=1" {
		t.Errorf("query encoding: %s", got)
	}
}

func TestRawToolGating(t *testing.T) {
	ro := startMock(t, toolOptions(false, false))
	contains(t, "writes", ro.callErr("unifi_api_request", `{"method":"POST","path":"/api/s/default/cmd/devmgr","body":{"cmd":"restart"}}`), "Write access is disabled")

	env := startMock(t, toolOptions(true, false))
	before := len(env.mock.Requests())
	contains(t, "DELETE", env.callErr("unifi_api_request", `{"method":"DELETE","path":"/api/s/default/rest/wlanconf/w1"}`), "Delete access is disabled. Set UNIFI_ALLOW_DELETES=true to allow DELETE requests.")
	for _, c := range []struct{ path, body, reason string }{
		{"/api/s/default/cmd/sitemgr", `{"cmd":"delete-site","site":"x"}`, `command "delete-site"`},
		{"/api/s/default/cmd/sitemgr", `{"cmd":"delete-device","mac":"aa:bb:cc:00:00:01"}`, `command "delete-device"`},
		{"/api/s/default/cmd/sitemgr", `{"cmd":"revoke-admin","admin":"a1"}`, `command "revoke-admin"`},
		{"/api/s/default/cmd/stamgr", `{"cmd":"forget-sta","macs":["11:22:33:44:55:66"]}`, `command "forget-sta"`},
		{"/api/s/default/cmd/backup", `{"cmd":"DELETE-BACKUP","filename":"x.unf"}`, `command "DELETE-BACKUP"`},
		{"/api/s/default/cmd/stat", `{"cmd":"reset-dpi"}`, `command "reset-dpi"`},
		{"/api/s/default/cmd/hotspot", `[{"cmd":"delete-voucher","_id":"v1"}]`, `command "delete-voucher"`},
		{"/v2/api/site/default/firewall-policies/batch-delete", `["p1"]`, `path segment "batch-delete"`},
		{"/api/s/default/cmd/devmgr", `{"cmd":"destroy-everything"}`, `command "destroy-everything"`},
		{"/api/s/default/cmd/devmgr", `{"cmd":"set-default","mac":"aa:bb:cc:00:00:01"}`, `command "set-default"`},
		{"/api/s/default/cmd/devmgr", `{"CMD":"Set-Default","mac":"aa:bb:cc:00:00:01"}`, `command "Set-Default"`},
		{"/api/s/default/cmd/stat", `{"cmd":"clear-dpi"}`, `command "clear-dpi"`},
		{"/api/s/default/cmd/devmgr", `{"cmd":"migrate","mac":"aa:bb:cc:00:00:01"}`, `command "migrate" for devmgr isn't on the list of commands known not to delete anything`},
		{"/api/s/default/cmd/stamgr", `{"Cmd":"block-sta","cmd":"purge-sta"}`, `command "purge-sta"`},
	} {
		msg := env.callErr("unifi_api_request", `{"method":"POST","path":"`+c.path+`","body":`+c.body+`}`)
		contains(t, c.body, msg, "Delete access is disabled and this request looks like a delete ("+c.reason+"). Set UNIFI_ALLOW_DELETES=true to allow it.")
	}
	if len(env.mock.Requests()) != before {
		t.Error("delete-like requests reached the controller")
	}
	env.call("unifi_api_request", `{"method":"POST","path":"/api/s/default/cmd/devmgr","body":{"cmd":"restart","mac":"aa:bb:cc:00:00:01"}}`)
	eq(t, "restart", env.mock.Last().Body, js(`{"cmd":"restart","mac":"aa:bb:cc:00:00:01"}`))
	contains(t, "description", toolMap(t, env.mcp)["unifi_api_request"].Description, "forget, revoke")

	all := startMock(t, toolOptions(true, true))
	all.call("unifi_api_request", `{"method":"POST","path":"/api/s/default/cmd/stamgr","body":{"cmd":"forget-sta","macs":["11:22:33:44:55:66"]}}`)
	eq(t, "forget", get(all.mock.Last().Body, "cmd"), "forget-sta")
}

// Regression test: a percent-encoded letter must not hide a delete from the
// no-deletes gate (POST .../batch-d%65lete reached the controller and batch-deleted).
func TestRawToolEncodedDeleteIsRejected(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	before := len(env.mock.Requests())
	for _, path := range []string{
		"/v2/api/site/default/firewall-policies/batch-d%65lete",
		"/v2/api/site/default/firewall-policies/batch-%64elete",
		"/v2/api/site/default/firewall-policies/batch-DEL%45TE",
	} {
		contains(t, path, env.callErr("unifi_api_request", `{"method":"POST","path":"`+path+`","body":["p1"]}`), "Invalid API path")
	}
	if reqs := env.mock.Requests()[before:]; len(reqs) != 0 {
		t.Errorf("encoded delete reached the controller: %+v", reqs)
	}
	// The gate itself also looks at the decoded path.
	eq(t, "decoded", deleteLikeReason("/v2/x/batch-d%65lete", ojson.Undefined), `path segment "batch-delete"`)
}

func TestForgetAndRevokeAreDeletes(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	writes := toolMap(t, env.mcp)
	deletes := toolMap(t, connect(t, env.client, toolOptions(true, true)))
	for _, name := range []string{"unifi_forget_clients", "unifi_revoke_voucher", "unifi_forget_device", "unifi_revoke_admin"} {
		if writes[name] != nil {
			t.Errorf("%s registered without deletes", name)
		}
		if tool := deletes[name]; tool == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("%s should be a destructive delete tool", name)
		}
	}
}

func TestFirmwareRefreshNeedsWrites(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	before := len(env.mock.Requests())
	contains(t, "refresh", env.callErr("unifi_get_firmware_status", `{"refresh":true}`), "refresh requires UNIFI_ALLOW_WRITES=true")
	contains(t, "check_updates", env.callErr("unifi_check_updates", `{"refresh":true}`), "refresh requires UNIFI_ALLOW_WRITES")
	for _, r := range env.mock.Requests()[before:] {
		if r.Method != "GET" {
			t.Errorf("non-GET request without writes: %s %s", r.Method, r.Path)
		}
	}
	ok := env.call("unifi_get_firmware_status", nil)
	if n, _ := ojson.Len(get(ok, "devices")); n != 2 {
		t.Errorf("devices: %d", n)
	}
}

func TestDeleteLikeReason(t *testing.T) {
	for _, c := range []struct {
		path string
		body any
		want string
	}{
		{"/api/s/default/cmd/devmgr", js(`{"cmd":"restart"}`), ""},
		{"/api/s/default/rest/user/u1", js(`{"name":"x"}`), ""},
		{"/api/s/default/cmd/x", js(`{"cmd":"Remove-Thing"}`), `command "Remove-Thing"`},
		{"/api/s/default/cmd/x", js(`[{"cmd":"ok"},{"cmd":"forget"}]`), `command "forget"`},
		{"/v2/api/site/default/thing/reset", ojson.Undefined, `path segment "reset"`},
		{"/api/s/default/cmd/x", js(`{"cmd":5}`), ""},
		{"/api/s/default/cmd/devmgr", js(`{"cmd":"set-default"}`), `command "set-default"`},
		{"/api/s/default/cmd/devmgr/set-rollupgrade", js(`{}`), ""},
		{"/api/s/default/cmd/devmgr/mystery", js(`{}`), `command "mystery" for devmgr isn't on the list of commands known not to delete anything`},
		{"/api/s/default/cmd/stamgr", js(`{"cmd":"Kick-Sta"}`), ""},
		{"/api/s/default/cmd/system", js(`{"cmd":"reboot"}`), ""},
		{"/api/s/default/rest/model-x/1", js(`{}`), ""},
		{"/api/s/default/cmd/x", js(`{"cmd":"del-user"}`), `command "del-user"`},
		{"/api/s/default/cmd/x", js(`{"cmd":"erase"}`), `command "erase"`},
		{"/api/s/default/cmd/x", js(`{"cmd":"factory"}`), `command "factory"`},
	} {
		if got := deleteLikeReason(c.path, c.body); got != c.want {
			t.Errorf("%s: %q, want %q", c.path, got, c.want)
		}
	}
}

func TestLargeOutputIsTruncated(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	big := make([]any, 0, 20000)
	for i := 0; i < 20000; i++ {
		big = append(big, ojson.New("name", strings.Repeat("é", 10), "i", float64(i)))
	}
	env.mock.On("GET", `^/api/s/default/stat/big$`, func(any, []string, url.Values) (int, any) {
		return 200, ojson.New("data", big, "meta", ojson.New("rc", "ok"))
	})
	res := callRaw(t, env.mcp, "unifi_api_request", `{"path":"/api/s/default/stat/big"}`)
	out := text(res)
	if len(out) > MaxOutputBytes+300 || !strings.Contains(out, "[Output truncated: the result is ") || !utf8.ValidString(out) {
		t.Errorf("output: %d bytes, valid UTF-8 %v, tail %q", len(out), utf8.ValidString(out), out[len(out)-200:])
	}
	small := text(callRaw(t, env.mcp, "unifi_api_request", `{"path":"/api/self/sites"}`))
	if strings.Contains(small, "truncated") {
		t.Error("small output truncated")
	}
}

func TestAccessGrantingAndDisruptiveWritesAreDestructive(t *testing.T) {
	tools := toolMap(t, startMock(t, toolOptions(true, false)).mcp)
	for _, name := range []string{
		"unifi_invite_admin", "unifi_create_site", "unifi_reconnect_client", "unifi_update_client", "unifi_unblock_client",
		"unifi_authorize_guest", "unifi_unauthorize_guest", "unifi_create_vouchers", "unifi_save_hotspot_operator",
		"unifi_save_radius_account", "unifi_save_radius_profile", "unifi_provision_device", "unifi_adopt_device",
		"unifi_save_ap_group", "unifi_save_dns_record", "unifi_save_content_filter", "unifi_save_bandwidth_profile",
		"unifi_save_dynamic_dns",
	} {
		if tool := tools[name]; tool == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("%s should be marked destructive", name)
		}
	}
}

func TestRestPathEncodesIDs(t *testing.T) {
	if p, err := restPath("device", "a/b c"); err != nil || p != "rest/device/a%2Fb%20c" {
		t.Errorf("%q %v", p, err)
	}
	for _, id := range []any{"..", ".", "", ojson.Undefined} {
		if _, err := restPath("device", id); err == nil {
			t.Errorf("%v accepted", id)
		}
	}
}
