package tools

import (
	"net/url"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

func opsEnv(t *testing.T) *testEnv {
	env := startMock(t, toolOptions(true, true))
	env.mock.
		Seed("rest", "setting", `[
			{"_id":"s_ips","key":"ips","ips_mode":"ids","enabled_categories":["emerging-malware"],"memory_optimized":false},
			{"_id":"s_dpi","key":"dpi","enabled":true,"fingerprintingEnabled":true},
			{"_id":"s_usg","key":"usg","geo_ip_filtering_enabled":false,"upnp_enabled":false},
			{"_id":"s_mgmt","key":"mgmt","led_enabled":true,"x_ssh_password":"sshpw"},
			{"_id":"s_ga","key":"guest_access","auth":"none","portal_enabled":false,"x_password":"guestpw"}]`).
		Seed("rest", "hotspotop", `[{"_id":"op1","name":"Front desk","x_password":"oppw"}]`).
		Seed("v2", "content-filtering", `[{"_id":"cf1","name":"Kids","enabled":true,"blocked_categories":["ADULT"]}]`).
		On("POST", `^/api/s/default/stat/ips/event$`, fixed(unifitest.OK(js(`[
			{"timestamp":1700000000000,"inner_alert_signature":"ET SCAN","inner_alert_category":"scan","src_ip":"1.2.3.4","dest_ip":"10.0.0.2","inner_alert_action":"blocked"},
			{"timestamp":1700000100000,"inner_alert_signature":"ET MALWARE","src_ip":"5.6.7.8"}]`)))).
		On("POST", `^/api/s/default/stat/event$`, fixed(404, js(`{"error":"gone"}`))).
		On("POST", `^/v2/api/site/default/system-log/([a-z-]+)$`, func(body any, m []string, _ url.Values) (int, any) {
			ts := 1700000000000.0
			if m[1] == "client-alert" {
				ts++
			}
			return 200, ojson.New("data", []any{ojson.New("timestamp", ts, "key", m[1]+"-key", "message", "from "+m[1], "echo", body)}, "totalElementCount", 1.0)
		}).
		On("POST", `^/api/s/default/stat/stadpi$`, func(body any, _ []string, _ url.Values) (int, any) {
			return unifitest.OK([]any{ojson.New("mac", get(body, "macs", 0), "by_app", []any{})})
		}).
		On("POST", `^/api/s/default/stat/session$`, fixed(unifitest.OK(js(`[{"mac":"11:22:33:44:55:66","assoc_time":1700000000,"disassoc_time":1700003600,"duration":3600}]`)))).
		On("POST", `^/api/s/default/cmd/sitemgr$`, func(body any, _ []string, _ url.Values) (int, any) {
			switch ojson.Get(body, "cmd") {
			case "get-admins":
				return unifitest.OK(js(`[{"_id":"a1","name":"Matt","email":"m@example.com","role":"admin","is_super":true}]`))
			case "add-site":
				return unifitest.OK([]any{ojson.New("_id", "site2id", "name", "abc123", "desc", ojson.Get(body, "desc"))})
			}
			return unifitest.OK([]any{})
		}).
		On("POST", `^/api/s/abc123/cmd/sitemgr$`, fixed(unifitest.OK([]any{}))).
		On("GET", `^/api/self/sites$`, fixed(unifitest.OK(js(`[{"_id":"s1","name":"default","desc":"Default"},{"_id":"site2id","name":"abc123","desc":"Branch"}]`)))).
		On("POST", `^/api/s/default/cmd/backup$`, func(body any, _ []string, _ url.Values) (int, any) {
			if ojson.Get(body, "cmd") == "list-backups" {
				return unifitest.OK(js(`[{"filename":"autobackup_9.0.unf","time":1700000000000,"size":2097152,"version":"9.0.114"}]`))
			}
			return unifitest.OK(js(`[{"url":"/dl/backup/9.0.unf"}]`))
		}).
		On("GET", `^/api/s/default/stat/fwupdate/latest-version$`, fixed(unifitest.OK(js(`[{"version":"9.1.0","update_available":true}]`))))
	return env
}

func fixed(status int, payload any) unifitest.Handler {
	return func(any, []string, url.Values) (int, any) { return status, payload }
}

func TestSecuritySettings(t *testing.T) {
	env := opsEnv(t)
	res := env.call("unifi_get_security_settings", nil)
	eq(t, "ips mode", get(res, "threat_management", "ips_mode"), "ids")
	eq(t, "dpi", get(res, "traffic_identification"), js(`{"enabled":true,"device_fingerprinting":true}`))
	eq(t, "geo", get(res, "country_blocking"), js(`{"enabled":false}`))

	env.mock.On("GET", `^/v2/api/site/default/settings/ips/available-categories$`, func(_ any, _ []string, q url.Values) (int, any) {
		return 200, js(`[{"id":"` + q.Get("memoryOptimized") + `"}]`)
	})
	cats := env.call("unifi_get_security_settings", `{"include_categories":true}`)
	eq(t, "categories", get(cats, "available_ips_categories"), js(`[{"id":"false"}]`))
}

func TestUpdateThreatManagement(t *testing.T) {
	env := opsEnv(t)
	res := env.call("unifi_update_threat_management", `{"ips_mode":"ips","dpi_enabled":false}`)
	eq(t, "mode", get(res, "threat_management", "ips_mode"), "ips")
	eq(t, "dpi", get(res, "traffic_identification", "enabled"), false)
	put := firstTo(env.mock, "/api/s/default/rest/setting/ips/s_ips", "PUT").Body
	eq(t, "mode sent", get(put, "ips_mode"), "ips")
	eq(t, "unchanged fields are kept", get(put, "enabled_categories"), js(`["emerging-malware"]`))
	contains(t, "empty", env.callErr("unifi_update_threat_management", nil), "Nothing to update")
}

func TestCountryBlocking(t *testing.T) {
	env := opsEnv(t)
	res := env.call("unifi_update_country_blocking", `{"enabled":true,"mode":"block","countries":["cn","ru"]}`)
	eq(t, "result", res, js(`{"enabled":true,"block":"block","countries":"CN,RU"}`))
	eq(t, "kept", get(lastTo(env.mock, "/api/s/default/rest/setting/usg").Body, "upnp_enabled"), false)
}

func TestThreats(t *testing.T) {
	env := opsEnv(t)
	res := env.list("unifi_list_threats", `{"hours":1}`)
	eq(t, "newest first", get(res, 0, "signature"), "ET MALWARE")
	eq(t, "action", get(res, 1, "action"), "blocked")
	eq(t, "time", get(res, 1, "time"), "2023-11-14T22:13:20.000Z")
	body := lastTo(env.mock, "/api/s/default/stat/ips/event").Body
	if end, start := ojson.ToNumber(get(body, "end")), ojson.ToNumber(get(body, "start")); end-start != 3600000 {
		t.Errorf("window %v", end-start)
	}
}

func TestContentFilters(t *testing.T) {
	env := opsEnv(t)
	eq(t, "name", get(env.list("unifi_list_content_filters", nil), 0, "name"), "Kids")
	env.call("unifi_save_content_filter", `{"id":"cf1","enabled":false}`)
	eq(t, "kept", get(lastTo(env.mock, "/v2/api/site/default/content-filtering/cf1").Body, "blocked_categories"), js(`["ADULT"]`))
	created := env.call("unifi_save_content_filter", `{"name":"Office","safe_search":["GOOGLE"]}`)
	eq(t, "created", get(created, "name"), "Office")
	eq(t, "schedule", get(lastTo(env.mock, "/v2/api/site/default/content-filtering/create").Body, "schedule", "mode"), "ALWAYS")
	env.call("unifi_delete_content_filter", `{"id":"cf1"}`)
	if m := lastTo(env.mock, "/v2/api/site/default/content-filtering/cf1").Method; m != "DELETE" {
		t.Errorf("method %s", m)
	}
}

func TestSiteSettings(t *testing.T) {
	env := opsEnv(t)
	keys := env.call("unifi_get_site_settings", nil)
	eq(t, "keys", get(keys, "keys"), js(`["dpi","guest_access","ips","mgmt","usg"]`))
	eq(t, "redacted", get(env.call("unifi_get_site_settings", `{"key":"mgmt"}`), "x_ssh_password"), "[redacted]")
	eq(t, "secret", get(env.call("unifi_get_site_settings", `{"key":"mgmt","include_secrets":true}`), "x_ssh_password"), "sshpw")
	contains(t, "missing", env.callErr("unifi_get_site_settings", `{"key":"nope"}`), `Setting "nope" not found; available: ips, dpi, usg, mgmt, guest_access`)

	res := env.call("unifi_update_site_setting", `{"key":"mgmt","changes":{"led_enabled":false}}`)
	eq(t, "led", get(res, "led_enabled"), false)
	eq(t, "redacted", get(res, "x_ssh_password"), "[redacted]")
	put := lastTo(env.mock, "/api/s/default/rest/setting/mgmt/s_mgmt")
	if put.Method != "PUT" || get(put.Body, "x_ssh_password") != "sshpw" {
		t.Errorf("secrets are preserved on write: %s %v", put.Method, put.Body)
	}
	contains(t, "_id", env.callErr("unifi_update_site_setting", `{"key":"mgmt","changes":{"_id":"x"}}`), `"_id" cannot be changed`)
	contains(t, "empty", env.callErr("unifi_update_site_setting", `{"key":"mgmt","changes":{}}`), "changes is empty")
}

func TestSites(t *testing.T) {
	env := opsEnv(t)
	eq(t, "created", env.call("unifi_create_site", `{"description":"Branch"}`), js(`{"id":"site2id","name":"abc123","description":"Branch"}`))
	env.call("unifi_rename_site", `{"site":"abc123","description":"Branch office"}`)
	eq(t, "rename", lastTo(env.mock, "/api/s/abc123/cmd/sitemgr").Body, js(`{"cmd":"update-site","desc":"Branch office"}`))
	eq(t, "delete result", env.call("unifi_delete_site", `{"site_name":"abc123"}`), js(`{"deleted":"abc123","description":"Branch"}`))
	eq(t, "delete", lastTo(env.mock, "/api/s/default/cmd/sitemgr").Body, js(`{"cmd":"delete-site","site":"site2id"}`))
	contains(t, "default", env.callErr("unifi_delete_site", `{"site_name":"default"}`), "The default site cannot be deleted")
	contains(t, "missing", env.callErr("unifi_delete_site", `{"site_name":"nope"}`), "No site named nope")
}

func TestAdmins(t *testing.T) {
	env := opsEnv(t)
	eq(t, "admins", get(env.list("unifi_list_admins", nil), 0), js(`{"id":"a1","name":"Matt","email":"m@example.com","role":"admin","super_admin":true}`))
	env.call("unifi_invite_admin", `{"name":" Sam ","email":"sam@example.com","role":"readonly","allow_restart":true}`)
	body := lastTo(env.mock, "/api/s/default/cmd/sitemgr").Body
	if s, _ := ojson.Stringify(body); s != `{"cmd":"invite-admin","name":"Sam","email":"sam@example.com","for_sso":true,"role":"readonly","permissions":["API_DEVICE_RESTART"]}` {
		t.Errorf("invite body %s", s)
	}
	env.call("unifi_revoke_admin", `{"admin_id":"a1"}`)
	eq(t, "revoke", lastTo(env.mock, "/api/s/default/cmd/sitemgr").Body, js(`{"cmd":"revoke-admin","admin":"a1"}`))
}

func TestBackups(t *testing.T) {
	env := opsEnv(t)
	eq(t, "list", get(env.list("unifi_list_backups", nil), 0), js(`{"filename":"autobackup_9.0.unf","time":"2023-11-14T22:13:20.000Z","version":"9.0.114","size_mb":2}`))
	created := env.call("unifi_create_backup", `{"days":0}`)
	eq(t, "path", get(created, "download_path"), "/dl/backup/9.0.unf")
	eq(t, "body", lastTo(env.mock, "/api/s/default/cmd/backup").Body, js(`{"cmd":"backup","days":0}`))
	env.call("unifi_delete_backup", `{"filename":"autobackup_9.0.unf"}`)
	eq(t, "delete", lastTo(env.mock, "/api/s/default/cmd/backup").Body, js(`{"cmd":"delete-backup","filename":"autobackup_9.0.unf"}`))
}

func TestCheckUpdates(t *testing.T) {
	env := opsEnv(t)
	res := env.call("unifi_check_updates", `{"refresh":true}`)
	eq(t, "version", get(res, "controller", "version"), "9.1.0")
	eq(t, "up to date", get(res, "devices_up_to_date"), 2.0)
	eq(t, "refresh", lastTo(env.mock, "/api/s/default/cmd/productinfo").Body, js(`{"cmd":"check-firmware-update"}`))
}

func TestEventsFallBackToSystemLog(t *testing.T) {
	env := opsEnv(t)
	res := env.call("unifi_list_events", `{"hours":2}`)
	contains(t, "source", ojson.String(get(res, "source")), "system-log")
	if n, _ := ojson.Len(get(res, "items")); n != 2 {
		t.Errorf("events: %d", n)
	}
	eq(t, "newest", get(res, "items", 0, "key"), "client-alert-key")
	contains(t, "legacy error", ojson.String(get(res, "legacy_error")), "POST /api/s/default/stat/event failed with HTTP 404: gone (endpoint not found")
}

func TestClassicEvents(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	env.mock.On("POST", `^/api/s/default/stat/event$`, fixed(unifitest.OK(js(`[{"time":1700000000,"key":"EVT_X","subsystem":"lan","msg":"hello"}]`))))
	eq(t, "events", env.list("unifi_list_events", nil), js(`[{"time":"2023-11-14T22:13:20.000Z","key":"EVT_X","subsystem":"lan","message":"hello"}]`))
	eq(t, "body", env.mock.Last().Body, js(`{"within":24,"_limit":100,"_sort":"-time"}`))
}

func TestSystemLog(t *testing.T) {
	env := opsEnv(t)
	res := env.call("unifi_get_system_log", `{"class":"threat-alert","page_size":10}`)
	eq(t, "total", get(res, "total"), 1.0)
	body := lastTo(env.mock, "/v2/api/site/default/system-log/threat-alert").Body
	eq(t, "filters", get(body, "threatTypes"), js(`["HONEYPOT","THREAT"]`))
	eq(t, "page size", get(body, "pageSize"), 10.0)
}

func TestDPIAndSessions(t *testing.T) {
	env := opsEnv(t)
	dpi := env.call("unifi_get_dpi_stats", `{"by":"app","macs":["11-22-33-44-55-66"]}`)
	eq(t, "mac", get(dpi, 0, "mac"), "11:22:33:44:55:66")
	eq(t, "body", lastTo(env.mock, "/api/s/default/stat/stadpi").Body, js(`{"type":"by_app","macs":["11:22:33:44:55:66"]}`))
	sessions := env.list("unifi_list_client_sessions", `{"mac":"11:22:33:44:55:66","hours":1}`)
	eq(t, "duration", get(sessions, 0, "duration"), "1h 0m")
	body := lastTo(env.mock, "/api/s/default/stat/session").Body
	if d := ojson.ToNumber(get(body, "end")) - ojson.ToNumber(get(body, "start")); d != 3600 {
		t.Errorf("session window is in seconds: %v", d)
	}
}

func TestReportsUseFixedClock(t *testing.T) {
	old := nowMs
	nowMs = func() float64 { return 1700000000000 }
	t.Cleanup(func() { nowMs = old })
	env := startMock(t, toolOptions(false, false))
	env.mock.On("POST", `^/api/s/default/stat/report/hourly\.site$`, fixed(unifitest.OK(js(`[{"oid":"x","o":"site","site_id":"s","time":1700000000000,"bytes":5}]`))))
	res := env.call("unifi_get_traffic_report", `{"scope":"site"}`)
	eq(t, "rows", res, js(`[{"time":"2023-11-14T22:13:20.000Z","bytes":5}]`))
	eq(t, "body", env.mock.Last().Body, js(`{"attrs":["bytes","wan-tx_bytes","wan-rx_bytes","wlan_bytes","num_sta","lan-num_sta","wlan-num_sta","time"],"start":1699395200000,"end":1700000000000}`))

	env.mock.On("GET", `^/v2/api/site/default/aggregated-dashboard$`, func(_ any, _ []string, q url.Values) (int, any) {
		return 200, ojson.New("historySeconds", q.Get("historySeconds"))
	})
	eq(t, "dashboard", env.call("unifi_get_dashboard", `{"hours":1.5}`), js(`{"historySeconds":"5400"}`))
}

func TestHotspot(t *testing.T) {
	env := opsEnv(t)
	eq(t, "ops", env.list("unifi_list_hotspot_operators", nil), js(`[{"id":"op1","name":"Front desk"}]`))
	created := env.call("unifi_save_hotspot_operator", `{"name":"Night","password":"pw123"}`)
	eq(t, "name", get(created, "name"), "Night")
	eq(t, "password sent", get(lastTo(env.mock, "/api/s/default/rest/hotspotop").Body, "x_password"), "pw123")
	eq(t, "portal redacted", get(env.call("unifi_get_guest_portal_settings", nil), "x_password"), "[redacted]")
	updated := env.call("unifi_update_guest_portal_settings", `{"portal_enabled":true,"auth":"password","session_minutes":480}`)
	eq(t, "auth", get(updated, "auth"), "password")
	eq(t, "expire", get(updated, "expire"), 480.0)
	eq(t, "unit", get(updated, "expire_unit"), 1.0)
	env.call("unifi_delete_hotspot_operator", `{"id":"op1"}`)
	if m := lastTo(env.mock, "/api/s/default/rest/hotspotop/op1").Method; m != "DELETE" {
		t.Errorf("method %s", m)
	}

	env.mock.Seed("rest", "voucher", `[]`)
	env.mock.On("POST", `^/api/s/default/cmd/hotspot$`, fixed(unifitest.OK(js(`[{"create_time":1700000000}]`)))).
		On("POST", `^/api/s/default/stat/voucher$`, fixed(unifitest.OK(js(`[{"_id":"v1","code":"1234567890","duration":1440,"quota":0,"create_time":1700000000}]`)))).
		On("GET", `^/api/s/default/stat/voucher$`, fixed(unifitest.OK(js(`[{"_id":"v1","code":"1234567890","duration":1440,"quota":1,"used":0,"create_time":1700000000}]`))))
	vouchers := env.call("unifi_create_vouchers", `{"minutes":1440,"note":"lobby"}`)
	eq(t, "vouchers", vouchers, js(`{"created":1,"vouchers":[{"id":"v1","code":"12345-67890","minutes":1440,"uses":"unlimited","created":"2023-11-14T22:13:20.000Z"}]}`))
	eq(t, "voucher cmd", lastTo(env.mock, "/api/s/default/cmd/hotspot").Body, js(`{"cmd":"create-voucher","n":1,"expire":1440,"quota":1,"note":"lobby"}`))
	eq(t, "list", env.list("unifi_list_vouchers", nil), js(`[{"id":"v1","code":"12345-67890","duration":"1d 0h 0m","quota":1,"used":0,"created":"2023-11-14T22:13:20.000Z"}]`))
	env.call("unifi_authorize_guest", `{"mac":"AA:BB:CC:DD:EE:FF","minutes":60}`)
	eq(t, "authorize", lastTo(env.mock, "/api/s/default/cmd/stamgr").Body, js(`{"cmd":"authorize-guest","mac":"aa:bb:cc:dd:ee:ff","minutes":60}`))
}

func TestOpsGating(t *testing.T) {
	env := opsEnv(t)
	w := toolMap(t, connect(t, env.client, toolOptions(true, false)))
	if w["unifi_update_site_setting"] == nil {
		t.Error("unifi_update_site_setting missing")
	}
	for _, n := range []string{"unifi_delete_site", "unifi_delete_backup", "unifi_revoke_admin", "unifi_delete_content_filter", "unifi_delete_hotspot_operator"} {
		if w[n] != nil {
			t.Errorf("%s should need UNIFI_ALLOW_DELETES", n)
		}
	}
	ro := toolMap(t, connect(t, env.client, toolOptions(false, false)))
	if ro["unifi_get_security_settings"] == nil || ro["unifi_get_system_log"] == nil || ro["unifi_update_threat_management"] != nil || ro["unifi_create_backup"] != nil {
		t.Error("read-only ops tools")
	}
}

func TestAlarmsAndRogues(t *testing.T) {
	env := opsEnv(t)
	env.mock.On("GET", `^/api/s/default/list/alarm$`, fixed(unifitest.OK(js(`[{"_id":"a","time":1,"msg":"old"},{"_id":"b","time":3,"archived":true},{"_id":"c","time":2,"msg":"new"}]`)))).
		On("POST", `^/api/s/default/stat/rogueap$`, fixed(unifitest.OK(js(`[{"essid":"weak","rssi":10},{"essid":"strong","rssi":40,"is_rogue":true},{"essid":"none"}]`))))
	eq(t, "alarms", env.list("unifi_list_alarms", nil), js(`[{"id":"c","time":"1970-01-01T00:00:02.000Z","message":"new"},{"id":"a","time":"1970-01-01T00:00:01.000Z","message":"old"}]`))
	if n, _ := ojson.Len(env.list("unifi_list_alarms", `{"include_archived":true,"limit":5}`)); n != 3 {
		t.Errorf("archived alarms: %d", n)
	}
	eq(t, "rogues", env.list("unifi_list_rogue_aps", nil), js(`[{"ssid":"strong","rssi":40,"rogue":true},{"ssid":"weak","rssi":10},{"ssid":"none"}]`))
	eq(t, "rogue only", env.list("unifi_list_rogue_aps", `{"rogue_only":true}`), js(`[{"ssid":"strong","rssi":40,"rogue":true}]`))
	env.call("unifi_archive_alarms", nil)
	eq(t, "archive all", env.mock.Last().Body, js(`{"cmd":"archive-all-alarms"}`))
}
