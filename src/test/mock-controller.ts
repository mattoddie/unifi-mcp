import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";

export interface RecordedRequest {
  method: string;
  path: string;
  headers: IncomingMessage["headers"];
  body: any;
}

export type MockHandler = (body: any, match: RegExpMatchArray, query: URLSearchParams) => [number, unknown];

export interface MockOptions {
  mode: "unifi-os" | "legacy";
  apiKey?: string;
}

/** A tiny fake UniFi controller covering the endpoints the tests exercise. */
export class MockController {
  requests: RecordedRequest[] = [];
  /** Session tokens the server will accept; clear it to simulate expiry. */
  sessions = new Set<string>();
  loginCount = 0;
  /**
   * In-memory objects for the generic REST fallbacks, keyed by "rest/<collection>"
   * or "v2/<collection>" (site "default"). Seed with seed().
   */
  store = new Map<string, any[]>();
  private handlers: { method: string; pattern: RegExp; fn: MockHandler }[] = [];
  private nextId = 1;
  private server!: Server;
  url = "";

  constructor(private readonly opts: MockOptions) {
    this.seed("rest", "wlanconf", [{ _id: "w1", name: "Home", enabled: true, security: "wpapsk", x_passphrase: "hunter22" }]);
    this.seed("v2", "trafficrules", [{ _id: "t1", description: "Block games", enabled: true }]);
  }

