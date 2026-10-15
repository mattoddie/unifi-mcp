import assert from "node:assert/strict";
import { X509Certificate } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import type { Server } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, it } from "node:test";
import { insecureTlsWarning, loadConfig } from "../config.js";
import { startHttpServer } from "../http.js";
import { createServer } from "../server.js";
import { assertSafePath, normalizeFingerprint, pathSegment, UnifiClient } from "../unifi/client.js";
import { redactSecrets } from "../unifi/format.js";
import { connect, parse, startMock, toolOptions } from "./helpers.js";
import { MockController } from "./mock-controller.js";
import { TEST_TLS_CERT, TEST_TLS_KEY } from "./tls-fixture.js";

const base = { UNIFI_URL: "https://10.0.0.1", UNIFI_API_KEY: "k" };

describe("API path validation", () => {
  const traversals = [
    "/%2E%2E/%2e%2e/api/system",
    "/.%2e/api/system",
    "/%2e./api/system",
    "/api/s/default/../../../api/system",
    "/api/./s/default/stat/device",
    "/api/s/%252e%252e/stat/device",
    "/api/s/default/stat/device/..",
    "/api\\..\\system",
    "/api/s/default/stat/device\x00",
    "/api/s/default/stat/device%0a",
    "/api/s/default/stat/device?x=1",
    "/api/s/default/stat/device#x",
    "/api/s/%zz/stat",
    "api/s/default",
  ];

  it("rejects dot segments, encoded dots, backslashes, control characters, ? and #", () => {
    for (const p of traversals) assert.throws(() => assertSafePath(p), /Invalid API path/, p);
    assert.doesNotThrow(() => assertSafePath("/api/s/default/stat/device/aa:bb:cc:00:00:01"));
    assert.doesNotThrow(() => assertSafePath("/api/s/default/stat/report/5minutes.site"));
    assert.doesNotThrow(() => assertSafePath("/v2/api/site/default/trafficrules/my%20rule"));
  });

  it("refuses '.' and '..' as site or ID segments", () => {
    assert.throws(() => pathSegment(".."), /Invalid path segment/);
    assert.throws(() => pathSegment("."), /Invalid path segment/);
    assert.throws(() => pathSegment(""), /Invalid path segment/);
    assert.equal(pathSegment("a/b"), "a%2Fb");
  });

  describe("against the mock controller", () => {
    let mock: MockController;
    let client: UnifiClient;
    let stop: () => Promise<void>;
    before(async () => {
      ({ mock, client, stop } = await startMock());
      mock.on("GET", /^\/api\/system$/, () => [200, { leaked: true }]);
    });
    after(() => stop());

    it("never sends a traversal request to the controller", async () => {
      const before = mock.requests.length;
      for (const p of traversals.filter((p) => p.startsWith("/"))) {
        await assert.rejects(client.request("GET", p), /Invalid API path/, p);
      }
      await assert.rejects(client.siteGet("..", "stat/device"), /Invalid site/);
      await assert.rejects(client.v2("GET", ".", "trafficrules"), /Invalid site/);
      // Anything the URL parser would rewrite is refused rather than sent somewhere unexpected.
      await assert.rejects(client.request("GET", "/api/s/default/stat/my device"), /Refusing to send .* resolves to/);
      assert.equal(mock.requests.length, before);
    });

    it("rejects traversal through the raw tool and site arguments", async () => {
      const mcp = await connect(client, toolOptions(true, true));
      const before = mock.requests.length;
      for (const path of ["/%2E%2E/%2e%2e/api/system", "/.%2e/api/system", "/api/../api/system"]) {
        const res: any = await mcp.callTool({ name: "unifi_api_request", arguments: { path } });
        assert.equal(res.isError, true, path);
        assert.match(res.content[0].text, /Invalid API path/);
      }
      const health: any = await mcp.callTool({ name: "unifi_get_site_health", arguments: { site: ".." } });
      assert.equal(health.isError, true);
      assert.match(health.content[0].text, /Invalid site/);
      const wlan: any = await mcp.callTool({ name: "unifi_save_wlan", arguments: { id: "..", enabled: false } });
      assert.equal(wlan.isError, true);
      assert.match(wlan.content[0].text, /Invalid path segment/);
      assert.equal(mock.requests.length, before);
      assert.ok(mock.requests.every((r) => !r.path.startsWith("/api/system")));

      // Normal raw requests still work.
      const sites = parse(await mcp.callTool({ name: "unifi_api_request", arguments: { path: "/api/self/sites" } }));
      assert.equal(sites[0].name, "default");
      await mcp.close();
    });
  });
});

