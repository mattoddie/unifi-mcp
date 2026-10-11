import assert from "node:assert/strict";
import type { Server } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, describe, it } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { loadConfig, TOOLSETS, type Config } from "../config.js";
import { startHttpServer } from "../http.js";
import { createServer } from "../server.js";
import { UnifiClient, UnifiError } from "../unifi/client.js";
import { connect, parse, toolOptions } from "./helpers.js";
import { MockController } from "./mock-controller.js";

function cfgFor(url: string, extra: Record<string, string> = {}): Config {
  return loadConfig({ UNIFI_URL: url, UNIFI_USERNAME: "admin", UNIFI_PASSWORD: "secret", ...extra });
}

describe("config", () => {
  it("requires a URL and credentials", () => {
    assert.throws(() => loadConfig({}), /UNIFI_URL is required/);
    assert.throws(() => loadConfig({ UNIFI_URL: "https://10.0.0.1" }), /UNIFI_API_KEY or both/);
  });

  it("applies defaults", () => {
    const cfg = loadConfig({ UNIFI_URL: "https://10.0.0.1/", UNIFI_API_KEY: "k" });
    assert.equal(cfg.unifi.url, "https://10.0.0.1");
    assert.equal(cfg.unifi.site, "default");
    assert.equal(cfg.unifi.verifySsl, false);
    assert.equal(cfg.tools.allowWrites, false);
    assert.equal(cfg.tools.allowDeletes, false);
    assert.equal(cfg.tools.toolsets.size, TOOLSETS.length);
    assert.equal(cfg.transport, "stdio");
    assert.equal(cfg.http.port, 8080);
  });

  it("parses delete gating and toolsets", () => {
    const base = { UNIFI_URL: "https://x", UNIFI_API_KEY: "k" };
    assert.throws(() => loadConfig({ ...base, UNIFI_ALLOW_DELETES: "true" }), /also requires UNIFI_ALLOW_WRITES/);
    const cfg = loadConfig({ ...base, UNIFI_ALLOW_WRITES: "true", UNIFI_ALLOW_DELETES: "true", UNIFI_TOOLSETS: "overview, clients" });
    assert.equal(cfg.tools.allowDeletes, true);
    assert.deepEqual([...cfg.tools.toolsets].sort(), ["clients", "overview"]);
    assert.throws(() => loadConfig({ ...base, UNIFI_TOOLSETS: "overview,bogus" }), /Unknown toolset "bogus"/);
  });

  it("rejects API keys for legacy controllers", () => {
    assert.throws(
      () => loadConfig({ UNIFI_URL: "https://x", UNIFI_API_KEY: "k", UNIFI_CONTROLLER_TYPE: "legacy" }),
      /only supported by UniFi OS/,
    );
  });
});

describe("UnifiClient on UniFi OS with username/password", () => {
  const mock = new MockController({ mode: "unifi-os" });
  let client: UnifiClient;
  before(async () => {
    await mock.start();
    client = new UnifiClient(cfgFor(mock.url).unifi);
  });
  after(async () => {
    await client.close();
    await mock.stop();
  });

  it("detects UniFi OS, logs in and prefixes /proxy/network", async () => {
    const sites = await client.request("GET", "/api/self/sites");
    assert.equal(sites[0].name, "default");
    assert.ok(mock.requests.some((r) => r.path === "/api/auth/login"));
    assert.ok(mock.requests.some((r) => r.path === "/proxy/network/api/self/sites"));
  });

  it("sends the CSRF token on writes", async () => {
    await client.cmd("default", "stamgr", "block-sta", { mac: "11:22:33:44:55:66" });
    const req = mock.requests.at(-1)!;
    assert.equal(req.headers["x-csrf-token"], "csrf-tok1");
    assert.deepEqual(req.body, { cmd: "block-sta", mac: "11:22:33:44:55:66" });
  });

  it("re-authenticates once when the session expires", async () => {
    mock.sessions.clear();
    const devices = await client.siteGet("default", "stat/device");
    assert.equal(devices.length, 2);
    assert.equal(mock.loginCount, 2);
  });

  it("surfaces controller error messages", async () => {
    await assert.rejects(client.siteGet("missing", "stat/health"), (err: UnifiError) => {
      assert.match(err.message, /api\.err\.NoSiteContext/);
      return true;
    });
  });

  it("returns unwrapped v2 responses", async () => {
    const rules = await client.v2("GET", "default", "trafficrules");
    assert.equal(rules[0]._id, "t1");
  });
});

