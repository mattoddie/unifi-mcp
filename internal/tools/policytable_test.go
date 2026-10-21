package tools

import (
	"net/url"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

func policyEnv(t *testing.T) *testEnv {
	env := startMock(t, toolOptions(true, true))
	env.mock.
		Seed("v2", "qos-rules", `[{"_id":"q1","name":"Limit kids","enabled":true,"index":1,"objective":"LIMIT","download_limit_kbps":5000,"upload_limit_kbps":1000,"download_burst":"OFF","upload_burst":"OFF",
			"source":{"matching_target":"CLIENT","client_macs":["aa:bb:cc:00:00:10"],"network_ids":[],"port_matching_type":"ANY"},
			"destination":{"matching_target":"APP","matching_target_type":"SPECIFIC","app_ids":[5],"app_category_ids":[],"ips":[],"regions":[],"web_domains":[],"port_matching_type":"ANY"},
			"schedule":{"mode":"ALWAYS"}}]`).
		Seed("v2", "nat", `[
			{"_id":"nat1","description":"Hairpin","enabled":true,"type":"DNAT","rule_index":3,"ip_version":"IPV4","protocol":"tcp","in_interface":"wan1","ip_address":"10.0.0.5","port":"443",
			 "source_filter":{"filter_type":"NONE"},"destination_filter":{"filter_type":"ADDRESS_AND_PORT","address":"203.0.113.10","port":"443","invert_address":false,"invert_port":false},"is_predefined":false,"setting_preference":"manual"},
			{"_id":"nat0","description":"Default masquerade","enabled":true,"type":"MASQUERADE","rule_index":99,"out_interface":"wan1","is_predefined":true}]`).
		Seed("v2", "acl-rules", `[
			{"_id":"acl1","name":"Cameras isolated","enabled":true,"type":"MAC","action":"BLOCK","acl_index":2,"mac_acl_network_id":"n2",
			 "traffic_source":{"type":"CLIENT_MAC","specific_mac_addresses":["aa:bb:cc:00:00:20"],"mac_mask":48,"ips_or_subnets":[],"network_ids":[],"ports":[]},
			 "traffic_destination":{"type":"CLIENT_MAC","specific_mac_addresses":[],"ips_or_subnets":[],"network_ids":[],"ports":[]}},
			{"_id":"acl2","name":"Other net","enabled":true,"type":"MAC","action":"ALLOW","acl_index":1,"mac_acl_network_id":"n1"}]`).
		Seed("v2", oonList, `[{"_id":"o1","name":"Bedtime","enabled":true,"target_type":"CLIENTS","targets":[{"type":"MAC","value":"aa:bb:cc:00:00:30"}],
			"secure":{"enabled":true,"internet":{"mode":"TURN_OFF_INTERNET","schedule":{"mode":"EVERY_DAY","time_range_start":"21:00","time_range_end":"07:00"}}},
			"qos":{"enabled":false},"route":{"enabled":false}}]`)
	env.mock.
		On("POST", `^/v2/api/site/default/`+oonSingle+`$`, func(body any, _ []string, _ url.Values) (int, any) {
			return 201, ojson.Spread(body).Set("_id", "o2")
		}).
		On("PUT", `^/v2/api/site/default/`+oonSingle+`/([^/]+)$`, func(body any, m []string, _ url.Values) (int, any) { return 200, body }).
		On("DELETE", `^/v2/api/site/default/`+oonSingle+`/([^/]+)$`, fixed(204, nil))
	env.mock.ClearRequests()
	return env
}

func TestPolicyTableGating(t *testing.T) {
	env := policyEnv(t)
	ro := toolMap(t, connect(t, env.client, toolOptions(false, false)))
	for _, n := range []string{"unifi_list_qos_rules", "unifi_list_nat_rules", "unifi_list_acl_rules", "unifi_list_oon_policies", "unifi_list_traffic_flows"} {
		if ro[n] == nil || !ro[n].Annotations.ReadOnlyHint {
			t.Errorf("%s should be a read tool", n)
		}
	}
	rw := toolMap(t, connect(t, env.client, toolOptions(true, false)))
	for _, n := range []string{"qos_rule", "nat_rule", "acl_rule", "oon_policy"} {
		if ro["unifi_save_"+n] != nil || rw["unifi_save_"+n] == nil || !*rw["unifi_save_"+n].Annotations.DestructiveHint {
			t.Errorf("unifi_save_%s gating", n)
		}
		if rw["unifi_delete_"+n] != nil {
			t.Errorf("unifi_delete_%s should require deletes", n)
		}
	}
}

func TestQosRules(t *testing.T) {
	env := policyEnv(t)
	list := env.call("unifi_list_qos_rules", nil)
	eq(t, "summary", get(list, "items", 0), js(`{"id":"q1","name":"Limit kids","enabled":true,"index":1,"objective":"LIMIT","download_limit_kbps":5000,"upload_limit_kbps":1000,
		"source":{"matching_target":"CLIENT","client_macs":["aa:bb:cc:00:00:10"]},"destination":{"matching_target":"APP","app_ids":[5]}}`))

	contains(t, "required", env.callErr("unifi_save_qos_rule", `{"name":"x"}`), "name and objective are required")
	contains(t, "limit", env.callErr("unifi_save_qos_rule", `{"name":"x","objective":"LIMIT"}`), "needs download_limit_kbps")
	contains(t, "one destination", env.callErr("unifi_save_qos_rule", `{"name":"x","objective":"PRIORITIZE","ips":["1.1.1.1"],"regions":["US"]}`), "only one of")

	env.call("unifi_save_qos_rule", `{"name":"Prioritise calls","objective":"PRIORITIZE","network_ids":["n1"],"app_category_ids":[13]}`)
	body := firstTo(env.mock, "/v2/api/site/default/qos-rules", "POST").Body
	eq(t, "create body", body, js(`{"enabled":true,"download_burst":"OFF","upload_burst":"OFF",
		"source":{"matching_target":"NETWORK","client_macs":[],"network_ids":["n1"],"port_matching_type":"ANY"},
		"destination":{"matching_target":"APP_CATEGORY","matching_target_type":"SPECIFIC","app_ids":[],"app_category_ids":[13],"ips":[],"regions":[],"web_domains":[],"port_matching_type":"ANY"},
		"schedule":{"mode":"ALWAYS","repeat_on_days":[],"time_all_day":false},
		"name":"Prioritise calls","objective":"PRIORITIZE"}`))

	// An update merges into the full rule and PUTs it back; nested source fields are kept.
	env.call("unifi_save_qos_rule", `{"id":"q1","download_limit_kbps":8000,"destination_port":"443"}`)
	put := firstTo(env.mock, "/v2/api/site/default/qos-rules/q1", "PUT").Body
	eq(t, "limit", get(put, "download_limit_kbps"), 8000.0)
	eq(t, "upload kept", get(put, "upload_limit_kbps"), 1000.0)
	eq(t, "source kept", get(put, "source", "client_macs"), js(`["aa:bb:cc:00:00:10"]`))
	eq(t, "destination merged", get(put, "destination"), js(`{"matching_target":"APP","matching_target_type":"SPECIFIC","app_ids":[5],"app_category_ids":[],"ips":[],"regions":[],"web_domains":[],"port_matching_type":"SPECIFIC","port":"443"}`))
	contains(t, "nothing", env.callErr("unifi_save_qos_rule", `{"id":"q1"}`), "Nothing to update")

	env.call("unifi_delete_qos_rule", `{"id":"q1"}`)
	eq(t, "delete", lastTo(env.mock, "/v2/api/site/default/qos-rules/q1").Method, "DELETE")
}

func TestNatRules(t *testing.T) {
	env := policyEnv(t)
	list := env.call("unifi_list_nat_rules", `{"search":"hairpin"}`)
	eq(t, "summary", get(list, "items"), js(`[{"id":"nat1","description":"Hairpin","enabled":true,"type":"DNAT","rule_index":3,"ip_version":"IPV4","protocol":"tcp","in_interface":"wan1",
		"translated_ip":"10.0.0.5","translated_port":"443","destination":{"type":"ADDRESS_AND_PORT","address":"203.0.113.10","port":"443"}}]`))

	contains(t, "type", env.callErr("unifi_save_nat_rule", `{"description":"x"}`), "type is required")
	contains(t, "dnat", env.callErr("unifi_save_nat_rule", `{"type":"DNAT","in_interface":"wan1","translated_ip":"10.0.0.6"}`), "needs a destination")
	contains(t, "masq", env.callErr("unifi_save_nat_rule", `{"type":"MASQUERADE","out_interface":"wan1","translated_ip":"1.2.3.4"}`), "must be empty")

	env.call("unifi_save_nat_rule", `{"type":"SNAT","description":"Lab SNAT","out_interface":"wan1","translated_ip":"203.0.113.20","source_address":"10.9.0.0/24"}`)
	body := firstTo(env.mock, "/v2/api/site/default/nat", "POST").Body
	eq(t, "create body", body, js(`{"description":"Lab SNAT","enabled":true,"ip_version":"IPV4","protocol":"all","logging":false,"exclude":false,"pppoe_use_base_interface":false,
		"is_predefined":false,"setting_preference":"manual",
		"source_filter":{"filter_type":"ADDRESS_AND_PORT","firewall_group_ids":[],"invert_address":false,"invert_port":false,"address":"10.9.0.0/24"},
		"destination_filter":{"filter_type":"NONE","firewall_group_ids":[],"invert_address":false,"invert_port":false},
		"type":"SNAT","out_interface":"wan1","ip_address":"203.0.113.20","rule_index":4}`))

	env.call("unifi_save_nat_rule", `{"id":"nat1","translated_port":"8443","enabled":false}`)
	put := firstTo(env.mock, "/v2/api/site/default/nat/nat1", "PUT").Body
	eq(t, "port", get(put, "port"), "8443")
	eq(t, "enabled", get(put, "enabled"), false)
	eq(t, "destination kept", get(put, "destination_filter", "address"), "203.0.113.10")

	contains(t, "predefined update", env.callErr("unifi_save_nat_rule", `{"id":"nat0","enabled":false}`), "predefined")
	contains(t, "predefined delete", env.callErr("unifi_delete_nat_rule", `{"id":"nat0"}`), "predefined")
	env.call("unifi_delete_nat_rule", `{"id":"nat1"}`)
	eq(t, "delete", lastTo(env.mock, "/v2/api/site/default/nat/nat1").Method, "DELETE")
}

func TestAclRules(t *testing.T) {
	env := policyEnv(t)
	list := env.call("unifi_list_acl_rules", `{"network_id":"n2"}`)
	eq(t, "summary", get(list, "items"), js(`[{"id":"acl1","name":"Cameras isolated","enabled":true,"type":"MAC","action":"BLOCK","acl_index":2,"network_id":"n2",
		"source":{"macs":["aa:bb:cc:00:00:20"],"mac_mask":48},"destination":{}}]`))
	all := env.call("unifi_list_acl_rules", nil)
	eq(t, "ordered by index", get(all, "items", 0, "id"), "acl2")

	contains(t, "required", env.callErr("unifi_save_acl_rule", `{"name":"x","action":"BLOCK"}`), "network_id are required")
	env.call("unifi_save_acl_rule", `{"name":"Block printer","action":"BLOCK","network_id":"n2","source_macs":["AA-BB-CC-00-00-21"],"destination_macs":["aa:bb:cc:00:00:22"]}`)
	body := firstTo(env.mock, "/v2/api/site/default/acl-rules", "POST").Body
	eq(t, "create body", body, js(`{"type":"MAC","enabled":true,"specific_enforcers":[],
		"traffic_source":{"type":"CLIENT_MAC","specific_mac_addresses":["aa:bb:cc:00:00:21"],"ips_or_subnets":[],"network_ids":[],"ports":[]},
		"traffic_destination":{"type":"CLIENT_MAC","specific_mac_addresses":["aa:bb:cc:00:00:22"],"ips_or_subnets":[],"network_ids":[],"ports":[]},
		"name":"Block printer","action":"BLOCK","mac_acl_network_id":"n2","acl_index":3}`))

	env.call("unifi_save_acl_rule", `{"id":"acl1","destination_macs":["aa:bb:cc:00:00:23"]}`)
	put := firstTo(env.mock, "/v2/api/site/default/acl-rules/acl1", "PUT").Body
	eq(t, "source kept", get(put, "traffic_source"), js(`{"type":"CLIENT_MAC","specific_mac_addresses":["aa:bb:cc:00:00:20"],"mac_mask":48,"ips_or_subnets":[],"network_ids":[],"ports":[]}`))
	eq(t, "destination", get(put, "traffic_destination", "specific_mac_addresses"), js(`["aa:bb:cc:00:00:23"]`))
	eq(t, "name kept", get(put, "name"), "Cameras isolated")

	env.call("unifi_delete_acl_rule", `{"id":"acl1"}`)
	eq(t, "delete", lastTo(env.mock, "/v2/api/site/default/acl-rules/acl1").Method, "DELETE")
}

func TestAclCreateReadsBackEmptyResponse(t *testing.T) {
	env := policyEnv(t)
	env.mock.On("POST", `^/v2/api/site/default/acl-rules$`, fixed(200, ojson.New()))
	env.mock.Seed("v2", "acl-rules", `[{"_id":"acl9","name":"Block printer","action":"BLOCK","acl_index":1,"mac_acl_network_id":"n2"}]`)
	res := env.call("unifi_save_acl_rule", `{"name":"Block printer","action":"BLOCK","network_id":"n2"}`)
	eq(t, "id", get(res, "id"), "acl9")
}

func TestOonPolicies(t *testing.T) {
	env := policyEnv(t)
	list := env.call("unifi_list_oon_policies", nil)
	eq(t, "summary", get(list, "items", 0), js(`{"id":"o1","name":"Bedtime","enabled":true,"target_type":"CLIENTS","targets":["aa:bb:cc:00:00:30"],
		"secure":{"enabled":true,"internet":{"mode":"TURN_OFF_INTERNET","schedule":{"mode":"EVERY_DAY","time_range_start":"21:00","time_range_end":"07:00"}}}}`))

	contains(t, "required", env.callErr("unifi_save_oon_policy", `{"name":"x","target_type":"GROUPS","targets":["g1"]}`), "at least one of secure, qos or route")
	env.call("unifi_save_oon_policy", `{"name":"Kids VPN","target_type":"GROUPS","targets":["grp1"],"route":{"enabled":true,"vpn_id":"v1"}}`)
	eq(t, "create", firstTo(env.mock, "/v2/api/site/default/"+oonSingle, "POST").Body,
		js(`{"enabled":true,"name":"Kids VPN","target_type":"GROUPS","targets":[{"type":"NETWORK_GROUP_ID","value":"grp1"}],"route":{"enabled":true,"vpn_id":"v1"}}`))

	// The update reads the plural list, merges secure one level deep and PUTs the singular path.
	env.call("unifi_save_oon_policy", `{"id":"o1","targets":["AA:BB:CC:00:00:31"],"secure":{"enabled":false}}`)
	put := firstTo(env.mock, "/v2/api/site/default/"+oonSingle+"/o1", "PUT").Body
	eq(t, "targets", get(put, "targets"), js(`[{"type":"MAC","value":"aa:bb:cc:00:00:31"}]`))
	eq(t, "secure", get(put, "secure"), js(`{"enabled":false,"internet":{"mode":"TURN_OFF_INTERNET","schedule":{"mode":"EVERY_DAY","time_range_start":"21:00","time_range_end":"07:00"}}}`))
	eq(t, "qos kept", get(put, "qos"), js(`{"enabled":false}`))
	contains(t, "target type", env.callErr("unifi_save_oon_policy", `{"id":"o1","target_type":"GROUPS"}`), "Pass targets")

	env.call("unifi_delete_oon_policy", `{"id":"o1"}`)
	eq(t, "delete", lastTo(env.mock, "/v2/api/site/default/"+oonSingle+"/o1").Method, "DELETE")
}

func TestTrafficFlows(t *testing.T) {
	old := nowMs
	nowMs = func() float64 { return 1700000000000 }
	t.Cleanup(func() { nowMs = old })
	env := policyEnv(t)
	env.mock.On("POST", `^/v2/api/site/default/traffic-flows$`, fixed(200, js(`{"data":[
		{"id":"f1","action":"blocked","service":"YouTube","protocol":"TCP","direction":"outgoing","risk":"low","count":3,"duration_milliseconds":1200,"time":1699999990000,
		 "source":{"client_name":"Kid tablet","mac":"aa:bb:cc:00:00:40","ip":"10.0.0.40","network_name":"Default","zone_name":"Internal"},
		 "destination":{"ip":"198.51.100.7","domains":["youtube.com"],"zone_name":"External"},
		 "traffic_data":{"bytes_rx":100,"bytes_tx":50,"bytes_total":150},"policies":[{"name":"No YouTube","type":"TRAFFIC_RULE","id":"t1"}]}],
		"total_element_count":7,"has_next":true}`)))
	res := env.call("unifi_list_traffic_flows", `{"client_mac":"AA:BB:CC:00:00:40","action":"blocked","limit":1}`)
	body := firstTo(env.mock, "/v2/api/site/default/traffic-flows", "POST").Body
	eq(t, "window", get(body, "timestampFrom"), 1700000000000.0-15*60000)
	eq(t, "to", get(body, "timestampTo"), 1700000000000.0)
	eq(t, "page size", get(body, "pageSize"), 1.0)
	eq(t, "mac filter", get(body, "source_mac"), js(`["aa:bb:cc:00:00:40"]`))
	eq(t, "action filter", get(body, "action"), js(`["blocked"]`))
	eq(t, "unused filters sent empty", get(body, "destination_port"), js(`[]`))
	eq(t, "total", get(res, "total"), 7.0)
	eq(t, "has more", get(res, "has_more"), true)
	eq(t, "flow", get(res, "items", 0), js(`{"time":"2023-11-14T22:13:10.000Z","action":"blocked","service":"YouTube","protocol":"TCP","direction":"outgoing","risk":"low",
		"source":{"name":"Kid tablet","mac":"aa:bb:cc:00:00:40","ip":"10.0.0.40","network":"Default","zone":"Internal"},
		"destination":{"ip":"198.51.100.7","zone":"External","domains":["youtube.com"]},
		"bytes_rx":100,"bytes_tx":50,"bytes_total":150,"count":3,"duration_ms":1200,"policies":[{"name":"No YouTube","type":"TRAFFIC_RULE","id":"t1"}]}`))

	// The controller applies search, so it isn't applied again to the summaries; fields still is.
	res = env.call("unifi_list_traffic_flows", `{"search":"kid-tablet.lan","fields":["service","source.name"]}`)
	eq(t, "search sent", get(lastTo(env.mock, "/v2/api/site/default/traffic-flows").Body, "search_text"), "kid-tablet.lan")
	eq(t, "default page size", get(lastTo(env.mock, "/v2/api/site/default/traffic-flows").Body, "pageSize"), 50.0)
	eq(t, "fields", get(res, "items"), js(`[{"service":"YouTube","source":{"name":"Kid tablet"}}]`))
}

func TestFixedAccessPoint(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	env.mock.
		On("GET", `^/api/s/default/stat/user/aa:bb:cc:00:00:50$`, fixed(unifitest.OK(js(`[{"_id":"u1","mac":"aa:bb:cc:00:00:50","is_wired":false}]`)))).
		On("GET", `^/api/s/default/stat/user/aa:bb:cc:00:00:51$`, fixed(unifitest.OK(js(`[{"_id":"u2","mac":"aa:bb:cc:00:00:51","is_wired":true}]`)))).
		On("PUT", `^/api/s/default/rest/user/([^/]+)$`, func(body any, m []string, _ url.Values) (int, any) {
			return unifitest.OK([]any{ojson.New("_id", m[1], "mac", "aa:bb:cc:00:00:50").Assign(body)})
		})
	res := env.call("unifi_update_client", `{"mac":"aa:bb:cc:00:00:50","fixed_ap_mac":"AA-BB-CC-00-00-02"}`)
	eq(t, "lock body", lastTo(env.mock, "/api/s/default/rest/user/u1").Body, js(`{"fixed_ap_enabled":true,"fixed_ap_mac":"aa:bb:cc:00:00:02"}`))
	eq(t, "summary", get(res, "locked_to_ap"), "aa:bb:cc:00:00:02")
	env.call("unifi_update_client", `{"mac":"aa:bb:cc:00:00:50","clear_fixed_ap":true}`)
	eq(t, "unlock body", lastTo(env.mock, "/api/s/default/rest/user/u1").Body, js(`{"fixed_ap_enabled":false}`))
	contains(t, "wired", env.callErr("unifi_update_client", `{"mac":"aa:bb:cc:00:00:51","fixed_ap_mac":"aa:bb:cc:00:00:02"}`), "Only wireless clients")
	contains(t, "both", env.callErr("unifi_update_client", `{"mac":"aa:bb:cc:00:00:50","fixed_ap_mac":"aa:bb:cc:00:00:02","clear_fixed_ap":true}`), "not both")
}

func TestSuperSettingsUseSetPath(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	env.mock.
		Seed("rest", "setting", `[{"_id":"s_super","key":"super_mgmt","autobackup_enabled":false,"autobackup_max_files":10}]`).
		On("PUT", `^/api/s/default/set/setting/super_mgmt/s_super$`, func(body any, _ []string, _ url.Values) (int, any) { return unifitest.OK([]any{body}) })
	res := env.call("unifi_update_site_setting", `{"key":"super_mgmt","changes":{"autobackup_enabled":true}}`)
	put := lastTo(env.mock, "/api/s/default/set/setting/super_mgmt/s_super")
	eq(t, "method", put.Method, "PUT")
	eq(t, "merged", put.Body, js(`{"_id":"s_super","key":"super_mgmt","autobackup_enabled":true,"autobackup_max_files":10}`))
	eq(t, "result", get(res, "autobackup_enabled"), true)
}