describe("raw tool delete gating", () => {
  let mock: MockController;
  let client: UnifiClient;
  let stop: () => Promise<void>;
  before(async () => {
    ({ mock, client, stop } = await startMock());
  });
  after(() => stop());

  const raw = (method: string, path: string, body?: unknown) => ({ name: "unifi_api_request", arguments: { method, path, body } });

  it("refuses delete-like commands when deletes are disabled", async () => {
    const mcp = await connect(client, toolOptions(true, false));
    const before = mock.requests.length;
    for (const [path, body] of [
      ["/api/s/default/cmd/sitemgr", { cmd: "delete-site", site: "x" }],
      ["/api/s/default/cmd/sitemgr", { cmd: "delete-device", mac: "aa:bb:cc:00:00:01" }],
      ["/api/s/default/cmd/sitemgr", { cmd: "revoke-admin", admin: "a1" }],
      ["/api/s/default/cmd/stamgr", { cmd: "forget-sta", macs: ["11:22:33:44:55:66"] }],
      ["/api/s/default/cmd/backup", { cmd: "DELETE-BACKUP", filename: "x.unf" }],
      ["/api/s/default/cmd/stat", { cmd: "reset-dpi" }],
      ["/api/s/default/cmd/hotspot", [{ cmd: "delete-voucher", _id: "v1" }]],
      ["/v2/api/site/default/firewall-policies/batch-delete", ["p1"]],
    ] as const) {
      const res: any = await mcp.callTool(raw("POST", path, body));
      assert.equal(res.isError, true, JSON.stringify(body));
      assert.match(res.content[0].text, /Delete access is disabled/);
    }
    assert.equal(mock.requests.length, before);

    parse(await mcp.callTool(raw("POST", "/api/s/default/cmd/devmgr", { cmd: "restart", mac: "aa:bb:cc:00:00:01" })));
    assert.deepEqual(mock.requests.at(-1)!.body, { cmd: "restart", mac: "aa:bb:cc:00:00:01" });
    const { tools } = await mcp.listTools();
    assert.match(tools.find((t) => t.name === "unifi_api_request")!.description!, /forget, revoke/);
    await mcp.close();
  });

  it("allows them when deletes are enabled", async () => {
    const mcp = await connect(client, toolOptions(true, true));
    parse(await mcp.callTool(raw("POST", "/api/s/default/cmd/stamgr", { cmd: "forget-sta", macs: ["11:22:33:44:55:66"] })));
    assert.equal(mock.requests.at(-1)!.body.cmd, "forget-sta");
    await mcp.close();
  });

  it("treats forgetting clients and revoking vouchers as deletes", async () => {
    const writes = await connect(client, toolOptions(true, false));
    const names = (await writes.listTools()).tools.map((t) => t.name);
    assert.ok(!names.includes("unifi_forget_clients"));
    assert.ok(!names.includes("unifi_revoke_voucher"));
    await writes.close();

    const deletes = await connect(client, toolOptions(true, true));
    const tools = (await deletes.listTools()).tools;
    for (const name of ["unifi_forget_clients", "unifi_revoke_voucher"]) {
      assert.equal(tools.find((t) => t.name === name)?.annotations?.destructiveHint, true, name);
    }
    await deletes.close();
  });
});

