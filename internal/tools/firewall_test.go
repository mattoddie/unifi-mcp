package tools

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

const siteUUID = "88f7af54-98f8-306a-a1c7-c9349722b1f6"

func firewallEnv(t *testing.T) *testEnv {
	env := startMock(t, toolOptions(true, true))
	env.mock.
		Seed("rest", "firewallrule", `[
			{"_id":"r1","name":"Existing","ruleset":"LAN_IN","rule_index":2000,"action":"drop","enabled":true,"protocol":"all"},
			{"_id":"r2","name":"Later","ruleset":"LAN_IN","rule_index":2003,"action":"accept","enabled":true,"protocol":"all"}]`).
		Seed("rest", "firewallgroup", `[{"_id":"g1","name":"Servers","group_type":"address-group","group_members":["10.0.0.5"]}]`).
		Seed("rest", "portforward", `[{"_id":"p1","name":"Web","enabled":true,"proto":"tcp","pfwd_interface":"wan","src":"any","dst_port":"443","fwd":"10.0.0.5","fwd_port":"443","log":false}]`).
		Seed("rest", "routing", `[{"_id":"s1","name":"Lab","enabled":true,"type":"static-route","static-route_type":"nexthop-route","static-route_network":"10.9.0.0/16","static-route_nexthop":"10.0.0.254","static-route_distance":1}]`).
		Seed("v2", "firewall-policies", `[
			{"_id":"fp1","name":"Block IoT to LAN","action":"BLOCK","enabled":true,"index":10000,"predefined":false,"source":{"zone_id":"z1","matching_target":"ANY"},"destination":{"zone_id":"z2","matching_target":"ANY"}},
			{"_id":"fp2","name":"Allow Return Traffic","action":"ALLOW","predefined":true,"index":30000}]`).
		Seed("v2", "firewall/zone", `[
			{"_id":"z1","name":"Internal","zone_key":"internal","default_zone":true,"network_ids":["n1"],"external_id":"iz-internal"},
			{"_id":"z2","name":"IoT","default_zone":false,"network_ids":["n2"]}]`).
		Seed("v2", "trafficrules", `[{"_id":"t1","description":"Block games","enabled":true,"action":"BLOCK","matching_target":"APP","app_ids":[1],"target_devices":[{"type":"ALL_CLIENTS"}],"bandwidth_limit":{"enabled":false,"download_limit_kbps":1024,"upload_limit_kbps":1024}}]`).
		Seed("v2", "trafficroutes", `[{"_id":"tr1","description":"Netflix via VPN","enabled":true,"matching_target":"DOMAIN","network_id":"vpn1","domains":[{"domain":"netflix.com","ports":[],"port_ranges":[]}],"target_devices":[{"type":"ALL_CLIENTS"}],"kill_switch_enabled":false}]`)
	env.mock.ClearRequests()
	return env
}

func TestFirewallGating(t *testing.T) {
	env := firewallEnv(t)
	ro := toolMap(t, connect(t, env.client, toolOptions(false, false)))
	if ro["unifi_list_traffic_routes"] == nil || ro["unifi_get_firewall_policy_ordering"] == nil || ro["unifi_save_firewall_rule"] != nil {
		t.Error("read-only firewall tools")
	}
	rw := toolMap(t, connect(t, env.client, toolOptions(true, false)))
	if rw["unifi_save_firewall_rule"] == nil || rw["unifi_save_traffic_route"] == nil {
		t.Error("write firewall tools missing")
	}
	for _, n := range []string{"unifi_delete_firewall_rule", "unifi_delete_port_forward", "unifi_delete_firewall_policy", "unifi_delete_route", "unifi_delete_traffic_route"} {
		if rw[n] != nil {
			t.Errorf("%s should require deletes", n)
		}
	}
	all := toolMap(t, env.mcp)
	if !*all["unifi_delete_traffic_rule"].Annotations.DestructiveHint || !*all["unifi_save_port_forward"].Annotations.DestructiveHint {
		t.Error("destructive hints")
	}
}

