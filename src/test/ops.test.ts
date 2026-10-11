import assert from "node:assert/strict";
import { after, before, describe, it } from "node:test";
import type { Client } from "@modelcontextprotocol/sdk/client/index.js";
import type { UnifiClient } from "../unifi/client.js";
import { connect, parse, startMock, toolOptions } from "./helpers.js";
import type { MockController } from "./mock-controller.js";

const ok = (data: unknown): [number, unknown] => [200, { meta: { rc: "ok" }, data }];

describe("security, admin, monitoring and hotspot tools", () => {
  let mock: MockController;
  let client: UnifiClient;
  let stop: () => Promise<void>;
  let mcp: Client;

  before(async () => {
    ({ mock, client, stop } = await startMock());
    mock
      .seed("rest", "setting", [
        { _id: "s_ips", key: "ips", ips_mode: "ids", enabled_categories: ["emerging-malware"], memory_optimized: false },
        { _id: "s_dpi", key: "dpi", enabled: true, fingerprintingEnabled: true },
        { _id: "s_usg", key: "usg", geo_ip_filtering_enabled: false, upnp_enabled: false },
        { _id: "s_mgmt", key: "mgmt", led_enabled: true, x_ssh_password: "sshpw" },
        { _id: "s_ga", key: "guest_access", auth: "none", portal_enabled: false, x_password: "guestpw" },
      ])
      .seed("rest", "hotspotop", [{ _id: "op1", name: "Front desk", x_password: "oppw" }])
      .seed("v2", "content-filtering", [{ _id: "cf1", name: "Kids", enabled: true, blocked_categories: ["ADULT"] }])
      .on("POST", /^\/api\/s\/default\/stat\/ips\/event$/, () =>
        ok([
          { timestamp: 1_700_000_000_000, inner_alert_signature: "ET SCAN", inner_alert_category: "scan", src_ip: "1.2.3.4", dest_ip: "10.0.0.2", inner_alert_action: "blocked" },
          { timestamp: 1_700_000_100_000, inner_alert_signature: "ET MALWARE", src_ip: "5.6.7.8" },
        ]),
      )
      .on("POST", /^\/api\/s\/default\/stat\/event$/, () => [404, { error: "gone" }])
      .on("POST", /^\/v2\/api\/site\/default\/system-log\/([a-z-]+)$/, (body, m) => [
        200,
        {
          data: [{ timestamp: 1_700_000_000_000 + (m[1] === "client-alert" ? 1 : 0), key: `${m[1]}-key`, message: `from ${m[1]}`, echo: body }],
          totalElementCount: 1,
        },
      ])
      .on("POST", /^\/api\/s\/default\/stat\/stadpi$/, (body) => ok([{ mac: body.macs[0], by_app: [] }]))
      .on("POST", /^\/api\/s\/default\/stat\/session$/, () =>
        ok([{ mac: "11:22:33:44:55:66", assoc_time: 1_700_000_000, disassoc_time: 1_700_003_600, duration: 3600 }]),
      )
      .on("POST", /^\/api\/s\/default\/cmd\/sitemgr$/, (body) => {
        if (body.cmd === "get-admins") return ok([{ _id: "a1", name: "Matt", email: "m@example.com", role: "admin", is_super: true }]);
        if (body.cmd === "add-site") return ok([{ _id: "site2id", name: "abc123", desc: body.desc }]);
        return ok([]);
      })
      .on("POST", /^\/api\/s\/abc123\/cmd\/sitemgr$/, () => ok([]))
      .on("GET", /^\/api\/self\/sites$/, () =>
        ok([
          { _id: "s1", name: "default", desc: "Default" },
          { _id: "site2id", name: "abc123", desc: "Branch" },
        ]),
      )
      .on("POST", /^\/api\/s\/default\/cmd\/backup$/, (body) =>
        body.cmd === "list-backups"
          ? ok([{ filename: "autobackup_9.0.unf", time: 1_700_000_000_000, size: 2097152, version: "9.0.114" }])
          : ok([{ url: "/dl/backup/9.0.unf" }]),
      )
      .on("GET", /^\/api\/s\/default\/stat\/fwupdate\/latest-version$/, () => ok([{ version: "9.1.0", update_available: true }]));
    mcp = await connect(client, toolOptions(true, true));
  });

  after(async () => {
    await mcp.close();
    await stop();
  });

  const call = async (name: string, args: Record<string, unknown> = {}) => parse(await mcp.callTool({ name, arguments: args }));
  const last = (prefix: string) => mock.requestsTo(prefix).at(-1)!;

  it("reads security settings", async () => {
    const res = await call("unifi_get_security_settings");
    assert.equal(res.threat_management.ips_mode, "ids");
    assert.deepEqual(res.traffic_identification, { enabled: true, device_fingerprinting: true });
    assert.deepEqual(res.country_blocking, { enabled: false });
  });

  it("updates threat management via the ips and dpi settings", async () => {
    const res = await call("unifi_update_threat_management", { ips_mode: "ips", dpi_enabled: false });
    assert.equal(res.threat_management.ips_mode, "ips");
    assert.equal(res.traffic_identification.enabled, false);
    const ipsPut = mock.requestsTo("/api/s/default/rest/setting/ips/s_ips").find((r) => r.method === "PUT")!;
    assert.equal(ipsPut.body.ips_mode, "ips");
    assert.deepEqual(ipsPut.body.enabled_categories, ["emerging-malware"], "unchanged fields are kept");
  });

  it("updates country blocking", async () => {
    const res = await call("unifi_update_country_blocking", { enabled: true, mode: "block", countries: ["cn", "ru"] });
    assert.deepEqual(res, { enabled: true, block: "block", countries: "CN,RU" });
    assert.equal(last("/api/s/default/rest/setting/usg").body.upnp_enabled, false);
  });

  it("lists threats newest first with a millisecond window", async () => {
    const res = await call("unifi_list_threats", { hours: 1 });
    assert.equal(res[0].signature, "ET MALWARE");
    assert.equal(res[1].action, "blocked");
    const body = last("/api/s/default/stat/ips/event").body;
    assert.ok(body.end - body.start === 3600_000);
  });

  it("manages content filters", async () => {
    assert.equal((await call("unifi_list_content_filters"))[0].name, "Kids");
    await call("unifi_save_content_filter", { id: "cf1", enabled: false });
    assert.deepEqual(last("/v2/api/site/default/content-filtering/cf1").body.blocked_categories, ["ADULT"]);
    const created = await call("unifi_save_content_filter", { name: "Office", safe_search: ["GOOGLE"] });
    assert.equal(created.name, "Office");
    assert.equal(last("/v2/api/site/default/content-filtering/create").body.schedule.mode, "ALWAYS");
    await call("unifi_delete_content_filter", { id: "cf1" });
    assert.equal(last("/v2/api/site/default/content-filtering/cf1").method, "DELETE");
  });

  it("lists setting keys and redacts secrets", async () => {
    const keys = await call("unifi_get_site_settings");
    assert.ok(keys.keys.includes("mgmt"));
    const mgmt = await call("unifi_get_site_settings", { key: "mgmt" });
    assert.equal(mgmt.x_ssh_password, "[redacted]");
    const withSecrets = await call("unifi_get_site_settings", { key: "mgmt", include_secrets: true });
    assert.equal(withSecrets.x_ssh_password, "sshpw");
  });

  it("updates a site setting by key", async () => {
    const res = await call("unifi_update_site_setting", { key: "mgmt", changes: { led_enabled: false } });
    assert.equal(res.led_enabled, false);
    assert.equal(res.x_ssh_password, "[redacted]");
    const put = last("/api/s/default/rest/setting/mgmt/s_mgmt");
    assert.equal(put.method, "PUT");
    assert.equal(put.body.x_ssh_password, "sshpw", "secrets are preserved on write");
    const bad: any = await mcp.callTool({ name: "unifi_update_site_setting", arguments: { key: "mgmt", changes: { _id: "x" } } });
    assert.equal(bad.isError, true);
  });

  it("creates, renames and deletes sites", async () => {
    const created = await call("unifi_create_site", { description: "Branch" });
    assert.deepEqual(created, { id: "site2id", name: "abc123", description: "Branch" });
    await call("unifi_rename_site", { site: "abc123", description: "Branch office" });
    assert.deepEqual(last("/api/s/abc123/cmd/sitemgr").body, { cmd: "update-site", desc: "Branch office" });
    await call("unifi_delete_site", { site_name: "abc123" });
    assert.deepEqual(last("/api/s/default/cmd/sitemgr").body, { cmd: "delete-site", site: "site2id" });
    const res: any = await mcp.callTool({ name: "unifi_delete_site", arguments: { site_name: "default" } });
    assert.equal(res.isError, true);
  });

  it("lists and invites admins", async () => {
    const admins = await call("unifi_list_admins");
    assert.deepEqual(admins[0], { id: "a1", name: "Matt", email: "m@example.com", role: "admin", super_admin: true });
    await call("unifi_invite_admin", { name: "Sam", email: "sam@example.com", role: "readonly", allow_restart: true });
    assert.deepEqual(last("/api/s/default/cmd/sitemgr").body, {
      cmd: "invite-admin",
      name: "Sam",
      email: "sam@example.com",
      for_sso: true,
      role: "readonly",
      permissions: ["API_DEVICE_RESTART"],
    });
  });

  it("lists, creates and deletes backups", async () => {
    const list = await call("unifi_list_backups");
    assert.deepEqual(list[0], { filename: "autobackup_9.0.unf", time: "2023-11-14T22:13:20.000Z", version: "9.0.114", size_mb: 2 });
    const created = await call("unifi_create_backup", { days: 0 });
    assert.equal(created.download_path, "/dl/backup/9.0.unf");
    assert.deepEqual(last("/api/s/default/cmd/backup").body, { cmd: "backup", days: 0 });
    await call("unifi_delete_backup", { filename: "autobackup_9.0.unf" });
    assert.deepEqual(last("/api/s/default/cmd/backup").body, { cmd: "delete-backup", filename: "autobackup_9.0.unf" });
  });

  it("checks for updates", async () => {
    const res = await call("unifi_check_updates", { refresh: true });
    assert.equal(res.controller.version, "9.1.0");
    assert.equal(res.devices_up_to_date, 2);
    assert.deepEqual(last("/api/s/default/cmd/productinfo").body, { cmd: "check-firmware-update" });
  });

  it("falls back to the v2 system log when the classic event log is unavailable", async () => {
    const res = await call("unifi_list_events", { hours: 2 });
    assert.match(res.source, /system-log/);
    assert.equal(res.events.length, 2);
    assert.equal(res.events[0].key, "client-alert-key");
  });

  it("reads the system log with class-specific filters", async () => {
    const res = await call("unifi_get_system_log", { class: "threat-alert", page_size: 10 });
    assert.equal(res.total, 1);
    const body = last("/v2/api/site/default/system-log/threat-alert").body;
    assert.deepEqual(body.threatTypes, ["HONEYPOT", "THREAT"]);
    assert.equal(body.pageSize, 10);
  });

  it("gets per-client DPI and sessions", async () => {
    const dpi = await call("unifi_get_dpi_stats", { by: "app", macs: ["11-22-33-44-55-66"] });
    assert.equal(dpi[0].mac, "11:22:33:44:55:66");
    assert.deepEqual(last("/api/s/default/stat/stadpi").body, { type: "by_app", macs: ["11:22:33:44:55:66"] });
    const sessions = await call("unifi_list_client_sessions", { mac: "11:22:33:44:55:66", hours: 1 });
    assert.equal(sessions[0].duration, "1h 0m");
    const body = last("/api/s/default/stat/session").body;
    assert.equal(body.end - body.start, 3600, "session window is in seconds");
  });

  it("manages hotspot operators and guest portal settings", async () => {
    const ops = await call("unifi_list_hotspot_operators");
    assert.deepEqual(ops, [{ id: "op1", name: "Front desk" }]);
    const created = await call("unifi_save_hotspot_operator", { name: "Night", password: "pw123" });
    assert.equal(created.name, "Night");
    assert.equal(last("/api/s/default/rest/hotspotop").body.x_password, "pw123");
    const portal = await call("unifi_get_guest_portal_settings");
    assert.equal(portal.x_password, "[redacted]");
    const updated = await call("unifi_update_guest_portal_settings", { portal_enabled: true, auth: "password", session_minutes: 480 });
    assert.equal(updated.auth, "password");
    assert.equal(updated.expire, 480);
    await call("unifi_delete_hotspot_operator", { id: "op1" });
    assert.equal(last("/api/s/default/rest/hotspotop/op1").method, "DELETE");
  });

  it("gates delete and write tools", async () => {
    const writesOnly = await connect(client, toolOptions(true, false));
    const names = (await writesOnly.listTools()).tools.map((t) => t.name);
    assert.ok(names.includes("unifi_update_site_setting"));
    for (const t of ["unifi_delete_site", "unifi_delete_backup", "unifi_revoke_admin", "unifi_delete_content_filter", "unifi_delete_hotspot_operator"]) {
      assert.ok(!names.includes(t), `${t} should need UNIFI_ALLOW_DELETES`);
    }
    await writesOnly.close();

    const readOnly = await connect(client, toolOptions(false));
    const ro = (await readOnly.listTools()).tools.map((t) => t.name);
    assert.ok(ro.includes("unifi_get_security_settings") && ro.includes("unifi_get_system_log"));
    assert.ok(!ro.includes("unifi_update_threat_management") && !ro.includes("unifi_create_backup"));
    const refresh: any = await readOnly.callTool({ name: "unifi_check_updates", arguments: { refresh: true } });
    assert.match(refresh.content[0].text, /requires UNIFI_ALLOW_WRITES/);
    await readOnly.close();
  });

  it("exposes prompts", async () => {
    const { prompts } = await mcp.listPrompts();
    const names = prompts.map((p) => p.name);
    for (const n of ["network_health_check", "troubleshoot_client", "security_review", "firmware_review", "wifi_optimization"]) {
      assert.ok(names.includes(n), n);
    }
    const p = await mcp.getPrompt({ name: "troubleshoot_client", arguments: { client: "laptop", site: "branch" } });
    const text = (p.messages[0].content as { text: string }).text;
    assert.match(text, /"laptop"/);
    assert.match(text, /site "branch"/);
    assert.match(text, /unifi_get_client/);
  });
});