describe("firmware refresh gating", () => {
  it("needs writes for unifi_get_firmware_status refresh", async () => {
    const { mock, client, stop } = await startMock();
    const mcp = await connect(client, toolOptions(false));
    const before = mock.requests.length;
    const res: any = await mcp.callTool({ name: "unifi_get_firmware_status", arguments: { refresh: true } });
    assert.equal(res.isError, true);
    assert.match(res.content[0].text, /requires UNIFI_ALLOW_WRITES/);
    assert.ok(mock.requests.slice(before).every((r) => r.method === "GET"));
    const ok = parse(await mcp.callTool({ name: "unifi_get_firmware_status", arguments: {} }));
    assert.equal(ok.devices.length, 2);
    await mcp.close();
    await stop();
  });
});

describe("secret redaction", () => {
  it("redacts known secret fields without x_ prefix and keeps identifiers", () => {
    const out = redactSecrets({
      key: "mgmt",
      name: "Office",
      zone_key: "internal",
      community: "public-ro",
      x_passphrase: "hunter22",
      passphrase_autogenerated: true,
      radius_secret: "s3",
      secret: "s4",
      private_key: "wg-private",
      privatekey: "wg-private-2",
      api_key: "abc",
      auth_token: "tok",
      configuration_file: "client\nremote vpn.example",
      openvpn_config_file: "x",
      ssh_authorized_tokens: ["a", "b"],
      private_preshared_keys: [{ networkconf_id: "n1", password: "ppsk-1" }],
      nested: { pppoe: { username: "isp", password: "pw" }, psk: "abc" },
      empty_password: "",
    }) as any;
    assert.equal(out.key, "mgmt");
    assert.equal(out.name, "Office");
    assert.equal(out.zone_key, "internal");
    assert.equal(out.passphrase_autogenerated, true);
    assert.equal(out.empty_password, "");
    for (const k of ["community", "x_passphrase", "radius_secret", "secret", "private_key", "privatekey", "api_key", "auth_token", "configuration_file", "openvpn_config_file"]) {
      assert.equal(out[k], "[redacted]", k);
    }
    assert.deepEqual(out.ssh_authorized_tokens, ["[redacted]", "[redacted]"]);
    assert.deepEqual(out.private_preshared_keys, [{ networkconf_id: "n1", password: "[redacted]" }]);
    assert.deepEqual(out.nested, { pppoe: { username: "isp", password: "[redacted]" }, psk: "[redacted]" });
  });
});

describe("UnifiClient session handling", () => {
  let mock: MockController;
  let client: UnifiClient;
  before(async () => {
    mock = new MockController({ mode: "unifi-os" });
    await mock.start();
    client = new UnifiClient(loadConfig({ UNIFI_URL: mock.url, UNIFI_USERNAME: "admin", UNIFI_PASSWORD: "secret" }).unifi);
    mock.delay(/^\/api\/s\/default\/stat\/sta$/, 250);
  });
  after(async () => {
    await client.close();
    await mock.stop();
  });

  it("logs in once for many parallel requests", async () => {
    await Promise.all(Array.from({ length: 8 }, () => client.siteGet("default", "stat/device")));
    assert.equal(mock.loginCount, 1);
    mock.sessions.clear();
    const results = await Promise.all(Array.from({ length: 8 }, () => client.siteGet("default", "stat/device")));
    assert.ok(results.every((r) => r.length === 2));
    assert.equal(mock.loginCount, 2);
  });

  it("does not discard a fresh session when a stale request fails late", async () => {
    const logins = mock.loginCount;
    mock.sessions.clear();
    // Both requests go out with the expired session. The fast one re-logs in; the
    // slow one's 401 arrives afterwards and must reuse the new session.
    const [devices, clients] = await Promise.all([client.siteGet("default", "stat/device"), client.siteGet("default", "stat/sta")]);
    assert.equal(devices.length, 2);
    assert.equal(clients.length, 2);
    assert.equal(mock.loginCount, logins + 1);
    // The fresh session is still in use.
    await client.siteGet("default", "stat/device");
    assert.equal(mock.loginCount, logins + 1);
  });
});

