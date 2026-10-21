package tools

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

const (
	switchJSON = `{"_id":"d1","mac":"aa:bb:cc:00:00:01","name":"Office Switch","model":"US8P60","port_table":[
		{"port_idx":1,"name":"Port 1","up":true,"enable":true,"speed":1000,"poe_mode":"auto","portconf_id":"pc-all"},
		{"port_idx":2,"name":"Port 2","up":false,"enable":true,"poe_mode":"auto","portconf_id":"pc-all"},
		{"port_idx":3,"name":"Port 3","up":true,"enable":true,"speed":100}],
		"port_overrides":[{"port_idx":2,"name":"Printer","portconf_id":"pc-iot"}]}`
	apJSON = `{"_id":"d2","mac":"aa:bb:cc:00:00:02","name":"Lounge AP","type":"uap","radio_table":[
		{"radio":"ng","channel":6,"ht":20,"tx_power_mode":"auto"},
		{"radio":"na","channel":"auto","ht":80,"tx_power_mode":"auto","min_rssi_enabled":false}]}`
	plugJSON = `{"_id":"d3","mac":"aa:bb:cc:00:00:03","name":"Modem plug","type":"usp","outlet_table":[{"index":1,"name":"Outlet 1","relay_state":true,"cycle_enabled":false}],"outlet_overrides":[]}`
)

type switchEnv struct {
	*testEnv
	mark int
}

func newSwitchEnv(t *testing.T) *switchEnv {
	env := startMock(t, toolOptions(true, true))
	for _, dev := range []string{switchJSON, apJSON, plugJSON} {
		d := js(dev)
		env.mock.On("GET", "^/api/s/default/stat/device/"+ojson.String(ojson.Get(d, "mac"))+"$", func(any, []string, url.Values) (int, any) {
			return unifitest.OK([]any{ojson.Clone(d)})
		})
	}
	env.mock.
		On("PUT", `^/api/s/default/rest/device/([^/]+)$`, func(body any, m []string, _ url.Values) (int, any) {
			return unifitest.OK([]any{ojson.New("_id", m[1]).Assign(body)})
		}).
		On("GET", `^/api/s/default/stat/spectrum-scan/aa:bb:cc:00:00:02$`, fixed(unifitest.OK(js(`[{"spectrum_scanning":false,"spectrum_scan_last_ts":1700000000,"spectrum_table":[{"channel":36,"utilization":12}]}]`)))).
		On("GET", `^/api/self/sites$`, fixed(unifitest.OK(js(`[{"_id":"0123456789abcdef01234567","name":"default","desc":"Default"},{"_id":"89abcdef0123456789abcdef","name":"branch","desc":"Branch office"}]`)))).
		On("POST", `^/api/s/default/cmd/firmware$`, fixed(unifitest.OK(js(`[{"version":"7.1.66","device":"US8P60"}]`)))).
		Seed("rest", "portconf", `[{"_id":"pc-all","name":"All","forward":"all"},{"_id":"pc-iot","name":"IoT","native_networkconf_id":"n-iot"}]`).
		Seed("v2", "apgroups", `[{"_id":"ag1","name":"All APs","device_macs":["aa:bb:cc:00:00:02"],"attr_no_delete":true}]`)
	return &switchEnv{testEnv: env}
}

// begin marks the start of the requests sent() reports.
func (e *switchEnv) begin() { e.mark = len(e.mock.Requests()) }

// sent are the non-GET requests since begin().
func (e *switchEnv) sent() []unifitest.Request {
	var out []unifitest.Request
	for _, r := range e.mock.Requests()[e.mark:] {
		if r.Method != "GET" {
			out = append(out, r)
		}
	}
	return out
}

func TestSwitchingGating(t *testing.T) {
	env := newSwitchEnv(t)
	ro := toolMap(t, connect(t, env.client, toolOptions(false, false)))
	if ro["unifi_list_switch_ports"] == nil || ro["unifi_get_rf_scan"] == nil || ro["unifi_set_switch_port"] != nil || ro["unifi_forget_device"] != nil {
		t.Error("read-only switching tools")
	}
	w := toolMap(t, connect(t, env.client, toolOptions(true, false)))
	if w["unifi_set_switch_port"] == nil || w["unifi_delete_port_profile"] != nil || w["unifi_forget_device"] != nil {
		t.Error("write switching tools")
	}
}