func TestCreateFirewallRule(t *testing.T) {
	env := firewallEnv(t)
	res := env.call("unifi_save_firewall_rule", `{"name":"Block IoT","ruleset":"LAN_IN","action":"drop","src_network_id":"n2","dst_group_ids":["g1"],"states":["new"]}`)
	body := firstTo(env.mock, "/api/s/default/rest/firewallrule", "POST").Body
	for k, want := range map[string]any{"rule_index": 2004.0, "protocol": "all", "enabled": true, "src_networkconf_id": "n2", "src_networkconf_type": "NETv4", "state_new": true, "state_established": false} {
		eq(t, k, get(body, k), want)
	}
	eq(t, "dst groups", get(body, "dst_firewallgroup_ids"), js(`["g1"]`))
	eq(t, "index", get(res, "index"), 2004.0)
	eq(t, "name", get(res, "name"), "Block IoT")
	// Defaults come first, as the TypeScript server sent them.
	if o, _ := ojson.AsObj(body); o.Keys()[0] != "enabled" || o.Keys()[len(o.Keys())-1] != "rule_index" {
		t.Errorf("body key order: %v", o.Keys())
	}
}

func TestRequiredFieldsOnCreate(t *testing.T) {
	env := firewallEnv(t)
	contains(t, "rule", env.callErr("unifi_save_firewall_rule", `{"name":"x"}`), "ruleset and action are required")
	contains(t, "forward", env.callErr("unifi_save_port_forward", `{"name":"x"}`), "external_port and forward_ip are required")
}

func TestUpdateAndDeleteFirewallRule(t *testing.T) {
	env := firewallEnv(t)
	env.call("unifi_save_firewall_rule", `{"id":"r1","enabled":false}`)
	put := firstTo(env.mock, "/api/s/default/rest/firewallrule/r1", "PUT").Body
	eq(t, "enabled", get(put, "enabled"), false)
	eq(t, "name", get(put, "name"), "Existing")
	eq(t, "action", get(put, "action"), "drop")
	eq(t, "stored", ojson.Get(env.mock.Store("rest/firewallrule")[0], "enabled"), false)

	eq(t, "delete", env.call("unifi_delete_firewall_rule", `{"id":"r2"}`), js(`{"deleted":"r2"}`))
	if n := len(env.mock.Store("rest/firewallrule")); n != 1 {
		t.Errorf("rules left: %d", n)
	}
}

func TestFirewallGroups(t *testing.T) {
	env := firewallEnv(t)
	g := env.call("unifi_save_firewall_group", `{"name":"Web ports","type":"port-group","members":["80","443"]}`)
	eq(t, "members", get(g, "members"), js(`["80","443"]`))
	eq(t, "type", get(g, "type"), "port-group")
	upd := env.call("unifi_save_firewall_group", `{"id":"g1","members":["10.0.0.5","10.0.0.6"]}`)
	eq(t, "name", get(upd, "name"), "Servers")
	eq(t, "members", get(upd, "members"), js(`["10.0.0.5","10.0.0.6"]`))
	contains(t, "type change", env.callErr("unifi_save_firewall_group", `{"id":"g1","type":"port-group"}`), "cannot be changed")
}

func TestPortForwards(t *testing.T) {
	env := firewallEnv(t)
	pf := env.call("unifi_save_port_forward", `{"name":"SSH","external_port":"2222","forward_ip":"10.0.0.7","protocol":"tcp"}`)
	body := firstTo(env.mock, "/api/s/default/rest/portforward", "POST").Body
	for k, want := range map[string]any{"pfwd_interface": "wan", "proto": "tcp", "src": "any", "fwd_port": "2222", "enabled": true, "log": false} {
		eq(t, k, get(body, k), want)
	}
	eq(t, "forward ip", get(pf, "forward_ip"), "10.0.0.7")
	upd := env.call("unifi_save_port_forward", `{"id":"p1","enabled":false}`)
	eq(t, "enabled", get(upd, "enabled"), false)
	eq(t, "port", get(upd, "external_port"), "443")
	env.call("unifi_delete_port_forward", `{"id":"p1"}`)
	for _, p := range env.mock.Store("rest/portforward") {
		if ojson.Get(p, "_id") == "p1" {
			t.Error("p1 not deleted")
		}
	}
	list := env.call("unifi_list_port_forwards", nil)
	if n, _ := ojson.Len(list); n != 1 {
		t.Errorf("forwards: %d", n)
	}
}

