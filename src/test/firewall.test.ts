import assert from "node:assert/strict";
import { after, before, beforeEach, describe, it } from "node:test";
import type { Client } from "@modelcontextprotocol/sdk/client/index.js";
import type { UnifiClient } from "../unifi/client.js";
import { connect, parse, startMock, toolOptions } from "./helpers.js";
import type { MockController } from "./mock-controller.js";

const call = async (mcp: Client, name: string, args: Record<string, unknown> = {}) => parse(await mcp.callTool({ name, arguments: args }));
const callErr = async (mcp: Client, name: string, args: Record<string, unknown> = {}) => {
  const res: any = await mcp.callTool({ name, arguments: args });
  assert.equal(res.isError, true, "expected a tool error");
  return res.content[0].text as string;
};

const SITE_UUID = "88f7af54-98f8-306a-a1c7-c9349722b1f6";

describe("firewall & routing tools", () => {
  let mock: MockController;
  let client: UnifiClient;
  let stop: () => Promise<void>;
  let mcp: Client;

  before(async () => {
    ({ mock, client, stop } = await startMock());
    mcp = await connect(client, toolOptions(true, true));
  });
  after(async () => {
    await mcp.close();
    await stop();
  });

  beforeEach(() => {
    mock.requests = [];
    mock
      .seed("rest", "firewallrule", [
        { _id: "r1", name: "Existing", ruleset: "LAN_IN", rule_index: 2000, action: "drop", enabled: true, protocol: "all" },
        { _id: "r2", name: "Later", ruleset: "LAN_IN", rule_index: 2003, action: "accept", enabled: true, protocol: "all" },
      ])
      .seed("rest", "firewallgroup", [{ _id: "g1", name: "Servers", group_type: "address-group", group_members: ["10.0.0.5"] }])
      .seed("rest", "portforward", [
        { _id: "p1", name: "Web", enabled: true, proto: "tcp", pfwd_interface: "wan", src: "any", dst_port: "443", fwd: "10.0.0.5", fwd_port: "443", log: false },
      ])
      .seed("rest", "routing", [
        { _id: "s1", name: "Lab", enabled: true, type: "static-route", "static-route_type": "nexthop-route", "static-route_network": "10.9.0.0/16", "static-route_nexthop": "10.0.0.254", "static-route_distance": 1 },
      ])
      .seed("v2", "firewall-policies", [
        { _id: "fp1", name: "Block IoT to LAN", action: "BLOCK", enabled: true, index: 10000, predefined: false, source: { zone_id: "z1", matching_target: "ANY" }, destination: { zone_id: "z2", matching_target: "ANY" } },
        { _id: "fp2", name: "Allow Return Traffic", action: "ALLOW", predefined: true, index: 30000 },
      ])
      .seed("v2", "firewall/zone", [
        { _id: "z1", name: "Internal", zone_key: "internal", default_zone: true, network_ids: ["n1"], external_id: "iz-internal" },
        { _id: "z2", name: "IoT", default_zone: false, network_ids: ["n2"] },
      ])
      .seed("v2", "trafficrules", [
        { _id: "t1", description: "Block games", enabled: true, action: "BLOCK", matching_target: "APP", app_ids: [1], target_devices: [{ type: "ALL_CLIENTS" }], bandwidth_limit: { enabled: false, download_limit_kbps: 1024, upload_limit_kbps: 1024 } },
      ])
      .seed("v2", "trafficroutes", [
        { _id: "tr1", description: "Netflix via VPN", enabled: true, matching_target: "DOMAIN", network_id: "vpn1", domains: [{ domain: "netflix.com", ports: [], port_ranges: [] }], target_devices: [{ type: "ALL_CLIENTS" }], kill_switch_enabled: false },
      ]);
  });

  it("hides write and delete tools unless enabled", async () => {
    const ro = await connect(client, toolOptions(false));
    const roNames = (await ro.listTools()).tools.map((t) => t.name);
    assert.ok(roNames.includes("unifi_list_traffic_routes"));
    assert.ok(roNames.includes("unifi_get_firewall_policy_ordering"));
    assert.ok(!roNames.includes("unifi_save_firewall_rule"));
    await ro.close();

    const rw = await connect(client, toolOptions(true, false));
    const rwNames = (await rw.listTools()).tools.map((t) => t.name);
    assert.ok(rwNames.includes("unifi_save_firewall_rule"));
    assert.ok(rwNames.includes("unifi_save_traffic_route"));
    for (const n of ["unifi_delete_firewall_rule", "unifi_delete_port_forward", "unifi_delete_firewall_policy", "unifi_delete_route", "unifi_delete_traffic_route"]) {
      assert.ok(!rwNames.includes(n), `${n} should require deletes`);
    }
    await rw.close();

    const all = (await mcp.listTools()).tools;
    const del = all.find((t) => t.name === "unifi_delete_traffic_rule");
    assert.equal(del?.annotations?.destructiveHint, true);
    assert.equal(all.find((t) => t.name === "unifi_save_port_forward")?.annotations?.destructiveHint, true);
  });

  it("creates a classic firewall rule with defaults and the next free index", async () => {
    const res = await call(mcp, "unifi_save_firewall_rule", {
      name: "Block IoT",
      ruleset: "LAN_IN",
      action: "drop",
      src_network_id: "n2",
      dst_group_ids: ["g1"],
      states: ["new"],
    });
    const body = mock.requestsTo("/api/s/default/rest/firewallrule").find((r) => r.method === "POST")!.body;
    assert.equal(body.rule_index, 2004);
    assert.equal(body.protocol, "all");
    assert.equal(body.enabled, true);
    assert.equal(body.src_networkconf_id, "n2");
    assert.equal(body.src_networkconf_type, "NETv4");
    assert.deepEqual(body.dst_firewallgroup_ids, ["g1"]);
    assert.equal(body.state_new, true);
    assert.equal(body.state_established, false);
    assert.equal(res.index, 2004);
    assert.equal(res.name, "Block IoT");
  });

  it("requires core fields on create", async () => {
    assert.match(await callErr(mcp, "unifi_save_firewall_rule", { name: "x" }), /ruleset and action are required/);
    assert.match(await callErr(mcp, "unifi_save_port_forward", { name: "x" }), /external_port and forward_ip are required/);
  });

  it("updates only the passed fields of a firewall rule", async () => {
    await call(mcp, "unifi_save_firewall_rule", { id: "r1", enabled: false });
    const put = mock.requestsTo("/api/s/default/rest/firewallrule/r1").find((r) => r.method === "PUT")!;
    assert.equal(put.body.enabled, false);
    assert.equal(put.body.name, "Existing");
    assert.equal(put.body.action, "drop");
    assert.equal(mock.store.get("rest/firewallrule")![0].enabled, false);
  });

  it("deletes a firewall rule", async () => {
    assert.deepEqual(await call(mcp, "unifi_delete_firewall_rule", { id: "r2" }), { deleted: "r2" });
    assert.equal(mock.store.get("rest/firewallrule")!.length, 1);
  });

  it("saves firewall groups and refuses type changes", async () => {
    const g = await call(mcp, "unifi_save_firewall_group", { name: "Web ports", type: "port-group", members: ["80", "443"] });
    assert.deepEqual(g.members, ["80", "443"]);
    assert.equal(g.type, "port-group");
    const upd = await call(mcp, "unifi_save_firewall_group", { id: "g1", members: ["10.0.0.5", "10.0.0.6"] });
    assert.equal(upd.name, "Servers");
    assert.deepEqual(upd.members, ["10.0.0.5", "10.0.0.6"]);
    assert.match(await callErr(mcp, "unifi_save_firewall_group", { id: "g1", type: "port-group" }), /cannot be changed/);
  });

  it("creates and updates port forwards", async () => {
    const pf = await call(mcp, "unifi_save_port_forward", { name: "SSH", external_port: "2222", forward_ip: "10.0.0.7", protocol: "tcp" });
    const body = mock.requestsTo("/api/s/default/rest/portforward").find((r) => r.method === "POST")!.body;
    assert.deepEqual(
      { iface: body.pfwd_interface, proto: body.proto, src: body.src, fwd_port: body.fwd_port, enabled: body.enabled, log: body.log },
      { iface: "wan", proto: "tcp", src: "any", fwd_port: "2222", enabled: true, log: false },
    );
    assert.equal(pf.forward_ip, "10.0.0.7");

    const upd = await call(mcp, "unifi_save_port_forward", { id: "p1", enabled: false });
    assert.equal(upd.enabled, false);
    assert.equal(upd.external_port, "443");
    await call(mcp, "unifi_delete_port_forward", { id: "p1" });
    assert.equal(mock.store.get("rest/portforward")!.some((p) => p._id === "p1"), false);
  });

  it("lists firewall policies without predefined ones by default", async () => {
    const list = await call(mcp, "unifi_list_firewall_policies");
    assert.deepEqual(list.map((p: any) => p.id), ["fp1"]);
    assert.equal(list[0].source.zone_id, "z1");
    const all = await call(mcp, "unifi_list_firewall_policies", { include_predefined: true });
    assert.equal(all.length, 2);
  });

  it("creates a zone-based policy with defaults and normalised endpoints", async () => {
    await call(mcp, "unifi_save_firewall_policy", {
      name: "Block IoT DNS",
      action: "BLOCK",
      protocol: "tcp_udp",
      source: { zone_id: "z2", network_ids: ["n2"] },
      destination: { zone_id: "z1", ips: ["10.0.0.53"], port: "53" },
    });
    const body = mock.requestsTo("/v2/api/site/default/firewall-policies").find((r) => r.method === "POST")!.body;
    assert.equal(body.enabled, true);
    assert.equal(body.ip_version, "BOTH");
    assert.equal(body.connection_state_type, "ALL");
    assert.deepEqual(body.schedule, { mode: "ALWAYS" });
    assert.equal(body.create_allow_respond, false);
    assert.equal(body.source.matching_target, "NETWORK");
    assert.equal(body.source.matching_target_type, "OBJECT");
    assert.equal(body.destination.matching_target, "IP");
    assert.equal(body.destination.matching_target_type, "SPECIFIC");
    assert.equal(body.destination.port_matching_type, "SPECIFIC");

    assert.match(
      await callErr(mcp, "unifi_save_firewall_policy", { name: "x", action: "ALLOW", source: { matching_target: "ANY" }, destination: { zone_id: "z1" } }),
      /source\.zone_id is required/,
    );
  });

  it("merges policy endpoints on update and protects predefined policies", async () => {
    const res = await call(mcp, "unifi_save_firewall_policy", { id: "fp1", destination: { port: "443" }, logging: true });
    const put = mock.requestsTo("/v2/api/site/default/firewall-policies/fp1").find((r) => r.method === "PUT")!.body;
    assert.equal(put.destination.zone_id, "z2");
    assert.equal(put.destination.port, "443");
    assert.equal(put.logging, true);
    assert.equal(put.name, "Block IoT to LAN");
    assert.equal(res.logging, true);

    assert.match(await callErr(mcp, "unifi_save_firewall_policy", { id: "fp2", enabled: false }), /Predefined/);
    assert.match(await callErr(mcp, "unifi_delete_firewall_policy", { id: "fp2" }), /Predefined/);
    assert.equal((await call(mcp, "unifi_delete_firewall_policy", { id: "fp1" })).deleted, "fp1");
  });

  it("manages zones and ordering through the integration API", async () => {
    const izones = [
      { id: "iz-internal", name: "Internal", networkIds: ["n1"], metadata: { origin: "SYSTEM_DEFINED" } },
      { id: "iz-iot", name: "IoT", networkIds: ["n2"], metadata: { origin: "USER_DEFINED" } },
    ];
    const seen: any[] = [];
    mock
      .on("GET", /^\/integration\/v1\/sites$/, () => [200, { data: [{ id: SITE_UUID, internalReference: "default", name: "Default" }], totalCount: 1 }])
      .on("GET", /^\/integration\/v1\/sites\/[^/]+\/firewall\/zones$/, () => [200, { data: izones, totalCount: izones.length }])
      .on("POST", /^\/integration\/v1\/sites\/[^/]+\/firewall\/zones$/, (body) => [201, { id: "iz-new", ...body }])
      .on("PUT", /^\/integration\/v1\/sites\/[^/]+\/firewall\/zones\/([^/]+)$/, (body, m) => {
        seen.push({ id: m[1], body });
        return [200, { id: m[1], ...body }];
      })
      .on("DELETE", /^\/integration\/v1\/sites\/[^/]+\/firewall\/zones\/([^/]+)$/, (_b, m) => {
        seen.push({ deleted: m[1] });
        return [200, {}];
      })
      .on("GET", /^\/integration\/v1\/sites\/[^/]+\/firewall\/policies\/ordering$/, (_b, _m, q) => {
        seen.push({ query: Object.fromEntries(q) });
        return [200, { orderedFirewallPolicyIds: { beforeSystemDefined: ["a", "b"], afterSystemDefined: [] } }];
      })
      .on("PUT", /^\/integration\/v1\/sites\/[^/]+\/firewall\/policies\/ordering$/, (body) => [200, body]);

    const created = await call(mcp, "unifi_save_firewall_zone", { name: "Cameras", network_ids: ["n3"] });
    assert.deepEqual(created, { id: "iz-new", name: "Cameras", networkIds: ["n3"] });

    // v2 id z2 maps to the integration zone by name; network_ids kept when not passed.
    await call(mcp, "unifi_save_firewall_zone", { id: "z2", name: "Smart home" });
    assert.deepEqual(seen.at(-1), { id: "iz-iot", body: { name: "Smart home", networkIds: ["n2"] } });

    assert.match(await callErr(mcp, "unifi_save_firewall_zone", { id: "z1", name: "Renamed" }), /system-defined/);
    assert.match(await callErr(mcp, "unifi_delete_firewall_zone", { id: "z1" }), /system-defined/);
    await call(mcp, "unifi_delete_firewall_zone", { id: "iz-iot" });
    assert.deepEqual(seen.at(-1), { deleted: "iz-iot" });

    const order = await call(mcp, "unifi_get_firewall_policy_ordering", { source_zone_id: "z1", destination_zone_id: "z2" });
    assert.deepEqual(order.orderedFirewallPolicyIds.beforeSystemDefined, ["a", "b"]);
    assert.deepEqual(seen.at(-1), { query: { sourceFirewallZoneId: "iz-internal", destinationFirewallZoneId: "iz-iot" } });

    const put = await call(mcp, "unifi_set_firewall_policy_ordering", {
      source_zone_id: "z1",
      destination_zone_id: "z2",
      before_system_defined: ["b", "a"],
    });
    assert.deepEqual(put, { orderedFirewallPolicyIds: { beforeSystemDefined: ["b", "a"], afterSystemDefined: [] } });
    assert.match(
      await callErr(mcp, "unifi_set_firewall_policy_ordering", { source_zone_id: "z1", destination_zone_id: "z2", before_system_defined: ["a"] }),
      /exactly the current policy IDs/,
    );
    assert.equal(mock.requestsTo("/integration/v1/sites")[0].headers["x-api-key"], "key123");
  });

  it("creates traffic rules with defaults and targets", async () => {
    const res = await call(mcp, "unifi_save_traffic_rule", {
      description: "No social at night",
      action: "BLOCK",
      matching_target: "DOMAIN",
      domains: ["facebook.com"],
      client_macs: ["AA-BB-CC-DD-EE-FF"],
      schedule: { mode: "EVERY_DAY", time_range_start: "22:00", time_range_end: "06:00" },
    });
    const body = mock.requestsTo("/v2/api/site/default/trafficrules").find((r) => r.method === "POST")!.body;
    assert.deepEqual(body.domains, [{ domain: "facebook.com", ports: [], port_ranges: [] }]);
    assert.deepEqual(body.target_devices, [{ type: "CLIENT", client_mac: "aa:bb:cc:dd:ee:ff" }]);
    assert.deepEqual(body.app_ids, []);
    assert.equal(body.bandwidth_limit.enabled, false);
    assert.equal(body.enabled, true);
    assert.deepEqual(res.domains, ["facebook.com"]);
    assert.deepEqual(res.targets, ["client:aa:bb:cc:dd:ee:ff"]);
  });

  it("updates traffic rules (bandwidth limit merge) and deletes them", async () => {
    const res = await call(mcp, "unifi_save_traffic_rule", { id: "t1", download_limit_kbps: 5000 });
    const put = mock.requestsTo("/v2/api/site/default/trafficrules/t1").find((r) => r.method === "PUT")!.body;
    assert.deepEqual(put.bandwidth_limit, { enabled: true, download_limit_kbps: 5000, upload_limit_kbps: 1024 });
    assert.deepEqual(put.app_ids, [1]);
    assert.deepEqual(res.bandwidth_limit, { down_kbps: 5000, up_kbps: 1024 });
    await call(mcp, "unifi_delete_traffic_rule", { id: "t1" });
    assert.equal(mock.store.get("v2/trafficrules")!.length, 0);
  });

  it("manages traffic routes", async () => {
    const list = await call(mcp, "unifi_list_traffic_routes");
    assert.deepEqual(list[0].domains, ["netflix.com"]);

    await call(mcp, "unifi_save_traffic_route", {
      description: "Work VPN",
      matching_target: "IP",
      network_id: "vpn2",
      ip_addresses: ["192.168.50.0/24"],
      network_ids: ["n2"],
      kill_switch: true,
    });
    const body = mock.requestsTo("/v2/api/site/default/trafficroutes").find((r) => r.method === "POST")!.body;
    assert.deepEqual(body.ip_addresses, [{ ip_or_subnet: "192.168.50.0/24", ip_version: "v4", ports: [], port_ranges: [] }]);
    assert.deepEqual(body.target_devices, [{ type: "NETWORK", network_id: "n2" }]);
    assert.equal(body.kill_switch_enabled, true);
    assert.equal(body.next_hop, "");

    assert.match(
      await callErr(mcp, "unifi_save_traffic_route", { description: "All", matching_target: "INTERNET", network_id: "vpn2" }),
      /explicit client_macs or network_ids/,
    );

    const upd = await call(mcp, "unifi_save_traffic_route", { id: "tr1", enabled: false });
    assert.equal(upd.enabled, false);
    assert.equal(upd.network_id, "vpn1");
    await call(mcp, "unifi_delete_traffic_route", { id: "tr1" });
    assert.equal(mock.store.get("v2/trafficroutes")!.some((r) => r._id === "tr1"), false);
  });

  it("manages static routes with hyphenated fields", async () => {
    const list = await call(mcp, "unifi_list_routes");
    assert.deepEqual(list[0], { id: "s1", name: "Lab", enabled: true, type: "nexthop", destination: "10.9.0.0/16", next_hop: "10.0.0.254", distance: 1 });

    const created = await call(mcp, "unifi_save_route", { name: "Null", type: "blackhole", destination: "10.66.0.0/16" });
    const body = mock.requestsTo("/api/s/default/rest/routing").find((r) => r.method === "POST")!.body;
    assert.deepEqual(body, {
      type: "static-route",
      enabled: true,
      gateway_type: "default",
      "static-route_type": "blackhole",
      "static-route_distance": 1,
      name: "Null",
      "static-route_network": "10.66.0.0/16",
    });
    assert.equal(created.type, "blackhole");
    assert.match(await callErr(mcp, "unifi_save_route", { name: "x", destination: "10.1.0.0/16" }), /next_hop is required/);

    const upd = await call(mcp, "unifi_save_route", { id: "s1", distance: 5 });
    assert.equal(upd.distance, 5);
    assert.equal(upd.next_hop, "10.0.0.254");
    await call(mcp, "unifi_delete_route", { id: "s1" });
    assert.equal(mock.store.get("rest/routing")!.some((r) => r._id === "s1"), false);
  });

  it("returns the active routing table when asked", async () => {
    mock.on("GET", /^\/api\/s\/default\/stat\/routing$/, () => [200, { meta: { rc: "ok" }, data: [{ pfx: "0.0.0.0/0", nh: [{ intf: "eth8" }] }] }]);
    const res = await call(mcp, "unifi_list_routes", { active: true });
    assert.equal(res[0].pfx, "0.0.0.0/0");
  });
});