func TestListSwitchPorts(t *testing.T) {
	env := newSwitchEnv(t)
	res := env.call("unifi_list_switch_ports", `{"mac":"aa:bb:cc:00:00:01"}`)
	if n, _ := ojson.Len(get(res, "items")); n != 3 {
		t.Errorf("ports: %d", n)
	}
	eq(t, "switch", get(res, "switch"), js(`{"name":"Office Switch","mac":"aa:bb:cc:00:00:01","model":"US8P60"}`))
	eq(t, "profile 1", get(res, "items", 0, "profile_name"), "All")
	eq(t, "profile 2", get(res, "items", 1, "profile_name"), "IoT")
	eq(t, "override", get(res, "items", 1, "override"), js(`{"name":"Printer","portconf_id":"pc-iot"}`))
	eq(t, "port 1", get(res, "items", 0), js(`{"idx":1,"name":"Port 1","up":true,"enabled":true,"speed_mbps":1000,"poe_mode":"auto","profile_id":"pc-all","profile_name":"All"}`))
	contains(t, "no ports", env.callErr("unifi_list_switch_ports", `{"mac":"aa:bb:cc:00:00:02"}`), "Device Lounge AP has no switch ports")
}

func TestSetSwitchPort(t *testing.T) {
	env := newSwitchEnv(t)
	env.begin()
	env.call("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:01","port_idx":1,"name":"Desk","poe_mode":"off","speed":100,"tagged_vlan_mgmt":"block_all"}`)
	req := env.sent()[0]
	if req.Method != "PUT" || !strings.HasSuffix(req.Path, "/rest/device/d1") {
		t.Errorf("request %s %s", req.Method, req.Path)
	}
	want := `{"port_overrides":[{"port_idx":1,"portconf_id":"pc-all","name":"Desk","tagged_vlan_mgmt":"block_all","poe_mode":"off","autoneg":false,"speed":100,"full_duplex":true},{"port_idx":2,"name":"Printer","portconf_id":"pc-iot"}]}`
	if s, _ := ojson.Stringify(req.Body); s != want {
		t.Errorf("body (with key order):\n got  %s\n want %s", s, want)
	}

	env.begin()
	env.call("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:01","port_idx":2,"enabled":false}`)
	eq(t, "disable", get(env.sent()[0].Body, "port_overrides"), js(`[{"port_idx":2,"name":"Printer","portconf_id":"pc-iot","port_security_enabled":true,"port_security_mac_address":[]}]`))
}