func TestFirewallPolicies(t *testing.T) {
	env := firewallEnv(t)
	list := env.call("unifi_list_firewall_policies", nil)
	eq(t, "ids", ojson.Map(list.([]any), func(p any) any { return ojson.Get(p, "id") }), js(`["fp1"]`))
	eq(t, "zone", get(list, 0, "source", "zone_id"), "z1")
	all := env.call("unifi_list_firewall_policies", `{"include_predefined":true}`)
	if n, _ := ojson.Len(all); n != 2 {
		t.Errorf("all policies: %d", n)
	}

	env.call("unifi_save_firewall_policy", `{"name":"Block IoT DNS","action":"BLOCK","protocol":"tcp_udp","source":{"zone_id":"z2","network_ids":["n2"]},"destination":{"zone_id":"z1","ips":["10.0.0.53"],"port":"53"}}`)
	body := firstTo(env.mock, "/v2/api/site/default/firewall-policies", "POST").Body
	for path, want := range map[string]any{"enabled": true, "ip_version": "BOTH", "connection_state_type": "ALL", "create_allow_respond": false} {
		eq(t, path, get(body, path), want)
	}
	eq(t, "schedule", get(body, "schedule"), js(`{"mode":"ALWAYS"}`))
	eq(t, "src target", get(body, "source", "matching_target"), "NETWORK")
	eq(t, "src type", get(body, "source", "matching_target_type"), "OBJECT")
	eq(t, "dst target", get(body, "destination", "matching_target"), "IP")
	eq(t, "dst type", get(body, "destination", "matching_target_type"), "SPECIFIC")
	eq(t, "dst port type", get(body, "destination", "port_matching_type"), "SPECIFIC")

	contains(t, "zone", env.callErr("unifi_save_firewall_policy", `{"name":"x","action":"ALLOW","source":{"matching_target":"ANY"},"destination":{"zone_id":"z1"}}`), "source.zone_id is required")
	contains(t, "respond", env.callErr("unifi_save_firewall_policy", `{"name":"x","action":"BLOCK","create_allow_respond":true,"source":{"zone_id":"z1"},"destination":{"zone_id":"z1"}}`), "create_allow_respond can only be used with ALLOW policies")
}

func TestFirewallPolicyUpdate(t *testing.T) {
	env := firewallEnv(t)
	res := env.call("unifi_save_firewall_policy", `{"id":"fp1","destination":{"port":"443"},"logging":true}`)
	put := firstTo(env.mock, "/v2/api/site/default/firewall-policies/fp1", "PUT").Body
	eq(t, "zone kept", get(put, "destination", "zone_id"), "z2")
	eq(t, "port", get(put, "destination", "port"), "443")
	eq(t, "logging", get(put, "logging"), true)
	eq(t, "name", get(put, "name"), "Block IoT to LAN")
	eq(t, "summary logging", get(res, "logging"), true)

	contains(t, "predefined save", env.callErr("unifi_save_firewall_policy", `{"id":"fp2","enabled":false}`), "Predefined")
	contains(t, "predefined delete", env.callErr("unifi_delete_firewall_policy", `{"id":"fp2"}`), "Predefined")
	eq(t, "deleted", get(env.call("unifi_delete_firewall_policy", `{"id":"fp1"}`), "deleted"), "fp1")
}

