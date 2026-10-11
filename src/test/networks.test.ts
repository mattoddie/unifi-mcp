import assert from "node:assert/strict";
import { after, before, beforeEach, describe, it } from "node:test";
import type { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { subnetRange } from "../tools/networks.js";
import type { UnifiClient } from "../unifi/client.js";
import { connect, parse, startMock, toolOptions } from "./helpers.js";
import type { MockController } from "./mock-controller.js";

const LAN = {
  _id: "net-lan",
  name: "Default",
  purpose: "corporate",
  networkgroup: "LAN",
  ip_subnet: "192.168.1.1/24",
  dhcpd_enabled: true,
  dhcpd_start: "192.168.1.6",
  dhcpd_stop: "192.168.1.254",
  attr_no_delete: true,
};
const WAN = {
  _id: "net-wan",
  name: "Primary (WAN1)",
  purpose: "wan",
  wan_networkgroup: "WAN",
  wan_type: "dhcp",
  wan_load_balance_type: "failover-only",
  wan_provider_capabilities: { download_kilobits_per_second: 100000, upload_kilobits_per_second: 20000 },
};
const WG = {
  _id: "net-wg",
  name: "WireGuard",
  purpose: "remote-user-vpn",
  vpn_type: "wireguard-server",
  ip_subnet: "192.168.3.1/24",
  local_port: 51820,
  x_wireguard_private_key: "PRIVATE",
};

describe("network, WiFi and VPN tools", () => {
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
      .seed("rest", "networkconf", [LAN, WAN, WG])
      .seed("rest", "usergroup", [{ _id: "ug-default", name: "Default", qos_rate_max_down: -1, qos_rate_max_up: -1, attr_no_delete: true }])
      .seed("rest", "wlanconf", [
        { _id: "w1", name: "Home", enabled: true, security: "wpapsk", wpa_mode: "wpa2", x_passphrase: "hunter22", networkconf_id: "net-lan" },
      ])
      .seed("v2", "apgroups", [{ _id: "apg-all", name: "All APs", attr_no_delete: true }])
      .seed("v2", "static-dns", [])
      .seed("rest", "dynamicdns", [])
      .seed("rest", "radiusprofile", [{ _id: "rp-default", name: "Default", attr_no_delete: true, auth_servers: [] }])
      .seed("rest", "account", []);
  });

  const call = async (name: string, args: Record<string, unknown>) => mcp.callTool({ name, arguments: args });
  const lastBody = (method: string) => [...mock.requests].reverse().find((r) => r.method === method)!.body;

  it("derives DHCP ranges from a subnet", () => {
    assert.deepEqual(subnetRange("192.168.10.1/24"), { gateway: "192.168.10.1", first: "192.168.10.6", last: "192.168.10.254" });
    assert.deepEqual(subnetRange("10.0.0.1/30"), { gateway: "10.0.0.1", first: "10.0.0.2", last: "10.0.0.2" });
    assert.throws(() => subnetRange("10.0.0.0/24"), /gateway IP/);
    assert.throws(() => subnetRange("10.0.0.1"), /gateway\/prefix/);
  });

  it("creates a VLAN network with sensible defaults", async () => {
    const res = parse(await call("unifi_save_network", { name: "IoT", vlan: 20, subnet: "192.168.20.1/24", dhcp_dns: ["1.1.1.1"] }));
    const body = lastBody("POST");
    assert.equal(body.purpose, "corporate");
    assert.equal(body.networkgroup, "LAN");
    assert.equal(body.vlan_enabled, true);
    assert.equal(body.vlan, 20);
    assert.equal(body.dhcpd_enabled, true);
    assert.equal(body.dhcpd_start, "192.168.20.6");
    assert.equal(body.dhcpd_stop, "192.168.20.254");
    assert.equal(body.dhcpd_leasetime, 86400);
    assert.equal(body.dhcpd_dns_enabled, true);
    assert.equal(body.dhcpd_dns_1, "1.1.1.1");
    assert.equal(res.name, "IoT");
    assert.equal(res.dhcp_range, "192.168.20.6-192.168.20.254");
  });

  it("updates only the passed network fields and keeps the rest", async () => {
    parse(await call("unifi_save_network", { id: "net-lan", domain_name: "home.arpa", igmp_snooping: false }));
    const body = lastBody("PUT");
    assert.equal(body.domain_name, "home.arpa");
    assert.equal(body.igmp_snooping, false);
    assert.equal(body.ip_subnet, "192.168.1.1/24");
    assert.equal(body.dhcpd_start, "192.168.1.6");
    assert.equal(body.name, "Default");
  });

  it("refuses to edit or delete WAN/VPN networks with the LAN tools", async () => {
    const edit: any = await call("unifi_save_network", { id: "net-wan", name: "x" });
    assert.equal(edit.isError, true);
    assert.match(edit.content[0].text, /unifi_update_wan/);
    const del: any = await call("unifi_delete_network", { id: "net-wan" });
    assert.equal(del.isError, true);
    const builtIn: any = await call("unifi_delete_network", { id: "net-lan" });
    assert.match(builtIn.content[0].text, /built-in/);
    assert.ok(!mock.requests.some((r) => r.method === "DELETE"));
  });

  it("deletes a LAN network", async () => {
    mock.seed("rest", "networkconf", [LAN, { _id: "net-iot", name: "IoT", purpose: "corporate" }]);
    parse(await call("unifi_delete_network", { id: "net-iot" }));
    assert.deepEqual(mock.store.get("rest/networkconf")!.map((n) => n._id), ["net-lan"]);
  });

  it("lists and updates WANs", async () => {
    const list = parse(await call("unifi_list_wans", {}));
    assert.equal(list.wans.length, 1);
    assert.equal(list.wans[0].isp_download_kbps, 100000);

    const res = parse(
      await call("unifi_update_wan", {
        id: "net-wan",
        type: "static",
        static_ip: "203.0.113.10",
        netmask: "255.255.255.0",
        gateway: "203.0.113.1",
        dns: ["9.9.9.9"],
        smart_queues: true,
        download_kbps: 90000,
      }),
    );
    const body = lastBody("PUT");
    assert.equal(body.wan_type, "static");
    assert.equal(body.wan_ip, "203.0.113.10");
    assert.equal(body.wan_dns_preference, "manual");
    assert.equal(body.wan_dns1, "9.9.9.9");
    assert.equal(body.wan_smartq_enabled, true);
    assert.equal(body.wan_smartq_down_rate, 90000);
    assert.deepEqual(body.wan_provider_capabilities, { download_kilobits_per_second: 90000, upload_kilobits_per_second: 20000 });
    assert.equal(res.static.ip, "203.0.113.10");

    const bad: any = await call("unifi_update_wan", { id: "net-lan", type: "dhcp" });
    assert.match(bad.content[0].text, /not a WAN/);
  });

  it("creates a WLAN with default groups and hides the passphrase", async () => {
    const res = parse(
      await call("unifi_save_wlan", { name: "Guests", passphrase: "welcome123", wpa3: "transition", network_id: "net-lan", client_isolation: true }),
    );
    const body = lastBody("POST");
    assert.equal(body.security, "wpapsk");
    assert.equal(body.x_passphrase, "welcome123");
    assert.equal(body.wpa3_support, true);
    assert.equal(body.wpa3_transition, true);
    assert.equal(body.usergroup_id, "ug-default");
    assert.deepEqual(body.ap_group_ids, ["apg-all"]);
    assert.equal(body.l2_isolation, true);
    assert.deepEqual(body.wlan_bands, ["2g", "5g"]);
    assert.equal(res.passphrase, undefined);
    assert.equal(res.wpa3, "transition");
  });

  it("requires a passphrase for new WPA-PSK WLANs", async () => {
    const res: any = await call("unifi_save_wlan", { name: "X", security: "wpapsk" });
    assert.equal(res.isError, true);
    assert.match(res.content[0].text, /passphrase is required/);
  });

  it("updates a WLAN without dropping its passphrase", async () => {
    parse(await call("unifi_save_wlan", { id: "w1", hidden: true, mac_filter: { policy: "allow", macs: ["AA-BB-CC-DD-EE-FF"] } }));
    const body = lastBody("PUT");
    assert.equal(body.hide_ssid, true);
    assert.equal(body.x_passphrase, "hunter22");
    assert.equal(body.name, "Home");
    assert.deepEqual(body.mac_filter_list, ["aa:bb:cc:dd:ee:ff"]);
    assert.equal(body.mac_filter_policy, "allow");
  });

  it("builds a WiFi QR string with escaping", async () => {
    mock.seed("rest", "wlanconf", [{ _id: "w2", name: "Cafe;Net", security: "wpapsk", x_passphrase: 'p:a"ss', hide_ssid: true }]);
    const res = parse(await call("unifi_get_wifi_qr", { id: "w2" }));
    assert.equal(res.wifi_string, 'WIFI:T:WPA;S:Cafe\\;Net;P:p\\:a\\"ss;H:true;;');
  });

  it("manages local DNS records", async () => {
    const created = parse(await call("unifi_save_dns_record", { name: "nas.home.arpa", value: "192.168.1.10" }));
    assert.deepEqual(lastBody("POST"), { key: "nas.home.arpa", value: "192.168.1.10", record_type: "A", ttl: 0, enabled: true });
    parse(await call("unifi_save_dns_record", { id: created.id, value: "192.168.1.11" }));
    assert.equal(mock.store.get("v2/static-dns")![0].value, "192.168.1.11");
    const list = parse(await call("unifi_list_dns_records", {}));
    assert.deepEqual(list[0], { id: created.id, type: "A", name: "nas.home.arpa", value: "192.168.1.11", ttl: 0, enabled: true });
    parse(await call("unifi_delete_dns_record", { id: created.id }));
    assert.equal(mock.store.get("v2/static-dns")!.length, 0);
  });

  it("manages bandwidth profiles and protects the default", async () => {
    const created = parse(await call("unifi_save_bandwidth_profile", { name: "Kids", download_kbps: 10000 }));
    assert.deepEqual(lastBody("POST"), { name: "Kids", qos_rate_max_down: 10000, qos_rate_max_up: -1 });
    assert.equal(created.upload_kbps, "unlimited");
    const res: any = await call("unifi_delete_bandwidth_profile", { id: "ug-default" });
    assert.match(res.content[0].text, /cannot be deleted/);
  });

  it("redacts dynamic DNS passwords", async () => {
    parse(await call("unifi_save_dynamic_dns", { service: "duckdns", hostname: "me.duckdns.org", password: "tok" }));
    assert.equal(lastBody("POST").interface, "wan");
    const list = parse(await call("unifi_list_dynamic_dns", {}));
    assert.equal(list[0].password, "[redacted]");
    const withSecrets = parse(await call("unifi_list_dynamic_dns", { include_secrets: true }));
    assert.equal(withSecrets[0].password, "tok");
  });

  it("creates and lists VPNs with secrets redacted", async () => {
    const created = parse(await call("unifi_save_vpn", { kind: "wireguard-server", name: "WG2", subnet: "192.168.4.1/24" }));
    const body = lastBody("POST");
    assert.equal(body.purpose, "remote-user-vpn");
    assert.equal(body.vpn_type, "wireguard-server");
    assert.equal(body.local_port, 51820);
    assert.equal(created.kind, "wireguard-server");

    const l2tp: any = await call("unifi_save_vpn", { kind: "l2tp-server", name: "L2TP", subnet: "192.168.5.1/24" });
    assert.match(l2tp.content[0].text, /pre_shared_key is required/);
    parse(await call("unifi_save_vpn", { kind: "l2tp-server", name: "L2TP", subnet: "192.168.5.1/24", pre_shared_key: "psk" }));
    assert.equal(lastBody("POST").radiusprofile_id, "rp-default");

    const list = parse(await call("unifi_list_vpns", {}));
    assert.deepEqual(list.map((v: any) => v.kind).sort(), ["l2tp-server", "wireguard-server", "wireguard-server"]);
    const raw = parse(await call("unifi_list_vpns", { raw: true }));
    assert.equal(raw.find((v: any) => v._id === "net-wg").x_wireguard_private_key, "[redacted]");

    const del: any = await call("unifi_delete_vpn", { id: "net-lan" });
    assert.match(del.content[0].text, /not a VPN/);
  });

  it("manages RADIUS profiles and accounts", async () => {
    parse(await call("unifi_save_radius_profile", { name: "NPS", auth_servers: [{ ip: "10.0.0.5", secret: "s3" }] }));
    assert.deepEqual(lastBody("POST").auth_servers, [{ ip: "10.0.0.5", port: 1812, x_secret: "s3" }]);
    const profiles = parse(await call("unifi_list_radius_profiles", {}));
    assert.deepEqual(profiles[1].auth_servers, [{ ip: "10.0.0.5", port: 1812 }]);

    const bad: any = await call("unifi_save_radius_account", { name: "bob", password: "pw", tunnel_type: 13 });
    assert.match(bad.content[0].text, /set together/);
    parse(await call("unifi_save_radius_account", { name: "bob", password: "pw", vlan: 30, tunnel_type: 13, tunnel_medium_type: 6 }));
    const accounts = parse(await call("unifi_list_radius_accounts", {}));
    assert.deepEqual(accounts[0], { id: accounts[0].id, name: "bob", vlan: 30, tunnel_type: 13, tunnel_medium_type: 6 });
  });

  it("gates write and delete tools", async () => {
    const names = async (opts: ReturnType<typeof toolOptions>) => {
      const c = await connect(client, opts);
      const { tools } = await c.listTools();
      await c.close();
      return tools.map((t) => t.name);
    };
    const readOnly = await names(toolOptions(false));
    assert.ok(readOnly.includes("unifi_list_wans") && readOnly.includes("unifi_list_vpns"));
    assert.ok(!readOnly.includes("unifi_save_wlan"));
    const writes = await names(toolOptions(true));
    assert.ok(writes.includes("unifi_save_wlan") && writes.includes("unifi_save_vpn"));
    assert.ok(!writes.some((n) => /^unifi_delete_(network|wlan|vpn|dns_record|bandwidth_profile|radius_)/.test(n)));
    const all = await names(toolOptions(true, true));
    for (const n of ["unifi_delete_network", "unifi_delete_wlan", "unifi_delete_vpn", "unifi_delete_dns_record", "unifi_delete_radius_account"]) {
      assert.ok(all.includes(n), n);
    }
  });
});