func TestSwitchPortErrors(t *testing.T) {
	env := newSwitchEnv(t)
	env.begin()
	contains(t, "bad port", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:01","port_idx":9,"name":"x"}`), "valid ports: 1, 2, 3")
	contains(t, "empty", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:01","port_idx":1}`), "Nothing to change")
	if len(env.sent()) != 0 {
		t.Error("errors sent requests")
	}
}

func TestResetSwitchPort(t *testing.T) {
	env := newSwitchEnv(t)
	env.begin()
	res := env.call("unifi_reset_switch_port", `{"mac":"aa:bb:cc:00:00:01","port_idx":[2,3]}`)
	eq(t, "reset", get(res, "reset"), js(`[2]`))
	eq(t, "already", get(res, "already_default"), js(`[3]`))
	eq(t, "body", env.sent()[0].Body, js(`{"port_overrides":[]}`))
}

func TestPortProfiles(t *testing.T) {
	env := newSwitchEnv(t)
	env.begin()
	created := env.call("unifi_save_port_profile", `{"name":"Cameras","native_network_id":"n-cam","poe_mode":"auto","isolation":true}`)
	eq(t, "name", get(created, "name"), "Cameras")
	eq(t, "body", env.sent()[0].Body, js(`{"name":"Cameras","native_networkconf_id":"n-cam","poe_mode":"auto","isolation":true}`))

	id := ojson.String(get(created, "id"))
	env.begin()
	updated := env.call("unifi_save_port_profile", `{"id":"`+id+`","speed":"auto"}`)
	eq(t, "network kept", get(updated, "native_network_id"), "n-cam")
	put := env.sent()[0]
	if put.Method != "PUT" || get(put.Body, "autoneg") != true || get(put.Body, "name") != "Cameras" {
		t.Errorf("update: %s %v", put.Method, put.Body)
	}

	env.begin()
	env.call("unifi_delete_port_profile", `{"id":"`+id+`"}`)
	if env.sent()[0].Method != "DELETE" {
		t.Error("not a DELETE")
	}
	for _, p := range env.mock.Store("rest/portconf") {
		if ojson.Get(p, "_id") == id {
			t.Error("profile not deleted")
		}
	}
	list := env.list("unifi_list_port_profiles", nil)
	eq(t, "list", list, js(`[{"id":"pc-all","name":"All","forward":"all","speed":"auto"},{"id":"pc-iot","name":"IoT","native_network_id":"n-iot","speed":"auto"}]`))
}

func TestUpdateDevice(t *testing.T) {
	env := newSwitchEnv(t)
	env.begin()
	env.call("unifi_update_device", `{"mac":"aa:bb:cc:00:00:01","name":"Core","led":"off","disabled":false,"ip_config":{"type":"static","ip":"10.0.0.2","netmask":"255.255.255.0","gateway":"10.0.0.1","dns1":"1.1.1.1"}}`)
	req := env.sent()[0]
	if !strings.HasSuffix(req.Path, "/rest/device/d1") {
		t.Errorf("path %s", req.Path)
	}
	eq(t, "body", req.Body, js(`{"name":"Core","led_override":"off","disabled":false,"config_network":{"type":"static","ip":"10.0.0.2","netmask":"255.255.255.0","gateway":"10.0.0.1","dns1":"1.1.1.1"}}`))

	env.begin()
	contains(t, "static", env.callErr("unifi_update_device", `{"mac":"aa:bb:cc:00:00:01","ip_config":{"type":"static","ip":"10.0.0.9"}}`), "needs ip, netmask and gateway")
	contains(t, "nothing", env.callErr("unifi_update_device", `{"mac":"aa:bb:cc:00:00:01"}`), "Nothing to update")
	if len(env.sent()) != 0 {
		t.Error("errors sent requests")
	}
}

func TestSetRadio(t *testing.T) {
	env := newSwitchEnv(t)
	env.begin()
	res := env.call("unifi_set_radio", `{"mac":"aa:bb:cc:00:00:02","radio":"na","channel":44,"channel_width":40,"tx_power":14,"min_rssi":-75}`)
	eq(t, "radio", get(res, "radio"), js(`{"radio":"na","channel":44,"channel_width":40,"tx_power_mode":"custom","tx_power":14,"min_rssi":-75}`))
	eq(t, "table", get(env.sent()[0].Body, "radio_table"), js(`[{"radio":"ng","channel":6,"ht":20,"tx_power_mode":"auto"},{"radio":"na","channel":44,"ht":40,"tx_power_mode":"custom","tx_power":14,"min_rssi_enabled":true,"min_rssi":-75}]`))
	contains(t, "missing", env.callErr("unifi_set_radio", `{"mac":"aa:bb:cc:00:00:02","radio":"6e","channel":5}`), "radios: ng, na")
	env.call("unifi_set_radio", `{"mac":"aa:bb:cc:00:00:02","radio":"ng","channel":"auto","min_rssi":null}`)
	eq(t, "auto/null", get(env.mock.Last().Body, "radio_table", 0), js(`{"radio":"ng","channel":"auto","ht":20,"tx_power_mode":"auto","min_rssi_enabled":false}`))
}

func TestSetOutlet(t *testing.T) {
	env := newSwitchEnv(t)
	env.begin()
	env.call("unifi_set_outlet", `{"mac":"aa:bb:cc:00:00:03","index":1,"on":false}`)
	eq(t, "body", env.sent()[0].Body, js(`{"outlet_overrides":[{"index":1,"name":"Outlet 1","relay_state":false,"cycle_enabled":false}]}`))
	contains(t, "missing", env.callErr("unifi_set_outlet", `{"mac":"aa:bb:cc:00:00:03","index":4,"on":false}`), "Modem plug has no outlet 4")
}

func TestDeviceCommands(t *testing.T) {
	env := newSwitchEnv(t)
	env.begin()
	for _, c := range [][2]string{
		{"unifi_upgrade_device", `{"mac":"aa:bb:cc:00:00:01","firmware_url":"https://fw.example/us8.bin"}`},
		{"unifi_rolling_upgrade", `{"action":"start","types":["uap"]}`},
		{"unifi_rolling_upgrade", `{"action":"cancel"}`},
		{"unifi_move_device", `{"mac":"AA:BB:CC:00:00:02","target_site":"branch"}`},
		{"unifi_forget_device", `{"mac":"aa:bb:cc:00:00:02"}`},
		{"unifi_run_rf_scan", `{"mac":"aa:bb:cc:00:00:02"}`},
		{"unifi_restart_device", `{"mac":"aa:bb:cc:00:00:01","reboot_type":"hard"}`},
		{"unifi_locate_device", `{"mac":"aa:bb:cc:00:00:01","enabled":true}`},
		{"unifi_provision_device", `{"mac":"aa:bb:cc:00:00:01"}`},
		{"unifi_adopt_device", `{"mac":"aa:bb:cc:00:00:01"}`},
	} {
		env.call(c[0], c[1])
	}
	var got []any
	for _, r := range env.sent() {
		got = append(got, []any{strings.TrimPrefix(r.Path, "/proxy/network/api/s/default/"), r.Body})
	}
	eq(t, "commands", got, js(`[
		["cmd/devmgr/upgrade-external",{"url":"https://fw.example/us8.bin","macs":["aa:bb:cc:00:00:01"]}],
		["cmd/devmgr/set-rollupgrade",["uap"]],
		["cmd/devmgr",{"cmd":"unset-rollupgrade"}],
		["cmd/sitemgr",{"cmd":"move-device","mac":"aa:bb:cc:00:00:02","site":"89abcdef0123456789abcdef"}],
		["cmd/sitemgr",{"cmd":"delete-device","mac":"aa:bb:cc:00:00:02"}],
		["cmd/devmgr",{"cmd":"spectrum-scan","mac":"aa:bb:cc:00:00:02"}],
		["cmd/devmgr",{"cmd":"restart","mac":"aa:bb:cc:00:00:01","reboot_type":"hard"}],
		["cmd/devmgr",{"cmd":"set-locate","mac":"aa:bb:cc:00:00:01"}],
		["cmd/devmgr",{"cmd":"force-provision","mac":"aa:bb:cc:00:00:01"}],
		["cmd/devmgr",{"cmd":"adopt","mac":"aa:bb:cc:00:00:01"}]]`))
	contains(t, "bad mac", env.callErr("unifi_restart_device", `{"mac":"nope"}`), "Invalid MAC address: nope")
	contains(t, "bad site", env.callErr("unifi_move_device", `{"mac":"aa:bb:cc:00:00:02","target_site":"nope"}`), `No site named "nope"; available: default (Default), branch (Branch office)`)
}

func TestRFScanAndFirmware(t *testing.T) {
	env := newSwitchEnv(t)
	scan := env.call("unifi_get_rf_scan", `{"mac":"aa:bb:cc:00:00:02"}`)
	eq(t, "scan", scan, js(`{"scanning":false,"last_scan":"2023-11-14T22:13:20.000Z","results":[{"channel":36,"utilization":12}]}`))

	env.begin()
	fw := env.call("unifi_get_firmware_status", `{"include_available":true,"refresh":true}`)
	if n, _ := ojson.Len(get(fw, "devices")); n != 2 {
		t.Errorf("devices: %d", n)
	}
	eq(t, "available", get(fw, "available"), js(`[{"version":"7.1.66","device":"US8P60"}]`))
	var bodies []any
	for _, r := range env.sent() {
		bodies = append(bodies, r.Body)
	}
	eq(t, "commands", bodies, js(`[{"cmd":"check-firmware-update"},{"cmd":"list-available"}]`))
}

func TestGetDevice(t *testing.T) {
	env := newSwitchEnv(t)
	dev := env.call("unifi_get_device", `{"mac":"aa:bb:cc:00:00:01"}`)
	eq(t, "ports", get(dev, "ports", 0), js(`{"idx":1,"name":"Port 1","up":true,"enabled":true,"speed_mbps":1000,"poe_mode":"auto","portconf_id":"pc-all"}`))
	eq(t, "overrides", get(dev, "port_overrides"), js(`[{"port_idx":2,"name":"Printer","portconf_id":"pc-iot"}]`))
	ap := env.call("unifi_get_device", `{"mac":"aa:bb:cc:00:00:02"}`)
	eq(t, "radio config", get(ap, "radio_config"), js(`[{"radio":"ng","channel":6,"channel_width":20,"tx_power_mode":"auto"},{"radio":"na","channel":"auto","channel_width":80,"tx_power_mode":"auto"}]`))
	contains(t, "missing", env.callErr("unifi_get_device", `{"mac":"aa:bb:cc:00:00:09"}`), "GET /api/s/default/stat/device/aa:bb:cc:00:00:09 failed with HTTP 404")
}

func TestAPGroups(t *testing.T) {
	env := newSwitchEnv(t)
	list := env.list("unifi_list_ap_groups", nil)
	eq(t, "list", get(list, 0), js(`{"id":"ag1","name":"All APs","device_macs":["aa:bb:cc:00:00:02"],"device_count":1,"default":true}`))

	env.begin()
	created := env.call("unifi_save_ap_group", `{"name":"Upstairs","device_macs":["AA-BB-CC-00-00-02"]}`)
	eq(t, "body", env.sent()[0].Body, js(`{"name":"Upstairs","device_macs":["aa:bb:cc:00:00:02"]}`))

	id := ojson.String(get(created, "id"))
	env.begin()
	renamed := env.call("unifi_save_ap_group", `{"id":"`+id+`","name":"First floor"}`)
	eq(t, "renamed", get(renamed, "name"), "First floor")
	eq(t, "macs", get(renamed, "device_macs"), js(`["aa:bb:cc:00:00:02"]`))
	if env.sent()[0].Method != "PUT" {
		t.Error("not a PUT")
	}

	env.begin()
	env.call("unifi_delete_ap_group", `{"id":"`+id+`"}`)
	if env.sent()[0].Method != "DELETE" || len(env.mock.Store("v2/apgroups")) != 1 {
		t.Error("AP group not deleted")
	}
	contains(t, "name", env.callErr("unifi_save_ap_group", nil), "name is required when creating an AP group")
}