describe("UnifiClient on a legacy controller", () => {
  const mock = new MockController({ mode: "legacy" });
  let client: UnifiClient;
  before(async () => {
    await mock.start();
    client = new UnifiClient(cfgFor(mock.url).unifi);
  });
  after(async () => {
    await client.close();
    await mock.stop();
  });

  it("logs in via /api/login without the /proxy/network prefix and uses the csrf cookie", async () => {
    await client.cmd("default", "devmgr", "restart", { mac: "aa:bb:cc:00:00:01" });
    assert.ok(mock.requests.some((r) => r.path === "/api/login"));
    const req = mock.requests.at(-1)!;
    assert.equal(req.path, "/api/s/default/cmd/devmgr");
    assert.equal(req.headers["x-csrf-token"], "csrf-tok1");
  });

  it("reports bad credentials clearly", async () => {
    const bad = new UnifiClient(cfgFor(mock.url, { UNIFI_PASSWORD: "wrong" }).unifi);
    await assert.rejects(bad.siteGet("default", "stat/device"), /Login to UniFi controller failed \(HTTP 401\)/);
    await bad.close();
  });
});

describe("MCP tools", () => {
  const mock = new MockController({ mode: "unifi-os", apiKey: "key123" });
  let client: UnifiClient;
  before(async () => {
    await mock.start();
    client = new UnifiClient(loadConfig({ UNIFI_URL: mock.url, UNIFI_API_KEY: "key123" }).unifi);
  });
  after(async () => {
    await client.close();
    await mock.stop();
  });

  it("only exposes read tools when writes are disabled", async () => {
    const mcp = await connect(client, false);
    const { tools } = await mcp.listTools();
    const names = tools.map((t) => t.name);
    assert.ok(names.includes("unifi_list_devices"));
    assert.ok(!names.includes("unifi_block_client"));
    assert.ok(tools.every((t) => t.annotations?.readOnlyHint === true));
    await mcp.close();
  });

  it("uses the API key and summarises devices", async () => {
    const mcp = await connect(client, false);
    const res = parse(await mcp.callTool({ name: "unifi_list_devices", arguments: { type: "switch" } }));
    assert.equal(res.total, 1);
    assert.deepEqual(res.items[0], {
      id: "d1",
      name: "Office Switch",
      mac: "aa:bb:cc:00:00:01",
      ip: "10.0.0.2",
      model: "US8P60",
      model_code: "US8P60",
      type: "switch",
      state: "connected",
      adopted: true,
      firmware: "7.1.26",
      uptime: "1d 1h 1m",
      clients: 4,
      cpu_pct: 5.1,
      mem_pct: 40.2,
    });
    assert.equal(mock.requests.at(-1)!.headers["x-api-key"], "key123");
    await mcp.close();
  });

  it("redacts secrets in raw output", async () => {
    const mcp = await connect(client, false);
    const dev = parse(await mcp.callTool({ name: "unifi_get_device", arguments: { mac: "AA-BB-CC-00-00-01", raw: true } }));
    assert.equal(dev.x_authkey, "[redacted]");
    const wlans = parse(await mcp.callTool({ name: "unifi_list_wlans", arguments: {} }));
    assert.equal(wlans[0].passphrase, undefined);
    const withSecrets = parse(await mcp.callTool({ name: "unifi_list_wlans", arguments: { include_secrets: true } }));
    assert.equal(withSecrets[0].passphrase, "hunter22");
    await mcp.close();
  });

  it("filters clients", async () => {
    const mcp = await connect(client, false);
    const res = parse(await mcp.callTool({ name: "unifi_list_clients", arguments: { connection: "wired" } }));
    assert.equal(res.total, 1);
    assert.equal(res.items[0].name, "NAS");
    assert.equal(res.items[0].switch_port, 3);
    const search = parse(await mcp.callTool({ name: "unifi_list_clients", arguments: { search: "LAPTOP" } }));
    assert.equal(search.items[0].tx_rate_mbps, 866);
    await mcp.close();
  });

  it("blocks non-GET raw requests when writes are disabled", async () => {
    const mcp = await connect(client, false);
    const res: any = await mcp.callTool({
      name: "unifi_api_request",
      arguments: { method: "POST", path: "/api/s/default/cmd/devmgr", body: { cmd: "restart" } },
    });
    assert.equal(res.isError, true);
    assert.match(res.content[0].text, /Write access is disabled/);
    await mcp.close();
  });

  it("blocks raw DELETE requests when deletes are disabled", async () => {
    const mcp = await connect(client, true);
    const before = mock.requests.length;
    const res: any = await mcp.callTool({
      name: "unifi_api_request",
      arguments: { method: "DELETE", path: "/api/s/default/rest/wlanconf/w1" },
    });
    assert.equal(res.isError, true);
    assert.match(res.content[0].text, /Delete access is disabled/);
    assert.equal(mock.requests.length, before);
    await mcp.close();
  });

  it("only registers tools from enabled toolsets", async () => {
    const mcp = await connect(client, { allowWrites: true, allowDeletes: false, toolsets: new Set(["clients"] as const) });
    const names = (await mcp.listTools()).tools.map((t) => t.name);
    assert.ok(names.includes("unifi_list_clients"));
    assert.ok(names.includes("unifi_block_client"));
    assert.ok(!names.includes("unifi_list_devices"));
    assert.ok(names.every((n) => !n.startsWith("unifi_delete_")));
    await mcp.close();
  });

  it("registers delete tools only when deletes are enabled", async () => {
    const without = await connect(client, toolOptions(true, false));
    const withDeletes = await connect(client, toolOptions(true, true));
    const a = (await without.listTools()).tools.map((t) => t.name);
    const b = (await withDeletes.listTools()).tools;
    assert.ok(a.every((n) => !n.startsWith("unifi_delete_")));
    for (const t of b.filter((t) => t.name.startsWith("unifi_delete_"))) assert.equal(t.annotations?.destructiveHint, true, t.name);
    assert.ok(b.length >= a.length);
    await without.close();
    await withDeletes.close();
  });

  it("returns tool errors instead of throwing", async () => {
    const mcp = await connect(client, false);
    const res: any = await mcp.callTool({ name: "unifi_get_site_health", arguments: { site: "missing" } });
    assert.equal(res.isError, true);
    assert.match(res.content[0].text, /NoSiteContext/);
    await mcp.close();
  });

  it("exposes and runs write tools when enabled", async () => {
    const mcp = await connect(client, true);
    const { tools } = await mcp.listTools();
    const block = tools.find((t) => t.name === "unifi_block_client");
    assert.equal(block?.annotations?.destructiveHint, true);

    parse(await mcp.callTool({ name: "unifi_block_client", arguments: { mac: "11-22-33-44-55-66" } }));
    assert.deepEqual(mock.requests.at(-1)!.body, { cmd: "block-sta", mac: "11:22:33:44:55:66" });

    const updated = parse(
      await mcp.callTool({ name: "unifi_update_client", arguments: { mac: "11:22:33:44:55:66", name: "Work laptop", fixed_ip: "10.0.0.60" } }),
    );
    assert.deepEqual(mock.requests.at(-1)!.body, { name: "Work laptop", use_fixedip: true, fixed_ip: "10.0.0.60", network_id: "n1" });
    assert.equal(updated.name, "Work laptop");
    assert.equal(updated.fixed_ip, "10.0.0.60");

    parse(await mcp.callTool({ name: "unifi_power_cycle_port", arguments: { mac: "aa:bb:cc:00:00:01", port_idx: 4 } }));
    assert.deepEqual(mock.requests.at(-1)!.body, { cmd: "power-cycle", mac: "aa:bb:cc:00:00:01", port_idx: 4 });
    await mcp.close();
  });
});

