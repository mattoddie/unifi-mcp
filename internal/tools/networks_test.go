package tools

import (
	"sort"
	"strings"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

const (
	lanJSON = `{"_id":"net-lan","name":"Default","purpose":"corporate","networkgroup":"LAN","ip_subnet":"192.168.1.1/24","dhcpd_enabled":true,"dhcpd_start":"192.168.1.6","dhcpd_stop":"192.168.1.254","attr_no_delete":true}`
	wanJSON = `{"_id":"net-wan","name":"Primary (WAN1)","purpose":"wan","wan_networkgroup":"WAN","wan_type":"dhcp","wan_load_balance_type":"failover-only","wan_provider_capabilities":{"download_kilobits_per_second":100000,"upload_kilobits_per_second":20000}}`
	wgJSON  = `{"_id":"net-wg","name":"WireGuard","purpose":"remote-user-vpn","vpn_type":"wireguard-server","ip_subnet":"192.168.3.1/24","local_port":51820,"x_wireguard_private_key":"PRIVATE"}`
)

func networksEnv(t *testing.T) *testEnv {
	env := startMock(t, toolOptions(true, true))
	env.mock.
		Seed("rest", "networkconf", "["+lanJSON+","+wanJSON+","+wgJSON+"]").
		Seed("rest", "usergroup", `[{"_id":"ug-default","name":"Default","qos_rate_max_down":-1,"qos_rate_max_up":-1,"attr_no_delete":true}]`).
		Seed("rest", "wlanconf", `[{"_id":"w1","name":"Home","enabled":true,"security":"wpapsk","wpa_mode":"wpa2","x_passphrase":"hunter22","networkconf_id":"net-lan"}]`).
		Seed("v2", "apgroups", `[{"_id":"apg-all","name":"All APs","attr_no_delete":true}]`).
		Seed("v2", "static-dns", `[]`).
		Seed("rest", "dynamicdns", `[]`).
		Seed("rest", "radiusprofile", `[{"_id":"rp-default","name":"Default","attr_no_delete":true,"auth_servers":[]}]`).
		Seed("rest", "account", `[]`)
	env.mock.ClearRequests()
	return env
}

func TestSubnetRange(t *testing.T) {
	r, err := subnetRange("192.168.10.1/24")
	if err != nil || r != (SubnetRange{"192.168.10.1", "192.168.10.6", "192.168.10.254"}) {
		t.Errorf("/24: %+v %v", r, err)
	}
	r, err = subnetRange("10.0.0.1/30")
	if err != nil || r != (SubnetRange{"10.0.0.1", "10.0.0.2", "10.0.0.2"}) {
		t.Errorf("/30: %+v %v", r, err)
	}
	r, err = subnetRange("10.0.0.2/30")
	if err != nil || r != (SubnetRange{"10.0.0.2", "10.0.0.1", "10.0.0.1"}) {
		t.Errorf("/30 high gateway: %+v %v", r, err)
	}
	for in, want := range map[string]string{
		"10.0.0.0/24":   "gateway IP",
		"10.0.0.255/24": "gateway IP",
		"10.0.0.1":      "gateway/prefix",
		"10.0.0.1/31":   "prefix must be /8-/30",
		"300.0.0.1/24":  "prefix must be /8-/30",
	} {
		if _, err := subnetRange(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", in, err)
		}
	}
}

func TestCreateVLAN(t *testing.T) {
	env := networksEnv(t)
	res := env.call("unifi_save_network", `{"name":"IoT","vlan":20,"subnet":"192.168.20.1/24","dhcp_dns":["1.1.1.1"]}`)
	body := lastBody(env.mock, "POST")
	for k, want := range map[string]any{"purpose": "corporate", "networkgroup": "LAN", "vlan_enabled": true, "vlan": 20.0, "dhcpd_enabled": true, "dhcpd_start": "192.168.20.6", "dhcpd_stop": "192.168.20.254", "dhcpd_leasetime": 86400.0, "dhcpd_dns_enabled": true, "dhcpd_dns_1": "1.1.1.1", "dhcpd_dns_2": ""} {
		eq(t, k, get(body, k), want)
	}
	eq(t, "name", get(res, "name"), "IoT")
	eq(t, "range", get(res, "dhcp_range"), "192.168.20.6-192.168.20.254")
	contains(t, "vlan-only", env.callErr("unifi_save_network", `{"name":"X","purpose":"vlan-only"}`), "vlan is required for a vlan-only network")
	contains(t, "subnet", env.callErr("unifi_save_network", `{"name":"X"}`), "subnet is required when creating")
}

func TestUpdateNetwork(t *testing.T) {
	env := networksEnv(t)
	env.call("unifi_save_network", `{"id":"net-lan","domain_name":"home.arpa","igmp_snooping":false}`)
	body := lastBody(env.mock, "PUT")
	for k, want := range map[string]any{"domain_name": "home.arpa", "igmp_snooping": false, "ip_subnet": "192.168.1.1/24", "dhcpd_start": "192.168.1.6", "name": "Default"} {
		eq(t, k, get(body, k), want)
	}
}

func TestLANToolsRefuseWANAndVPN(t *testing.T) {
	env := networksEnv(t)
	contains(t, "edit wan", env.callErr("unifi_save_network", `{"id":"net-wan","name":"x"}`), "unifi_update_wan")
	contains(t, "edit vpn", env.callErr("unifi_save_network", `{"id":"net-wg","name":"x"}`), "unifi_save_vpn")
	env.callErr("unifi_delete_network", `{"id":"net-wan"}`)
	contains(t, "built-in", env.callErr("unifi_delete_network", `{"id":"net-lan"}`), "built-in")
	for _, r := range env.mock.Requests() {
		if r.Method == "DELETE" {
			t.Error("a network was deleted")
		}
	}
}

func TestDeleteLAN(t *testing.T) {
	env := networksEnv(t)
	env.mock.Seed("rest", "networkconf", "["+lanJSON+`,{"_id":"net-iot","name":"IoT","purpose":"corporate"}]`)
	eq(t, "result", env.call("unifi_delete_network", `{"id":"net-iot"}`), js(`{"deleted":"net-iot","name":"IoT"}`))
	var ids []any
	for _, n := range env.mock.Store("rest/networkconf") {
		ids = append(ids, ojson.Get(n, "_id"))
	}
	eq(t, "left", ids, js(`["net-lan"]`))
}

func TestWANs(t *testing.T) {
	env := networksEnv(t)
	list := env.call("unifi_list_wans", nil)
	if n, _ := ojson.Len(get(list, "wans")); n != 1 {
		t.Errorf("wans: %d", n)
	}
	eq(t, "isp down", get(list, "wans", 0, "isp_download_kbps"), 100000.0)

	res := env.call("unifi_update_wan", `{"id":"net-wan","type":"static","static_ip":"203.0.113.10","netmask":"255.255.255.0","gateway":"203.0.113.1","dns":["9.9.9.9"],"smart_queues":true,"download_kbps":90000}`)
	body := lastBody(env.mock, "PUT")
	for k, want := range map[string]any{"wan_type": "static", "wan_ip": "203.0.113.10", "wan_dns_preference": "manual", "wan_dns1": "9.9.9.9", "wan_dns2": "", "wan_smartq_enabled": true, "wan_smartq_down_rate": 90000.0} {
		eq(t, k, get(body, k), want)
	}
	eq(t, "capabilities", get(body, "wan_provider_capabilities"), js(`{"download_kilobits_per_second":90000,"upload_kilobits_per_second":20000}`))
	eq(t, "static", get(res, "static", "ip"), "203.0.113.10")

	contains(t, "not a wan", env.callErr("unifi_update_wan", `{"id":"net-lan","type":"dhcp"}`), "not a WAN")
	contains(t, "pppoe", env.callErr("unifi_update_wan", `{"id":"net-wan","pppoe_username":"x"}`), "only apply to a PPPoE WAN")
}

func TestCreateWLAN(t *testing.T) {
	env := networksEnv(t)
	res := env.call("unifi_save_wlan", `{"name":"Guests","passphrase":"welcome123","wpa3":"transition","network_id":"net-lan","client_isolation":true}`)
	body := lastBody(env.mock, "POST")
	for k, want := range map[string]any{"security": "wpapsk", "x_passphrase": "welcome123", "wpa3_support": true, "wpa3_transition": true, "usergroup_id": "ug-default", "l2_isolation": true, "ap_group_mode": "all", "wpa_enc": "ccmp"} {
		eq(t, k, get(body, k), want)
	}
	eq(t, "ap groups", get(body, "ap_group_ids"), js(`["apg-all"]`))
	eq(t, "bands", get(body, "wlan_bands"), js(`["2g","5g"]`))
	eq(t, "no passphrase", get(res, "passphrase"), ojson.Undefined)
	eq(t, "wpa3", get(res, "wpa3"), "transition")

	contains(t, "passphrase", env.callErr("unifi_save_wlan", `{"name":"X","security":"wpapsk"}`), "passphrase is required")
	contains(t, "radius", env.callErr("unifi_save_wlan", `{"name":"X","security":"wpaeap"}`), "radius_profile_id is required")
	env.call("unifi_save_wlan", `{"name":"Open"}`)
	if b := lastBody(env.mock, "POST"); get(b, "security") != "open" || !ojson.IsUndefined(get(b, "x_passphrase")) {
		t.Errorf("open WLAN body: %v", b)
	}
}

func TestUpdateWLAN(t *testing.T) {
	env := networksEnv(t)
	env.call("unifi_save_wlan", `{"id":"w1","hidden":true,"mac_filter":{"policy":"allow","macs":["AA-BB-CC-DD-EE-FF"]}}`)
	body := lastBody(env.mock, "PUT")
	for k, want := range map[string]any{"hide_ssid": true, "x_passphrase": "hunter22", "name": "Home", "mac_filter_policy": "allow", "mac_filter_enabled": true} {
		eq(t, k, get(body, k), want)
	}
	eq(t, "macs", get(body, "mac_filter_list"), js(`["aa:bb:cc:dd:ee:ff"]`))
	env.call("unifi_save_wlan", `{"id":"w1","mac_filter":null}`)
	eq(t, "filter off", get(lastBody(env.mock, "PUT"), "mac_filter_enabled"), false)
	eq(t, "deleted", env.call("unifi_delete_wlan", `{"id":"w1"}`), js(`{"deleted":"w1","name":"Home"}`))
}

func TestWiFiQR(t *testing.T) {
	env := networksEnv(t)
	env.mock.Seed("rest", "wlanconf", `[{"_id":"w2","name":"Cafe;Net","security":"wpapsk","x_passphrase":"p:a\"ss","hide_ssid":true},{"_id":"w3","name":"Open","security":"open"},{"_id":"w4","name":"Corp","security":"wpaeap"},{"_id":"w5","name":"New","security":"wpapsk","wpa3_support":true,"x_passphrase":"x"}]`)
	eq(t, "escaped", get(env.call("unifi_get_wifi_qr", `{"id":"w2"}`), "wifi_string"), `WIFI:T:WPA;S:Cafe\;Net;P:p\:a\"ss;H:true;;`)
	eq(t, "open", get(env.call("unifi_get_wifi_qr", `{"id":"w3"}`), "wifi_string"), `WIFI:T:nopass;S:Open;;`)
	eq(t, "sae", get(env.call("unifi_get_wifi_qr", `{"id":"w5"}`), "wifi_string"), `WIFI:T:SAE;S:New;P:x;;`)
	contains(t, "enterprise", env.callErr("unifi_get_wifi_qr", `{"id":"w4"}`), "cannot be joined with a QR code")
}

func TestDNSRecords(t *testing.T) {
	env := networksEnv(t)
	created := env.call("unifi_save_dns_record", `{"name":"nas.home.arpa","value":"192.168.1.10"}`)
	if s, _ := ojson.Stringify(lastBody(env.mock, "POST")); s != `{"key":"nas.home.arpa","value":"192.168.1.10","record_type":"A","ttl":0,"enabled":true}` {
		t.Errorf("body: %s", s)
	}
	id := ojson.String(get(created, "id"))
	env.call("unifi_save_dns_record", `{"id":"`+id+`","value":"192.168.1.11"}`)
	eq(t, "stored", ojson.Get(env.mock.Store("v2/static-dns")[0], "value"), "192.168.1.11")
	list := env.call("unifi_list_dns_records", nil)
	eq(t, "list", get(list, 0), js(`{"id":"`+id+`","type":"A","name":"nas.home.arpa","value":"192.168.1.11","ttl":0,"enabled":true}`))
	env.call("unifi_delete_dns_record", `{"id":"`+id+`"}`)
	if n := len(env.mock.Store("v2/static-dns")); n != 0 {
		t.Errorf("records left: %d", n)
	}
	contains(t, "required", env.callErr("unifi_save_dns_record", `{"name":"x"}`), "name and value are required")
}

func TestBandwidthProfiles(t *testing.T) {
	env := networksEnv(t)
	created := env.call("unifi_save_bandwidth_profile", `{"name":"Kids","download_kbps":10000}`)
	eq(t, "body", lastBody(env.mock, "POST"), js(`{"name":"Kids","qos_rate_max_down":10000,"qos_rate_max_up":-1}`))
	eq(t, "upload", get(created, "upload_kbps"), "unlimited")
	contains(t, "default", env.callErr("unifi_delete_bandwidth_profile", `{"id":"ug-default"}`), "cannot be deleted")
	list := env.call("unifi_list_bandwidth_profiles", nil)
	eq(t, "default", get(list, 0), js(`{"id":"ug-default","name":"Default","download_kbps":"unlimited","upload_kbps":"unlimited","default":true}`))
}

func TestDynamicDNS(t *testing.T) {
	env := networksEnv(t)
	env.call("unifi_save_dynamic_dns", `{"service":"duckdns","hostname":"me.duckdns.org","password":"tok"}`)
	eq(t, "interface", get(lastBody(env.mock, "POST"), "interface"), "wan")
	eq(t, "redacted", get(env.call("unifi_list_dynamic_dns", nil), 0, "password"), "[redacted]")
	eq(t, "secret", get(env.call("unifi_list_dynamic_dns", `{"include_secrets":true}`), 0, "password"), "tok")
}

func TestVPNs(t *testing.T) {
	env := networksEnv(t)
	created := env.call("unifi_save_vpn", `{"kind":"wireguard-server","name":"WG2","subnet":"192.168.4.1/24"}`)
	body := lastBody(env.mock, "POST")
	for k, want := range map[string]any{"purpose": "remote-user-vpn", "vpn_type": "wireguard-server", "local_port": 51820.0, "enabled": true} {
		eq(t, k, get(body, k), want)
	}
	eq(t, "kind", get(created, "kind"), "wireguard-server")

	contains(t, "psk", env.callErr("unifi_save_vpn", `{"kind":"l2tp-server","name":"L2TP","subnet":"192.168.5.1/24"}`), "pre_shared_key is required")
	env.call("unifi_save_vpn", `{"kind":"l2tp-server","name":"L2TP","subnet":"192.168.5.1/24","pre_shared_key":"psk"}`)
	eq(t, "radius", get(lastBody(env.mock, "POST"), "radiusprofile_id"), "rp-default")

	list := env.call("unifi_list_vpns", nil)
	var kinds []string
	for _, v := range list.([]any) {
		kinds = append(kinds, ojson.String(ojson.Get(v, "kind")))
	}
	sort.Strings(kinds)
	eq(t, "kinds", ojson.Strings(kinds), js(`["l2tp-server","wireguard-server","wireguard-server"]`))
	raw := env.call("unifi_list_vpns", `{"raw":true}`)
	wg := ojson.Find(raw.([]any), func(v any) bool { return ojson.Get(v, "_id") == "net-wg" })
	eq(t, "private key", ojson.Get(wg, "x_wireguard_private_key"), "[redacted]")

	contains(t, "not a vpn", env.callErr("unifi_delete_vpn", `{"id":"net-lan"}`), "not a VPN")
	contains(t, "kind change", env.callErr("unifi_save_vpn", `{"id":"net-wg","kind":"openvpn-server"}`), "kind cannot be changed")
	contains(t, "site-to-site", env.callErr("unifi_save_vpn", `{"kind":"ipsec-site-to-site","name":"S2S"}`), "peer_ip and remote_subnets are required")
	env.call("unifi_save_vpn", `{"id":"net-wg","wan_interface":"wan2","dns":["1.1.1.1"]}`)
	put := lastBody(env.mock, "PUT")
	eq(t, "wg interface", get(put, "wireguard_interface"), "wan2")
	eq(t, "dns", []any{get(put, "dhcpd_dns_enabled"), get(put, "dhcpd_dns_1"), get(put, "dhcpd_dns_2")}, js(`[true,"1.1.1.1",""]`))
}

func TestRadius(t *testing.T) {
	env := networksEnv(t)
	env.call("unifi_save_radius_profile", `{"name":"NPS","auth_servers":[{"ip":"10.0.0.5","secret":"s3"}]}`)
	eq(t, "servers", get(lastBody(env.mock, "POST"), "auth_servers"), js(`[{"ip":"10.0.0.5","port":1812,"x_secret":"s3"}]`))
	profiles := env.call("unifi_list_radius_profiles", nil)
	eq(t, "summary", get(profiles, 1, "auth_servers"), js(`[{"ip":"10.0.0.5","port":1812}]`))
	contains(t, "built-in", env.callErr("unifi_delete_radius_profile", `{"id":"rp-default"}`), "built-in RADIUS profile")

	contains(t, "together", env.callErr("unifi_save_radius_account", `{"name":"bob","password":"pw","tunnel_type":13}`), "set together")
	env.call("unifi_save_radius_account", `{"name":"bob","password":"pw","vlan":30,"tunnel_type":13,"tunnel_medium_type":6}`)
	accounts := env.call("unifi_list_radius_accounts", nil)
	eq(t, "account", get(accounts, 0), js(`{"id":`+mustJSON(get(accounts, 0, "id"))+`,"name":"bob","vlan":30,"tunnel_type":13,"tunnel_medium_type":6}`))
	eq(t, "password", get(env.call("unifi_list_radius_accounts", `{"include_secrets":true}`), 0, "password"), "pw")
}

func mustJSON(v any) string {
	s, _ := ojson.Stringify(v)
	return s
}

func TestNetworkGating(t *testing.T) {
	env := networksEnv(t)
	ro := toolMap(t, connect(t, env.client, toolOptions(false, false)))
	if ro["unifi_list_wans"] == nil || ro["unifi_list_vpns"] == nil || ro["unifi_save_wlan"] != nil {
		t.Error("read-only network tools")
	}
	w := toolMap(t, connect(t, env.client, toolOptions(true, false)))
	if w["unifi_save_wlan"] == nil || w["unifi_save_vpn"] == nil {
		t.Error("write network tools")
	}
	for _, n := range []string{"unifi_delete_network", "unifi_delete_wlan", "unifi_delete_vpn", "unifi_delete_dns_record", "unifi_delete_radius_account", "unifi_delete_bandwidth_profile", "unifi_delete_radius_profile"} {
		if w[n] != nil {
			t.Errorf("%s without deletes", n)
		}
		if toolMap(t, env.mcp)[n] == nil {
			t.Errorf("%s missing with deletes", n)
		}
	}
}
