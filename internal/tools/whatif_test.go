package tools

import (
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

func whatIfEnv(t *testing.T) *testEnv {
	env := startMock(t, toolOptions(false, false))
	env.mock.
		Seed("rest", "networkconf", `[
			{"_id":"n-lan","name":"LAN","purpose":"corporate","ip_subnet":"192.0.2.1/25"},
			{"_id":"n-iot","name":"IoT","purpose":"corporate","ip_subnet":"192.0.2.129/26"},
			{"_id":"n-cam","name":"Cameras","purpose":"corporate","ip_subnet":"192.0.2.193/26"}]`).
		Seed("v2", "firewall/zone", `[
			{"_id":"z-int","name":"Internal","zone_key":"internal","network_ids":["n-lan"]},
			{"_id":"z-iot","name":"IoT","network_ids":["n-iot"]},
			{"_id":"z-ext","name":"External","zone_key":"external","network_ids":[]},
			{"_id":"z-gw","name":"Gateway","zone_key":"gateway","network_ids":[]}]`).
		Seed("v2", "firewall-policies", `[
			{"_id":"p-default","name":"Block All","action":"BLOCK","predefined":true,"index":30000,"source":{"zone_id":"z-iot","matching_target":"ANY"},"destination":{"zone_id":"z-int","matching_target":"ANY"}},
			{"_id":"p-dns","name":"IoT to DNS server","action":"ALLOW","enabled":true,"index":10001,"protocol":"tcp_udp","source":{"zone_id":"z-iot","matching_target":"NETWORK","network_ids":["n-iot"]},"destination":{"zone_id":"z-int","matching_target":"IP","matching_target_type":"SPECIFIC","ips":["192.0.2.10"],"port_matching_type":"SPECIFIC","port":"53"}},
			{"_id":"p-off","name":"Disabled allow all","action":"ALLOW","enabled":false,"index":10000,"source":{"zone_id":"z-iot","matching_target":"ANY"},"destination":{"zone_id":"z-int","matching_target":"ANY"}},
			{"_id":"p-app","name":"Allow casting","action":"ALLOW","enabled":true,"index":10002,"source":{"zone_id":"z-iot","matching_target":"ANY"},"destination":{"zone_id":"z-int","matching_target":"APP"}},
			{"_id":"p-return","name":"Return only","action":"ALLOW","enabled":true,"index":9000,"connection_state_type":"RESPOND_ONLY","source":{"zone_id":"z-iot","matching_target":"ANY"},"destination":{"zone_id":"z-int","matching_target":"ANY"}},
			{"_id":"p-printer","name":"LAN printer","action":"ALLOW","enabled":true,"index":10003,"source":{"zone_id":"z-int","matching_target":"CLIENT","client_macs":["AA:BB:CC:00:00:11"]},"destination":{"zone_id":"z-iot","matching_target":"IP","ips":["192.0.2.130-192.0.2.140"],"match_opposite_ips":false}},
			{"_id":"p-wan","name":"Block IoT to WAN","action":"REJECT","enabled":true,"index":10004,"source":{"zone_id":"z-iot","matching_target":"ANY"},"destination":{"zone_id":"z-ext","matching_target":"ANY"}}]`).
		Seed("v2", "trafficrules", `[
			{"_id":"t-ip","description":"Block bad host","enabled":true,"action":"BLOCK","matching_target":"IP","ip_addresses":[{"ip_or_subnet":"198.51.100.0/24","ports":[],"port_ranges":[]}],"target_devices":[{"type":"NETWORK","network_id":"n-iot"}]},
			{"_id":"t-dom","description":"Block social","enabled":true,"action":"BLOCK","matching_target":"DOMAIN","domains":[{"domain":"example.com"}],"target_devices":[{"type":"ALL_CLIENTS"}]},
			{"_id":"t-other","description":"Other client","enabled":true,"action":"BLOCK","matching_target":"INTERNET","target_devices":[{"type":"CLIENT","client_mac":"aa:bb:cc:00:00:99"}]}]`)
	env.mock.On("GET", `^/api/s/default/stat/sta$`, fixed(unifitest.OK(js(`[{"mac":"aa:bb:cc:00:00:11","ip":"192.0.2.20"},{"mac":"aa:bb:cc:00:00:22","ip":"192.0.2.131"}]`))))
	return env
}

func TestCheckFirewallPath(t *testing.T) {
	env := whatIfEnv(t)
	if toolMap(t, env.mcp)["unifi_check_firewall_path"] == nil {
		t.Fatal("unifi_check_firewall_path should be a read tool")
	}

	// IoT client to the DNS server on 53: the disabled and respond-only
	// policies are skipped, the DNS policy matches.
	res := env.call("unifi_check_firewall_path", `{"source":"aa-bb-cc-00-00-22","destination":"192.0.2.10","protocol":"udp","port":53}`)
	eq(t, "dns verdict", get(res, "verdict"), "ALLOW")
	eq(t, "dns decided", get(res, "decided_by", "id"), "p-dns")
	eq(t, "dns certain", get(res, "certain"), true)
	eq(t, "source", get(res, "source"), js(`{"ip":"192.0.2.131","mac":"aa:bb:cc:00:00:22","network":{"id":"n-iot","name":"IoT","subnet":"192.0.2.129/26"},"zone":{"id":"z-iot","name":"IoT"}}`))
	eq(t, "destination zone", get(res, "destination", "zone", "id"), "z-int")

	// Another port: the DNS policy doesn't match; the APP policy might, so the
	// default block isn't certain.
	res = env.call("unifi_check_firewall_path", `{"source":"192.0.2.131","destination":"192.0.2.10","port":22}`)
	eq(t, "ssh verdict", get(res, "verdict"), "BLOCK")
	eq(t, "ssh decided", get(res, "decided_by", "id"), "p-default")
	eq(t, "ssh certain", get(res, "certain"), false)
	eq(t, "ssh maybe", get(res, "may_match_first"), js(`[{"id":"p-app","name":"Allow casting","index":10002,"action":"ALLOW","unknown":["destination matches by app, which can't be evaluated from addresses"]}]`))

	// No port: the DNS policy is a maybe.
	res = env.call("unifi_check_firewall_path", `{"source":"192.0.2.131","destination":"192.0.2.10"}`)
	eq(t, "no port maybe", get(res, "may_match_first", 0, "id"), "p-dns")

	// ICMP never matches port policies.
	res = env.call("unifi_check_firewall_path", `{"source":"192.0.2.131","destination":"192.0.2.10","protocol":"icmp"}`)
	eq(t, "icmp maybe", get(res, "may_match_first", 0, "id"), "p-app")

	// Client MAC match and an IP range, in the other direction.
	res = env.call("unifi_check_firewall_path", `{"source":"192.0.2.20","destination":"192.0.2.135","port":9100}`)
	eq(t, "printer", get(res, "decided_by", "id"), "p-printer")
	res = env.call("unifi_check_firewall_path", `{"source":"192.0.2.21","destination":"192.0.2.135"}`)
	eq(t, "not the printer client", get(res, "verdict"), "unknown")
	contains(t, "note", ojson.String(get(res, "note")), "No enabled policy")
	eq(t, "client unknown", get(res, "may_match_first", 0, "unknown", 0), "source matches clients by MAC, and 192.0.2.21 isn't a connected client")

	// Outside every subnet: the External zone, and traffic rules.
	res = env.call("unifi_check_firewall_path", `{"source":"192.0.2.131","destination":"198.51.100.7","port":443}`)
	eq(t, "wan zone", get(res, "destination", "zone", "id"), "z-ext")
	eq(t, "wan verdict", get(res, "verdict"), "REJECT")
	eq(t, "rules", get(res, "traffic_rules"), js(`{"matching":[{"id":"t-ip","description":"Block bad host","action":"BLOCK","matching_target":"IP"}],"may_match":[{"id":"t-dom","description":"Block social","action":"BLOCK","matching_target":"DOMAIN","unknown":["matches by domain, which can't be evaluated from addresses"]}]}`))

	// Zone override.
	res = env.call("unifi_check_firewall_path", `{"source":"192.0.2.131","destination":"192.0.2.1","destination_zone_id":"z-gw"}`)
	eq(t, "gateway zone", get(res, "destination", "zone", "id"), "z-gw")
	eq(t, "nothing between", get(res, "policies_checked"), 0.0)

	contains(t, "no zone", env.callErr("unifi_check_firewall_path", `{"source":"192.0.2.200","destination":"192.0.2.10"}`), `Network "Cameras" (source) isn't in any firewall zone`)
	contains(t, "bad source", env.callErr("unifi_check_firewall_path", `{"source":"nope","destination":"192.0.2.10"}`), "must be an IP address or a client MAC address")
	contains(t, "offline mac", env.callErr("unifi_check_firewall_path", `{"source":"aa:bb:cc:00:00:77","destination":"192.0.2.10"}`), "isn't a connected client")
	contains(t, "mixed", env.callErr("unifi_check_firewall_path", `{"source":"192.0.2.131","destination":"2001:db8::1"}`), "IPv4 or both IPv6")
	contains(t, "bad override", env.callErr("unifi_check_firewall_path", `{"source":"192.0.2.131","destination":"192.0.2.10","source_zone_id":"nope"}`), `No firewall zone with id "nope"`)

	for _, r := range env.mock.Requests() {
		if r.Method != "GET" {
			t.Errorf("sent %s %s", r.Method, r.Path)
		}
	}
}

func TestWhatIfMatchers(t *testing.T) {
	for _, c := range []struct {
		port int
		spec string
		want tri
	}{{53, "53", yes}, {443, "80,443", yes}, {8050, "8000-8100", yes}, {22, "80,443", no}, {0, "53", maybe}, {53, "dns", maybe}} {
		if got := portSpecMatches(c.port, c.spec); got != c.want {
			t.Errorf("port %d %q: %s", c.port, c.spec, got)
		}
	}
}