func TestZonesAndOrderingThroughTheIntegrationAPI(t *testing.T) {
	env := firewallEnv(t)
	izones := js(`[{"id":"iz-internal","name":"Internal","networkIds":["n1"],"metadata":{"origin":"SYSTEM_DEFINED"}},{"id":"iz-iot","name":"IoT","networkIds":["n2"],"metadata":{"origin":"USER_DEFINED"}}]`)
	var seen []any
	env.mock.
		On("GET", `^/integration/v1/sites$`, func(any, []string, url.Values) (int, any) {
			return 200, ojson.New("data", []any{ojson.New("id", siteUUID, "internalReference", "default", "name", "Default")}, "totalCount", 1)
		}).
		On("GET", `^/integration/v1/sites/[^/]+/firewall/zones$`, func(any, []string, url.Values) (int, any) {
			return 200, ojson.New("data", izones, "totalCount", 2)
		}).
		On("POST", `^/integration/v1/sites/[^/]+/firewall/zones$`, func(body any, _ []string, _ url.Values) (int, any) {
			return 201, ojson.New("id", "iz-new").Assign(body)
		}).
		On("PUT", `^/integration/v1/sites/[^/]+/firewall/zones/([^/]+)$`, func(body any, m []string, _ url.Values) (int, any) {
			seen = append(seen, ojson.New("id", m[1], "body", body))
			return 200, ojson.New("id", m[1]).Assign(body)
		}).
		On("DELETE", `^/integration/v1/sites/[^/]+/firewall/zones/([^/]+)$`, func(_ any, m []string, _ url.Values) (int, any) {
			seen = append(seen, ojson.New("deleted", m[1]))
			return 200, ojson.New()
		}).
		On("GET", `^/integration/v1/sites/[^/]+/firewall/policies/ordering$`, func(_ any, _ []string, q url.Values) (int, any) {
			seen = append(seen, ojson.New("query", ojson.New("sourceFirewallZoneId", q.Get("sourceFirewallZoneId"), "destinationFirewallZoneId", q.Get("destinationFirewallZoneId"))))
			return 200, js(`{"orderedFirewallPolicyIds":{"beforeSystemDefined":["a","b"],"afterSystemDefined":[]}}`)
		}).
		On("PUT", `^/integration/v1/sites/[^/]+/firewall/policies/ordering$`, func(body any, _ []string, _ url.Values) (int, any) { return 200, body })

	last := func() any { return seen[len(seen)-1] }
	eq(t, "created", env.call("unifi_save_firewall_zone", `{"name":"Cameras","network_ids":["n3"]}`), js(`{"id":"iz-new","name":"Cameras","networkIds":["n3"]}`))

	// v2 id z2 maps to the integration zone by name; network_ids kept when not passed.
	env.call("unifi_save_firewall_zone", `{"id":"z2","name":"Smart home"}`)
	eq(t, "renamed", last(), js(`{"id":"iz-iot","body":{"name":"Smart home","networkIds":["n2"]}}`))

	contains(t, "rename system", env.callErr("unifi_save_firewall_zone", `{"id":"z1","name":"Renamed"}`), "system-defined")
	contains(t, "delete system", env.callErr("unifi_delete_firewall_zone", `{"id":"z1"}`), "system-defined")
	env.call("unifi_delete_firewall_zone", `{"id":"iz-iot"}`)
	eq(t, "deleted", last(), js(`{"deleted":"iz-iot"}`))

	order := env.call("unifi_get_firewall_policy_ordering", `{"source_zone_id":"z1","destination_zone_id":"z2"}`)
	eq(t, "order", get(order, "orderedFirewallPolicyIds", "beforeSystemDefined"), js(`["a","b"]`))
	eq(t, "query", last(), js(`{"query":{"sourceFirewallZoneId":"iz-internal","destinationFirewallZoneId":"iz-iot"}}`))

	put := env.call("unifi_set_firewall_policy_ordering", `{"source_zone_id":"z1","destination_zone_id":"z2","before_system_defined":["b","a"]}`)
	eq(t, "put", put, js(`{"orderedFirewallPolicyIds":{"beforeSystemDefined":["b","a"],"afterSystemDefined":[]}}`))
	contains(t, "wrong ids", env.callErr("unifi_set_firewall_policy_ordering", `{"source_zone_id":"z1","destination_zone_id":"z2","before_system_defined":["a"]}`), "exactly the current policy IDs: a, b")
	if r := env.mock.RequestsTo("/integration/v1/sites"); r[0].Header.Get("X-API-KEY") != "key123" || r[0].Path != "/proxy/network/integration/v1/sites?limit=200" {
		t.Errorf("integration request: %s %v", r[0].Path, r[0].Header)
	}
	// A site that isn't found is an error, even when the controller has only one site.
	if _, err := integrationSiteID(context.Background(), env.client, "branch"); err == nil || !strings.Contains(err.Error(), `Could not find site "branch"`) {
		t.Errorf("unknown site: %v", err)
	}
}