  async start(): Promise<void> {
    this.server = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", () => {
        const text = Buffer.concat(chunks).toString();
        const body = text ? JSON.parse(text) : undefined;
        const path = req.url ?? "/";
        this.requests.push({ method: req.method ?? "GET", path, headers: req.headers, body });
        const [status, payload, headers] = this.route(req, path, body);
        res.writeHead(status, { "Content-Type": "application/json", ...headers });
        res.end(payload === undefined ? "" : JSON.stringify(payload));
      });
    });
    await new Promise<void>((r) => this.server.listen(0, "127.0.0.1", r));
    this.url = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
  }

  /**
   * Register a custom route. `pattern` is matched against the path relative to the
   * Network application root without the query string, e.g. /^\/api\/s\/default\/stat\/ips\/event$/.
   * Custom routes take precedence over built-in ones.
   */
  on(method: string, pattern: RegExp, fn: MockHandler): this {
    this.handlers.push({ method, pattern, fn });
    return this;
  }

  /** Seed the generic store, e.g. seed("rest", "firewallrule", [{ _id: "r1", name: "x" }]). */
  seed(kind: "rest" | "v2", collection: string, items: any[]): this {
    this.store.set(`${kind}/${collection}`, structuredClone(items));
    return this;
  }

  /** Requests whose path (relative to the Network application) starts with `prefix`. */
  requestsTo(prefix: string): RecordedRequest[] {
    const strip = this.opts.mode === "unifi-os" ? "/proxy/network" : "";
    return this.requests.filter((r) => r.path.startsWith(strip + prefix));
  }

  async stop(): Promise<void> {
    await new Promise((r) => this.server.close(r));
  }

  private authorized(req: IncomingMessage): boolean {
    if (this.opts.apiKey) return req.headers["x-api-key"] === this.opts.apiKey;
    const cookie = req.headers.cookie ?? "";
    const token = cookie.match(/(?:TOKEN|unifises)=([^;]+)/)?.[1];
    if (!token || !this.sessions.has(token)) return false;
    if (req.method !== "GET" && req.headers["x-csrf-token"] !== `csrf-${token}`) return false;
    return true;
  }

  private route(req: IncomingMessage, path: string, body: any): [number, unknown, Record<string, string | string[]>?] {
    const os = this.opts.mode === "unifi-os";
    if (path === "/") return os ? [200, undefined] : [302, undefined, { Location: "/manage" }];

    const loginPath = os ? "/api/auth/login" : "/api/login";
    if (path === loginPath && req.method === "POST") {
      if (body?.username !== "admin" || body?.password !== "secret") return [401, { meta: { rc: "error", msg: "api.err.Invalid" } }];
      const token = `tok${++this.loginCount}`;
      this.sessions.add(token);
      return os
        ? [200, { username: "admin" }, { "Set-Cookie": `TOKEN=${token}; path=/; HttpOnly`, "X-CSRF-Token": `csrf-${token}` }]
        : [200, { meta: { rc: "ok" }, data: [] }, { "Set-Cookie": [`unifises=${token}; path=/`, `csrf_token=csrf-${token}; path=/`] }];
    }

    const prefix = os ? "/proxy/network" : "";
    if (!path.startsWith(`${prefix}/`)) return [404, { error: "not found" }];
    if (!this.authorized(req)) return [401, { meta: { rc: "error", msg: "api.err.LoginRequired" } }];
    const p = path.slice(prefix.length);
    const ok = (data: unknown): [number, unknown] => [200, { meta: { rc: "ok" }, data }];
    const [pathOnly, qs] = p.split("?");
    for (const h of this.handlers) {
      const m = pathOnly.match(h.pattern);
      if (m && h.method === req.method) return h.fn(body, m, new URLSearchParams(qs ?? ""));
    }

    switch (`${req.method} ${p.split("?")[0]}`) {
      case "GET /api/self/sites":
        return ok([{ _id: "s1", name: "default", desc: "Default", role: "admin" }]);
      case "GET /api/s/default/stat/device":
        return ok(DEVICES);
      case "GET /api/s/default/stat/device/aa:bb:cc:00:00:01":
        return ok([DEVICES[0]]);
      case "GET /api/s/default/stat/sta":
        return ok(CLIENTS);
      case "GET /api/s/default/stat/user/11:22:33:44:55:66":
        return ok([{ _id: "u1", mac: "11:22:33:44:55:66", network_id: "n1", hostname: "laptop" }]);
      case "PUT /api/s/default/rest/user/u1":
        return ok([{ _id: "u1", mac: "11:22:33:44:55:66", hostname: "laptop", ...body }]);
      case "GET /api/s/missing/stat/health":
        return [400, { meta: { rc: "error", msg: "api.err.NoSiteContext" }, data: [] }];
    }
    return this.generic(req.method ?? "GET", pathOnly, body) ?? [404, { error: `no mock for ${req.method} ${p}` }];
  }

  /**
   * CRUD over the in-memory store for /api/s/default/rest/<collection> and
   * /v2/api/site/default/<collection> (collections may contain slashes, e.g.
   * "firewall/zone"), plus an ok response for any cmd/<manager> POST.
   * rest/setting is addressed as rest/setting/<key>/<id>.
   */
  private generic(method: string, p: string, body: any): [number, unknown] | undefined {
    if (method === "POST" && /^\/api\/s\/default\/cmd\//.test(p)) return [200, { meta: { rc: "ok" }, data: [] }];

    let kind: "rest" | "v2";
    let rest: string;
    if (p.startsWith("/api/s/default/rest/")) [kind, rest] = ["rest", p.slice("/api/s/default/rest/".length)];
    else if (p.startsWith("/v2/api/site/default/")) [kind, rest] = ["v2", p.slice("/v2/api/site/default/".length)];
    else return undefined;

    const coll = [...this.store.keys()]
      .filter((k) => k.startsWith(`${kind}/`))
      .map((k) => k.slice(kind.length + 1))
      .filter((c) => rest === c || rest.startsWith(`${c}/`))
      .sort((a, b) => b.length - a.length)[0];
    if (!coll) return undefined;
    const items = this.store.get(`${kind}/${coll}`)!;
    let segs = rest.slice(coll.length).split("/").filter(Boolean).map(decodeURIComponent);
    let settingKey: string | undefined;
    if (kind === "rest" && coll === "setting") [settingKey, ...segs] = segs;
    const id = segs[0];

    const wrap = (data: any): [number, unknown] =>
      kind === "rest" ? [200, { meta: { rc: "ok" }, data: Array.isArray(data) ? data : [data] }] : [200, data];
    const notFound: [number, unknown] =
      kind === "rest" ? [400, { meta: { rc: "error", msg: "api.err.IdInvalid" }, data: [] }] : [404, { message: "not found" }];
    const idx = id ? items.findIndex((o) => o._id === id) : -1;

    switch (method) {
      case "GET":
        if (!id) return wrap(settingKey ? items.filter((o) => o.key === settingKey) : items);
        return idx >= 0 ? wrap(items[idx]) : notFound;
      case "POST": {
        const obj = { ...body, _id: (this.nextId++).toString(16).padStart(24, "0") };
        items.push(obj);
        return wrap(obj);
      }
      case "PUT":
      case "PATCH":
        if (idx < 0) return notFound;
        items[idx] = { ...items[idx], ...body, _id: items[idx]._id };
        return wrap(items[idx]);
      case "DELETE":
        if (idx < 0) return notFound;
        items.splice(idx, 1);
        return kind === "rest" ? [200, { meta: { rc: "ok" }, data: [] }] : [200, {}];
    }
    return undefined;
  }
}

export const DEVICES = [
  {
    _id: "d1",
    mac: "aa:bb:cc:00:00:01",
    name: "Office Switch",
    ip: "10.0.0.2",
    type: "usw",
    model: "US8P60",
    version: "7.1.26",
    state: 1,
    adopted: true,
    uptime: 90061,
    num_sta: 4,
    "system-stats": { cpu: "5.1", mem: "40.2" },
    x_authkey: "very-secret",
    port_table: [{ port_idx: 1, name: "Port 1", up: true, enable: true, speed: 1000, full_duplex: true, poe_mode: "auto" }],
  },
  { _id: "d2", mac: "aa:bb:cc:00:00:02", name: "Lounge AP", type: "uap", model: "U6LR", state: 0, version: "6.6.77" },
];

export const CLIENTS = [
  { mac: "11:22:33:44:55:66", hostname: "laptop", ip: "10.0.0.50", is_wired: false, essid: "Home", signal: -55, tx_rate: 866000, uptime: 3600 },
  { mac: "11:22:33:44:55:77", name: "NAS", ip: "10.0.0.10", is_wired: true, sw_mac: "aa:bb:cc:00:00:01", sw_port: 3 },
];
