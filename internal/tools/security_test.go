package tools

import (
	"net/url"
	"strings"
	"testing"

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
	} {
		if got := deleteLikeReason(c.path, c.body); got != c.want {
			t.Errorf("%s: %q, want %q", c.path, got, c.want)
		}
	}
}