func TestTrafficRules(t *testing.T) {
	env := firewallEnv(t)
	res := env.call("unifi_save_traffic_rule", `{"description":"No social at night","action":"BLOCK","matching_target":"DOMAIN","domains":["facebook.com"],"client_macs":["AA-BB-CC-DD-EE-FF"],"schedule":{"mode":"EVERY_DAY","time_range_start":"22:00","time_range_end":"06:00"}}`)
	body := firstTo(env.mock, "/v2/api/site/default/trafficrules", "POST").Body
	eq(t, "domains", get(body, "domains"), js(`[{"domain":"facebook.com","ports":[],"port_ranges":[]}]`))
	eq(t, "targets", get(body, "target_devices"), js(`[{"type":"CLIENT","client_mac":"aa:bb:cc:dd:ee:ff"}]`))
	eq(t, "app ids", get(body, "app_ids"), js(`[]`))
	eq(t, "bw", get(body, "bandwidth_limit", "enabled"), false)
	eq(t, "enabled", get(body, "enabled"), true)
	eq(t, "schedule passthrough", get(body, "schedule"), js(`{"mode":"EVERY_DAY","time_range_start":"22:00","time_range_end":"06:00"}`))
	eq(t, "res domains", get(res, "domains"), js(`["facebook.com"]`))
	eq(t, "res targets", get(res, "targets"), js(`["client:aa:bb:cc:dd:ee:ff"]`))

	upd := env.call("unifi_save_traffic_rule", `{"id":"t1","download_limit_kbps":5000}`)
	put := firstTo(env.mock, "/v2/api/site/default/trafficrules/t1", "PUT").Body
	eq(t, "bw merge", get(put, "bandwidth_limit"), js(`{"enabled":true,"download_limit_kbps":5000,"upload_limit_kbps":1024}`))
	eq(t, "apps kept", get(put, "app_ids"), js(`[1]`))
	eq(t, "summary bw", get(upd, "bandwidth_limit"), js(`{"down_kbps":5000,"up_kbps":1024}`))
	env.call("unifi_delete_traffic_rule", `{"id":"t1"}`)
	for _, r := range env.mock.Store("v2/trafficrules") {
		if ojson.Get(r, "_id") == "t1" {
			t.Error("t1 not deleted")
		}
	}
	contains(t, "create", env.callErr("unifi_save_traffic_rule", `{"description":"x"}`), "action and matching_target are required")
}

