import assert from "node:assert/strict";
import { after, before, beforeEach, describe, it } from "node:test";
import type { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { connect, parse, startMock, toolOptions } from "./helpers.js";
import type { UnifiClient } from "../unifi/client.js";
import type { MockController } from "./mock-controller.js";

const ok = (data: unknown): [number, unknown] => [200, { meta: { rc: "ok" }, data }];

const SWITCH = {
  _id: "d1",
  mac: "aa:bb:cc:00:00:01",
  name: "Office Switch",
  model: "US8P60",
  port_table: [
    { port_idx: 1, name: "Port 1", up: true, enable: true, speed: 1000, poe_mode: "auto", portconf_id: "pc-all" },
    { port_idx: 2, name: "Port 2", up: false, enable: true, poe_mode: "auto", portconf_id: "pc-all" },
    { port_idx: 3, name: "Port 3", up: true, enable: true, speed: 100 },
  ],
  port_overrides: [{ port_idx: 2, name: "Printer", portconf_id: "pc-iot" }],
};

const AP = {
  _id: "d2",
  mac: "aa:bb:cc:00:00:02",
  name: "Lounge AP",
  type: "uap",
  radio_table: [
    { radio: "ng", channel: 6, ht: 20, tx_power_mode: "auto" },
    { radio: "na", channel: "auto", ht: 80, tx_power_mode: "auto", min_rssi_enabled: false },
  ],
};

const PLUG = {
  _id: "d3",
  mac: "aa:bb:cc:00:00:03",
  name: "Modem plug",
  type: "usp",
  outlet_table: [{ index: 1, name: "Outlet 1", relay_state: true, cycle_enabled: false }],
  outlet_overrides: [],
};

describe("switching and device tools", () => {
  let mock: MockController;
  let stop: () => Promise<void>;
  let mcp: Client;
  let readOnly: Client;
  let unifi: UnifiClient;

  before(async () => {
    const m = await startMock();
    mock = m.mock;
    stop = m.stop;
    unifi = m.client;
    for (const dev of [SWITCH, AP, PLUG]) {
      mock.on("GET", new RegExp(`^/api/s/default/stat/device/${dev.mac}$`), () => ok([structuredClone(dev)]));
    }
    mock.on("PUT", /^\/api\/s\/default\/rest\/device\/([^/]+)$/, (body, match) => ok([{ _id: match[1], ...body }]));
    mock.on("GET", /^\/api\/s\/default\/stat\/spectrum-scan\/aa:bb:cc:00:00:02$/, () =>
      ok([{ spectrum_scanning: false, spectrum_scan_last_ts: 1700000000, spectrum_table: [{ channel: 36, utilization: 12 }] }]),
    );
    mock.on("GET", /^\/api\/self\/sites$/, () =>
      ok([
        { _id: "0123456789abcdef01234567", name: "default", desc: "Default" },
        { _id: "89abcdef0123456789abcdef", name: "branch", desc: "Branch office" },
      ]),
    );
    mock.on("POST", /^\/api\/s\/default\/cmd\/firmware$/, () => ok([{ version: "7.1.66", device: "US8P60" }]));
    mock.seed("rest", "portconf", [
      { _id: "pc-all", name: "All", forward: "all" },
      { _id: "pc-iot", name: "IoT", native_networkconf_id: "n-iot" },
    ]);
    mock.seed("v2", "apgroups", [{ _id: "ag1", name: "All APs", device_macs: ["aa:bb:cc:00:00:02"], attr_no_delete: true }]);
    mcp = await connect(m.client, toolOptions(true, true));
    readOnly = await connect(m.client, toolOptions(false));
  });

  after(async () => {
    await mcp.close();
    await readOnly.close();
    await stop();
  });

  let mark = 0;
  beforeEach(() => {
    mark = mock.requests.length;
  });
  const sent = () => mock.requests.slice(mark).filter((r) => r.method !== "GET");

  it("registers write/delete tools only when allowed", async () => {
    const names = (await readOnly.listTools()).tools.map((t) => t.name);
    assert.ok(names.includes("unifi_list_switch_ports"));
    assert.ok(names.includes("unifi_get_rf_scan"));
    assert.ok(!names.includes("unifi_set_switch_port"));
    assert.ok(!names.includes("unifi_forget_device"));
    const writeOnly = await connect(unifi, toolOptions(true));
    const writeNames = (await writeOnly.listTools()).tools.map((t) => t.name);
    await writeOnly.close();
    assert.ok(writeNames.includes("unifi_set_switch_port"));
    assert.ok(!writeNames.includes("unifi_delete_port_profile"));
    assert.ok(!writeNames.includes("unifi_forget_device"));
  });

  it("lists switch ports with profile names and overrides", async () => {
    const res = parse(await mcp.callTool({ name: "unifi_list_switch_ports", arguments: { mac: "aa:bb:cc:00:00:01" } }));
    assert.equal(res.ports.length, 3);
    assert.equal(res.ports[0].profile_name, "All");
    assert.equal(res.ports[1].profile_name, "IoT");
    assert.deepEqual(res.ports[1].override, { name: "Printer", portconf_id: "pc-iot" });
  });

  it("adds a port override while preserving existing ones", async () => {
    parse(
      await mcp.callTool({
        name: "unifi_set_switch_port",
        arguments: { mac: "aa:bb:cc:00:00:01", port_idx: 1, name: "Desk", poe_mode: "off", speed: 100, tagged_vlan_mgmt: "block_all" },
      }),
    );
    const [req] = sent();
    assert.equal(req.method, "PUT");
    assert.match(req.path, /\/rest\/device\/d1$/);
    assert.deepEqual(req.body, {
      port_overrides: [
        { port_idx: 1, portconf_id: "pc-all", name: "Desk", tagged_vlan_mgmt: "block_all", poe_mode: "off", autoneg: false, speed: 100, full_duplex: true },
        { port_idx: 2, name: "Printer", portconf_id: "pc-iot" },
      ],
    });
  });

  it("merges into an existing override and disables via port security", async () => {
    parse(await mcp.callTool({ name: "unifi_set_switch_port", arguments: { mac: "aa:bb:cc:00:00:01", port_idx: 2, enabled: false } }));
    assert.deepEqual(sent()[0].body.port_overrides, [
      { port_idx: 2, name: "Printer", portconf_id: "pc-iot", port_security_enabled: true, port_security_mac_address: [] },
    ]);
  });

  it("rejects unknown ports and empty changes", async () => {
    const bad: any = await mcp.callTool({ name: "unifi_set_switch_port", arguments: { mac: "aa:bb:cc:00:00:01", port_idx: 9, name: "x" } });
    assert.equal(bad.isError, true);
    assert.match(bad.content[0].text, /valid ports: 1, 2, 3/);
    const empty: any = await mcp.callTool({ name: "unifi_set_switch_port", arguments: { mac: "aa:bb:cc:00:00:01", port_idx: 1 } });
    assert.equal(empty.isError, true);
    assert.equal(sent().length, 0);
  });

  it("resets port overrides", async () => {
    const res = parse(await mcp.callTool({ name: "unifi_reset_switch_port", arguments: { mac: "aa:bb:cc:00:00:01", port_idx: [2, 3] } }));
    assert.deepEqual(res.reset, [2]);
    assert.deepEqual(res.already_default, [3]);
    assert.deepEqual(sent()[0].body, { port_overrides: [] });
  });

  it("creates, updates and deletes port profiles", async () => {
    const created = parse(
      await mcp.callTool({ name: "unifi_save_port_profile", arguments: { name: "Cameras", native_network_id: "n-cam", poe_mode: "auto", isolation: true } }),
    );
    assert.equal(created.name, "Cameras");
    assert.deepEqual(sent()[0].body, { name: "Cameras", native_networkconf_id: "n-cam", poe_mode: "auto", isolation: true });

    mark = mock.requests.length;
    const updated = parse(await mcp.callTool({ name: "unifi_save_port_profile", arguments: { id: created.id, speed: "auto" } }));
    assert.equal(updated.native_network_id, "n-cam");
    const put = sent()[0];
    assert.equal(put.method, "PUT");
    assert.equal(put.body.autoneg, true);
    assert.equal(put.body.name, "Cameras");

    mark = mock.requests.length;
    parse(await mcp.callTool({ name: "unifi_delete_port_profile", arguments: { id: created.id } }));
    assert.equal(sent()[0].method, "DELETE");
    assert.ok(!mock.store.get("rest/portconf")!.some((p) => p._id === created.id));
  });

  it("updates device LED, name, IP and disabled state", async () => {
    parse(
      await mcp.callTool({
        name: "unifi_update_device",
        arguments: {
          mac: "aa:bb:cc:00:00:01",
          name: "Core",
          led: "off",
          disabled: false,
          ip_config: { type: "static", ip: "10.0.0.2", netmask: "255.255.255.0", gateway: "10.0.0.1", dns1: "1.1.1.1" },
        },
      }),
    );
    const [req] = sent();
    assert.match(req.path, /\/rest\/device\/d1$/);
    assert.deepEqual(req.body, {
      name: "Core",
      led_override: "off",
      disabled: false,
      config_network: { type: "static", ip: "10.0.0.2", netmask: "255.255.255.0", gateway: "10.0.0.1", dns1: "1.1.1.1" },
    });

    mark = mock.requests.length;
    const bad: any = await mcp.callTool({ name: "unifi_update_device", arguments: { mac: "aa:bb:cc:00:00:01", ip_config: { type: "static", ip: "10.0.0.9" } } });
    assert.equal(bad.isError, true);
    assert.equal(sent().length, 0);
  });

  it("changes one AP radio and keeps the others", async () => {
    const res = parse(
      await mcp.callTool({ name: "unifi_set_radio", arguments: { mac: "aa:bb:cc:00:00:02", radio: "na", channel: 44, channel_width: 40, tx_power: 14, min_rssi: -75 } }),
    );
    assert.deepEqual(res.radio, { radio: "na", channel: 44, channel_width: 40, tx_power_mode: "custom", tx_power: 14, min_rssi: -75 });
    assert.deepEqual(sent()[0].body.radio_table, [
      { radio: "ng", channel: 6, ht: 20, tx_power_mode: "auto" },
      { radio: "na", channel: 44, ht: 40, tx_power_mode: "custom", tx_power: 14, min_rssi_enabled: true, min_rssi: -75 },
    ]);

    mark = mock.requests.length;
    const missing: any = await mcp.callTool({ name: "unifi_set_radio", arguments: { mac: "aa:bb:cc:00:00:02", radio: "6e", channel: 5 } });
    assert.equal(missing.isError, true);
    assert.match(missing.content[0].text, /radios: ng, na/);
  });

  it("controls SmartPower outlets", async () => {
    parse(await mcp.callTool({ name: "unifi_set_outlet", arguments: { mac: "aa:bb:cc:00:00:03", index: 1, on: false } }));
    assert.deepEqual(sent()[0].body, {
      outlet_overrides: [{ index: 1, name: "Outlet 1", relay_state: false, cycle_enabled: false }],
    });
  });

  it("sends device commands with the expected payloads", async () => {
    const calls: [string, Record<string, unknown>][] = [
      ["unifi_upgrade_device", { mac: "aa:bb:cc:00:00:01", firmware_url: "https://fw.example/us8.bin" }],
      ["unifi_rolling_upgrade", { action: "start", types: ["uap"] }],
      ["unifi_rolling_upgrade", { action: "cancel" }],
      ["unifi_move_device", { mac: "AA:BB:CC:00:00:02", target_site: "branch" }],
      ["unifi_forget_device", { mac: "aa:bb:cc:00:00:02" }],
      ["unifi_run_rf_scan", { mac: "aa:bb:cc:00:00:02" }],
    ];
    for (const [name, args] of calls) parse(await mcp.callTool({ name, arguments: args }));
    const reqs = sent().map((r) => [r.path.replace("/proxy/network/api/s/default/", ""), r.body]);
    assert.deepEqual(reqs, [
      ["cmd/devmgr/upgrade-external", { url: "https://fw.example/us8.bin", macs: ["aa:bb:cc:00:00:01"] }],
      ["cmd/devmgr/set-rollupgrade", ["uap"]],
      ["cmd/devmgr", { cmd: "unset-rollupgrade" }],
      ["cmd/sitemgr", { cmd: "move-device", mac: "aa:bb:cc:00:00:02", site: "89abcdef0123456789abcdef" }],
      ["cmd/sitemgr", { cmd: "delete-device", mac: "aa:bb:cc:00:00:02" }],
      ["cmd/devmgr", { cmd: "spectrum-scan", mac: "aa:bb:cc:00:00:02" }],
    ]);
  });

  it("reports RF scan results and firmware status", async () => {
    const scan = parse(await mcp.callTool({ name: "unifi_get_rf_scan", arguments: { mac: "aa:bb:cc:00:00:02" } }));
    assert.equal(scan.scanning, false);
    assert.deepEqual(scan.results, [{ channel: 36, utilization: 12 }]);

    const fw = parse(await mcp.callTool({ name: "unifi_get_firmware_status", arguments: { include_available: true, refresh: true } }));
    assert.equal(fw.devices.length, 2);
    assert.deepEqual(fw.available, [{ version: "7.1.66", device: "US8P60" }]);
    assert.deepEqual(
      sent().map((r) => r.body),
      [{ cmd: "check-firmware-update" }, { cmd: "list-available" }],
    );
  });

  it("manages AP groups", async () => {
    const list = parse(await mcp.callTool({ name: "unifi_list_ap_groups", arguments: {} }));
    assert.deepEqual(list[0], { id: "ag1", name: "All APs", device_macs: ["aa:bb:cc:00:00:02"], device_count: 1, default: true });

    const created = parse(await mcp.callTool({ name: "unifi_save_ap_group", arguments: { name: "Upstairs", device_macs: ["AA-BB-CC-00-00-02"] } }));
    assert.deepEqual(sent()[0].body, { name: "Upstairs", device_macs: ["aa:bb:cc:00:00:02"] });

    mark = mock.requests.length;
    const renamed = parse(await mcp.callTool({ name: "unifi_save_ap_group", arguments: { id: created.id, name: "First floor" } }));
    assert.equal(renamed.name, "First floor");
    assert.deepEqual(renamed.device_macs, ["aa:bb:cc:00:00:02"]);
    assert.equal(sent()[0].method, "PUT");

    mark = mock.requests.length;
    parse(await mcp.callTool({ name: "unifi_delete_ap_group", arguments: { id: created.id } }));
    assert.equal(sent()[0].method, "DELETE");
    assert.equal(mock.store.get("v2/apgroups")!.length, 1);
  });
});