describe("HTTP transport", () => {
  const mock = new MockController({ mode: "unifi-os", apiKey: "key123" });
  let http: Server;
  let unifi: UnifiClient;
  let base: string;
  before(async () => {
    await mock.start();
    const cfg = loadConfig({
      UNIFI_URL: mock.url,
      UNIFI_API_KEY: "key123",
      MCP_TRANSPORT: "http",
      MCP_HTTP_HOST: "127.0.0.1",
      MCP_HTTP_PORT: "1",
      MCP_AUTH_TOKEN: "s3cret",
    });
    cfg.http.port = 0;
    unifi = new UnifiClient(cfg.unifi);
    http = await startHttpServer(cfg, () => createServer(unifi, toolOptions(false)));
    base = `http://127.0.0.1:${(http.address() as AddressInfo).port}`;
  });
  after(async () => {
    http.close();
    await unifi.close();
    await mock.stop();
  });

  it("serves a health check without auth", async () => {
    const res = await fetch(`${base}/health`);
    assert.equal(res.status, 200);
  });

  it("rejects requests without the bearer token", async () => {
    const res = await fetch(`${base}/mcp`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json, text/event-stream" },
      body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }),
    });
    assert.equal(res.status, 401);
  });

  it("works end-to-end with the streamable HTTP client", async () => {
    const mcp = new Client({ name: "test", version: "0" });
    await mcp.connect(
      new StreamableHTTPClientTransport(new URL(`${base}/mcp`), {
        requestInit: { headers: { Authorization: "Bearer s3cret" } },
      }),
    );
    const { tools } = await mcp.listTools();
    assert.ok(tools.length > 20);
    const sites = parse(await mcp.callTool({ name: "unifi_list_sites", arguments: {} }));
    assert.equal(sites[0].name, "default");
    await mcp.close();
  });
});