func TestTrafficRoutes(t *testing.T) {
	env := firewallEnv(t)
	list := env.call("unifi_list_traffic_routes", nil)
	eq(t, "domains", get(list, 0, "domains"), js(`["netflix.com"]`))

	env.call("unifi_save_traffic_route", `{"description":"Work VPN","matching_target":"IP","network_id":"vpn2","ip_addresses":["192.168.50.0/24"],"network_ids":["n2"],"kill_switch":true}`)
	body := firstTo(env.mock, "/v2/api/site/default/trafficroutes", "POST").Body
	eq(t, "ips", get(body, "ip_addresses"), js(`[{"ip_or_subnet":"192.168.50.0/24","ip_version":"v4","ports":[],"port_ranges":[]}]`))
	eq(t, "targets", get(body, "target_devices"), js(`[{"type":"NETWORK","network_id":"n2"}]`))
	eq(t, "kill switch", get(body, "kill_switch_enabled"), true)
	eq(t, "next hop", get(body, "next_hop"), "")

	contains(t, "internet", env.callErr("unifi_save_traffic_route", `{"description":"All","matching_target":"INTERNET","network_id":"vpn2"}`), "explicit client_macs or network_ids")

	upd := env.call("unifi_save_traffic_route", `{"id":"tr1","enabled":false}`)
	eq(t, "enabled", get(upd, "enabled"), false)
	eq(t, "network", get(upd, "network_id"), "vpn1")
	env.call("unifi_delete_traffic_route", `{"id":"tr1"}`)
	for _, r := range env.mock.Store("v2/trafficroutes") {
		if ojson.Get(r, "_id") == "tr1" {
			t.Error("tr1 not deleted")
		}
	}
}

func TestStaticRoutes(t *testing.T) {
	env := firewallEnv(t)
	list := env.call("unifi_list_routes", nil)
	eq(t, "summary", get(list, 0), js(`{"id":"s1","name":"Lab","enabled":true,"type":"nexthop","destination":"10.9.0.0/16","next_hop":"10.0.0.254","distance":1}`))

	created := env.call("unifi_save_route", `{"name":"Null","type":"blackhole","destination":"10.66.0.0/16"}`)
	body := firstTo(env.mock, "/api/s/default/rest/routing", "POST").Body
	want := `{"type":"static-route","enabled":true,"gateway_type":"default","static-route_type":"blackhole","static-route_distance":1,"name":"Null","static-route_network":"10.66.0.0/16"}`
	if s, _ := ojson.Stringify(body); s != want {
		t.Errorf("route body (with key order):\n got  %s\n want %s", s, want)
	}
	eq(t, "type", get(created, "type"), "blackhole")
	contains(t, "next hop", env.callErr("unifi_save_route", `{"name":"x","destination":"10.1.0.0/16"}`), "next_hop is required")

	upd := env.call("unifi_save_route", `{"id":"s1","distance":5}`)
	eq(t, "distance", get(upd, "distance"), 5.0)
	eq(t, "next hop", get(upd, "next_hop"), "10.0.0.254")
	env.call("unifi_delete_route", `{"id":"s1"}`)
	for _, r := range env.mock.Store("rest/routing") {
		if ojson.Get(r, "_id") == "s1" {
			t.Error("s1 not deleted")
		}
	}
}

func TestActiveRoutingTable(t *testing.T) {
	env := firewallEnv(t)
	env.mock.On("GET", `^/api/s/default/stat/routing$`, func(any, []string, url.Values) (int, any) {
		return 200, js(`{"meta":{"rc":"ok"},"data":[{"pfx":"0.0.0.0/0","nh":[{"intf":"eth8"}]}]}`)
	})
	res := env.call("unifi_list_routes", `{"active":true}`)
	eq(t, "prefix", get(res, 0, "pfx"), "0.0.0.0/0")
}

func TestFirewallRuleOrdering(t *testing.T) {
	env := firewallEnv(t)
	env.mock.Seed("rest", "firewallrule", `[
		{"_id":"a","ruleset":"WAN_IN","rule_index":2001},
		{"_id":"b","ruleset":"LANv6_IN","rule_index":2000},
		{"_id":"c","ruleset":"LAN_IN","rule_index":2005},
		{"_id":"d","ruleset":"LAN_IN","rule_index":2001}]`)
	res := env.call("unifi_list_firewall_rules", nil)
	eq(t, "order", ojson.Map(res.([]any), func(r any) any { return ojson.Get(r, "id") }), js(`["d","c","b","a"]`))
	eq(t, "states", get(res, 0, "states"), js(`[]`))
}