describe("HTTP transport security", () => {
  it("requires MCP_AUTH_TOKEN for http unless explicitly allowed", () => {
    assert.throws(
      () => loadConfig({ ...base, MCP_TRANSPORT: "http" }),
      (err: Error) => {
        assert.equal(
          err.message,
          "MCP_AUTH_TOKEN is required for the http transport (generate one with: openssl rand -hex 32); set MCP_ALLOW_UNAUTHENTICATED=true only behind something else that authenticates",
        );
        return true;
      },
    );
    assert.equal(loadConfig({ ...base, MCP_TRANSPORT: "http", MCP_ALLOW_UNAUTHENTICATED: "true" }).http.allowUnauthenticated, true);
    assert.doesNotThrow(() => loadConfig(base)); // stdio needs no token
  });

  it("listens on localhost by default and lower-cases the host allow-list", () => {
    const cfg = loadConfig({ ...base, MCP_TRANSPORT: "http", MCP_AUTH_TOKEN: "t", MCP_ALLOWED_HOSTS: "MCP.Example.LAN, Other:8080" });
    assert.equal(cfg.http.host, "127.0.0.1");
    assert.deepEqual(cfg.http.allowedHosts, ["mcp.example.lan", "other:8080"]);
  });

  describe("server", () => {
    const mock = new MockController({ mode: "unifi-os", apiKey: "key123" });
    let unifi: UnifiClient;
    const servers: Server[] = [];
    const start = async (extra: Record<string, string>): Promise<string> => {
      const cfg = loadConfig({ UNIFI_URL: mock.url, UNIFI_API_KEY: "key123", MCP_TRANSPORT: "http", ...extra });
      cfg.http.port = 0;
      const http = await startHttpServer(cfg, () => createServer(unifi, toolOptions(false)));
      servers.push(http);
      return `http://127.0.0.1:${(http.address() as AddressInfo).port}`;
    };
    const list = (url: string, headers: Record<string, string> = {}) =>
      fetch(`${url}/mcp`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "application/json, text/event-stream", ...headers },
        body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }),
      });

    before(async () => {
      await mock.start();
      unifi = new UnifiClient(loadConfig({ UNIFI_URL: mock.url, UNIFI_API_KEY: "key123" }).unifi);
    });
    after(async () => {
      for (const s of servers) s.close();
      await unifi.close();
      await mock.stop();
    });

    it("rejects disallowed Origin headers when MCP_ALLOWED_HOSTS is set", async () => {
      const url = await start({ MCP_AUTH_TOKEN: "s3cret", MCP_ALLOWED_HOSTS: "127.0.0.1" });
      const auth = { Authorization: "Bearer s3cret" };
      assert.equal((await list(url, auth)).status, 200);
      assert.equal((await list(url, { ...auth, Origin: url })).status, 200);
      assert.equal((await list(url, { ...auth, Origin: "http://127.0.0.1" })).status, 200);
      for (const origin of ["http://evil.example", "null", "file://", "http://127.0.0.1.evil.example"]) {
        const res = await list(url, { ...auth, Origin: origin });
        assert.equal(res.status, 403, origin);
        assert.match(await res.text(), /Origin .* is not allowed/);
      }
    });

    it("serves without a token only when MCP_ALLOW_UNAUTHENTICATED=true", async () => {
      const url = await start({ MCP_ALLOW_UNAUTHENTICATED: "true" });
      assert.equal((await list(url)).status, 200);
    });
  });
});

describe("TLS to the controller", () => {
  const mock = new MockController({ mode: "unifi-os", apiKey: "key123", tls: { cert: TEST_TLS_CERT, key: TEST_TLS_KEY } });
  const fingerprint = new X509Certificate(TEST_TLS_CERT).fingerprint256;
  const wrongPin = "00".repeat(32);
  let dir: string;
  let caFile: string;

  before(async () => {
    await mock.start();
    dir = mkdtempSync(join(tmpdir(), "unifi-mcp-tls-"));
    caFile = join(dir, "ca.pem");
    writeFileSync(caFile, TEST_TLS_CERT);
  });
  after(async () => {
    await mock.stop();
    rmSync(dir, { recursive: true, force: true });
  });

  const sites = async (extra: Record<string, string>) => {
    const client = new UnifiClient(loadConfig({ UNIFI_URL: mock.url, UNIFI_API_KEY: "key123", ...extra }).unifi);
    try {
      return await client.request("GET", "/api/self/sites");
    } finally {
      await client.close();
    }
  };

  it("parses and validates fingerprints and CA settings", () => {
    const colon = normalizeFingerprint(fingerprint.toLowerCase());
    assert.equal(colon, fingerprint);
    assert.equal(normalizeFingerprint(fingerprint.replaceAll(":", "")), fingerprint);
    assert.equal(normalizeFingerprint(`sha256:${fingerprint}`), fingerprint);
    assert.throws(() => loadConfig({ ...base, UNIFI_TLS_FINGERPRINT: "abc" }), /UNIFI_TLS_FINGERPRINT: .*not a SHA-256/);
    const cfg = loadConfig({ ...base, UNIFI_TLS_FINGERPRINT: `${fingerprint.replaceAll(":", "").toLowerCase()}, ${wrongPin}` });
    assert.deepEqual(cfg.unifi.tlsFingerprints, [fingerprint, normalizeFingerprint(wrongPin)]);
    assert.throws(() => loadConfig({ ...base, UNIFI_CA_FILE: join(dir, "missing.pem") }), /Could not read UNIFI_CA_FILE/);
    assert.equal(loadConfig({ ...base, UNIFI_CA_FILE: caFile }).unifi.verifySsl, true);
    assert.throws(() => loadConfig({ ...base, UNIFI_CA_FILE: caFile, UNIFI_VERIFY_SSL: "false" }), /needs certificate verification/);
  });

  it("warns at startup only when verification is off without a pin", () => {
    assert.match(insecureTlsWarning(loadConfig(base).unifi)!, /UNIFI_TLS_FINGERPRINT/);
    assert.equal(insecureTlsWarning(loadConfig({ ...base, UNIFI_TLS_FINGERPRINT: fingerprint }).unifi), undefined);
    assert.equal(insecureTlsWarning(loadConfig({ ...base, UNIFI_VERIFY_SSL: "true" }).unifi), undefined);
    assert.equal(insecureTlsWarning(loadConfig({ ...base, UNIFI_URL: "http://10.0.0.1" }).unifi), undefined);
  });

  it("accepts the self-signed certificate by default", async () => {
    assert.equal((await sites({}))[0].name, "default");
  });

  it("accepts a matching pin and refuses a mismatched one before sending anything", async () => {
    assert.equal((await sites({ UNIFI_TLS_FINGERPRINT: fingerprint }))[0].name, "default");
    assert.equal((await sites({ UNIFI_TLS_FINGERPRINT: `${wrongPin},${fingerprint}` }))[0].name, "default");
    const before = mock.requests.length;
    await assert.rejects(sites({ UNIFI_TLS_FINGERPRINT: wrongPin }), (err: Error) => {
      assert.match(err.message, /fingerprint mismatch/);
      assert.ok(err.message.includes(fingerprint), "reports the presented fingerprint");
      return true;
    });
    // A pin also applies with verification on.
    await assert.rejects(sites({ UNIFI_TLS_FINGERPRINT: wrongPin, UNIFI_CA_FILE: caFile }), /fingerprint mismatch/);
    assert.equal(mock.requests.length, before);
  });

  it("suggests pinning when verification fails", async () => {
    await assert.rejects(sites({ UNIFI_VERIFY_SSL: "true" }), (err: Error) => {
      assert.match(err.message, /pin it with UNIFI_TLS_FINGERPRINT/);
      assert.doesNotMatch(err.message, /set UNIFI_VERIFY_SSL=false/);
      return true;
    });
    // With UNIFI_VERIFY_SSL=true the chain must still verify; a pin alone does not replace a trusted CA.
    await assert.rejects(sites({ UNIFI_VERIFY_SSL: "true", UNIFI_TLS_FINGERPRINT: fingerprint }), /pin it with UNIFI_TLS_FINGERPRINT/);
  });

  it("trusts a custom CA from UNIFI_CA_FILE", async () => {
    assert.equal((await sites({ UNIFI_CA_FILE: caFile }))[0].name, "default");
    assert.equal((await sites({ UNIFI_CA_FILE: caFile, UNIFI_TLS_FINGERPRINT: fingerprint }))[0].name, "default");
  });
});
