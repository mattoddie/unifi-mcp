import { Agent, fetch, type Response } from "undici";
import type { UnifiConfig } from "../config.js";

export class UnifiError extends Error {
  constructor(
    message: string,
    readonly status?: number,
  ) {
    super(message);
    this.name = "UnifiError";
  }
}

export type HttpMethod = "GET" | "POST" | "PUT" | "DELETE" | "PATCH";

interface RequestOptions {
  body?: unknown;
  query?: Record<string, string | number | boolean | undefined>;
}

/**
 * Minimal client for the UniFi Network application.
 *
 * Supports UniFi OS consoles (UDM, UCG, UDR, Cloud Key Gen2+, UniFi OS Server),
 * where the Network app lives under /proxy/network, and legacy self-hosted
 * Network controllers, where it is served from the root. Authenticates with an
 * API key (UniFi OS only) or a local username/password session.
 */
export class UnifiClient {
  private readonly dispatcher: Agent;
  private unifiOs?: boolean;
  private cookies = new Map<string, string>();
  private csrfToken?: string;
  private loginPromise?: Promise<void>;

  constructor(private readonly cfg: UnifiConfig) {
    this.dispatcher = new Agent({
      connect: { rejectUnauthorized: cfg.verifySsl },
      headersTimeout: cfg.timeoutMs,
      bodyTimeout: cfg.timeoutMs,
    });
    if (cfg.apiKey || cfg.controllerType === "unifi-os") this.unifiOs = true;
    else if (cfg.controllerType === "legacy") this.unifiOs = false;
  }

  get defaultSite(): string {
    return this.cfg.site;
  }

  async close(): Promise<void> {
    await this.dispatcher.close();
  }

  /** GET a site-scoped classic API endpoint, e.g. siteGet("default", "stat/device"). */
  siteGet<T = any>(site: string, path: string, query?: RequestOptions["query"]): Promise<T> {
    return this.request<T>("GET", `/api/s/${encodeURIComponent(site)}/${path}`, { query });
  }

  sitePost<T = any>(site: string, path: string, body?: unknown): Promise<T> {
    return this.request<T>("POST", `/api/s/${encodeURIComponent(site)}/${path}`, { body: body ?? {} });
  }

  sitePut<T = any>(site: string, path: string, body: unknown): Promise<T> {
    return this.request<T>("PUT", `/api/s/${encodeURIComponent(site)}/${path}`, { body });
  }

  /** Issue a command to one of the classic API managers (stamgr, devmgr, hotspot, evtmgr...). */
  cmd<T = any>(site: string, manager: string, cmd: string, params: Record<string, unknown> = {}): Promise<T> {
    return this.sitePost<T>(site, `cmd/${manager}`, { cmd, ...params });
  }

  /** Request against the v2 API (/v2/api/site/<site>/...), used by newer features. */
  v2<T = any>(method: HttpMethod, site: string, path: string, body?: unknown): Promise<T> {
    return this.request<T>(method, `/v2/api/site/${encodeURIComponent(site)}/${path}`, { body });
  }

  /**
   * Low-level request. `path` is relative to the Network application root
   * (e.g. /api/s/default/stat/health, /v2/api/site/default/trafficrules,
   * /integration/v1/sites); the /proxy/network prefix is added on UniFi OS.
   * Classic API envelopes ({meta, data}) are unwrapped to `data`.
   */
  async request<T = any>(method: HttpMethod, path: string, opts: RequestOptions = {}): Promise<T> {
    if (!path.startsWith("/")) path = `/${path}`;
    await this.ensureReady();

    let res = await this.send(method, this.networkPath(path), opts);
    if ((res.status === 401 || res.status === 403) && !this.cfg.apiKey) {
      // Session expired: log in again and retry once.
      await res.body?.cancel();
      this.cookies.clear();
      this.csrfToken = undefined;
      await this.login();
      res = await this.send(method, this.networkPath(path), opts);
    }
    return this.parse<T>(res, method, path);
  }

  private networkPath(path: string): string {
    return this.unifiOs ? `/proxy/network${path}` : path;
  }

  private async ensureReady(): Promise<void> {
    if (this.unifiOs === undefined) this.unifiOs = await this.detectUnifiOs();
    if (!this.cfg.apiKey && this.cookies.size === 0) await this.login();
  }

  /** UniFi OS answers 200 on "/", legacy controllers redirect to /manage. */
  private async detectUnifiOs(): Promise<boolean> {
    try {
      const res = await fetch(`${this.cfg.url}/`, {
        method: "GET",
        redirect: "manual",
        dispatcher: this.dispatcher,
        signal: AbortSignal.timeout(this.cfg.timeoutMs),
      });
      await res.body?.cancel();
      return res.status === 200;
    } catch (err) {
      throw this.networkError(err);
    }
  }

  private login(): Promise<void> {
    // Collapse concurrent logins into one.
    this.loginPromise ??= this.doLogin().finally(() => {
      this.loginPromise = undefined;
    });
    return this.loginPromise;
  }

  private async doLogin(): Promise<void> {
    const path = this.unifiOs ? "/api/auth/login" : "/api/login";
    const res = await this.send("POST", path, {
      body: { username: this.cfg.username, password: this.cfg.password, remember: true },
    });
    const text = await res.text();
    if (!res.ok) {
      let detail = text.slice(0, 300);
      if (res.status === 499 || /2fa|mfa|ubic_2fa/i.test(text)) {
        detail = "the account requires multi-factor authentication; use a local-only admin account or an API key";
      }
      throw new UnifiError(`Login to UniFi controller failed (HTTP ${res.status}): ${detail}`, res.status);
    }
    if (this.cookies.size === 0) {
      throw new UnifiError("Login succeeded but the controller did not return a session cookie");
    }
  }

  private async send(method: HttpMethod, path: string, opts: RequestOptions): Promise<Response> {
    const url = new URL(this.cfg.url + path);
    for (const [k, v] of Object.entries(opts.query ?? {})) {
      if (v !== undefined) url.searchParams.set(k, String(v));
    }

    const headers: Record<string, string> = { Accept: "application/json" };
    if (opts.body !== undefined) headers["Content-Type"] = "application/json";
    if (this.cfg.apiKey) {
      headers["X-API-KEY"] = this.cfg.apiKey;
    } else {
      if (this.cookies.size) headers.Cookie = [...this.cookies].map(([k, v]) => `${k}=${v}`).join("; ");
      if (this.csrfToken) headers["X-CSRF-Token"] = this.csrfToken;
    }

    let res: Response;
    try {
      res = await fetch(url, {
        method,
        headers,
        body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
        redirect: "manual",
        dispatcher: this.dispatcher,
        signal: AbortSignal.timeout(this.cfg.timeoutMs),
      });
    } catch (err) {
      throw this.networkError(err);
    }
    this.captureSession(res);
    return res;
  }

  private captureSession(res: Response): void {
    for (const cookie of res.headers.getSetCookie()) {
      const [pair] = cookie.split(";");
      const idx = pair.indexOf("=");
      if (idx <= 0) continue;
      const name = pair.slice(0, idx).trim();
      const value = pair.slice(idx + 1).trim();
      if (value === "" || /max-age=0|expires=thu, 01 jan 1970/i.test(cookie)) this.cookies.delete(name);
      else this.cookies.set(name, value);
      if (name === "csrf_token" && value) this.csrfToken = value;
    }
    const csrf = res.headers.get("x-updated-csrf-token") ?? res.headers.get("x-csrf-token");
    if (csrf) this.csrfToken = csrf;
  }

  private async parse<T>(res: Response, method: string, path: string): Promise<T> {
    const text = await res.text();
    let json: any;
    if (text) {
      try {
        json = JSON.parse(text);
      } catch {
        json = undefined;
      }
    }

    const apiMsg: string | undefined = json?.meta?.msg ?? json?.message ?? json?.error?.message ?? json?.error;
    if (!res.ok) {
      if (res.status >= 300 && res.status < 400) {
        throw new UnifiError(
          `${method} ${path} was redirected (HTTP ${res.status}) to ${res.headers.get("location") ?? "?"}; check UNIFI_URL and UNIFI_CONTROLLER_TYPE`,
          res.status,
        );
      }
      const detail = typeof apiMsg === "string" ? apiMsg : text.slice(0, 300);
      const hint =
        res.status === 401 || res.status === 403
          ? " (check credentials and that the account/API key has sufficient permissions)"
          : res.status === 404
            ? " (endpoint not found; it may not exist on this Network application version)"
            : "";
      throw new UnifiError(`${method} ${path} failed with HTTP ${res.status}: ${detail}${hint}`, res.status);
    }
    if (json === undefined) {
      if (!text) return undefined as T;
      throw new UnifiError(`${method} ${path} returned a non-JSON response: ${text.slice(0, 200)}`, res.status);
    }
    if (json?.meta?.rc === "error") {
      throw new UnifiError(`${method} ${path} failed: ${json.meta.msg ?? "unknown error"}`, res.status);
    }
    if (json && typeof json === "object" && "meta" in json && "data" in json) return json.data as T;
    return json as T;
  }

  private networkError(err: unknown): UnifiError {
    const e = err as Error & { cause?: { code?: string; message?: string } };
    if (e.name === "TimeoutError" || e.name === "AbortError") {
      return new UnifiError(`Timed out after ${this.cfg.timeoutMs}ms talking to ${this.cfg.url}`);
    }
    const cause = e.cause?.code ?? e.cause?.message ?? e.message;
    const tlsHint = /CERT|SELF_SIGNED|UNABLE_TO_VERIFY/i.test(String(cause))
      ? " (the controller uses a self-signed certificate; set UNIFI_VERIFY_SSL=false)"
      : "";
    return new UnifiError(`Could not reach UniFi controller at ${this.cfg.url}: ${cause}${tlsHint}`);
  }
}
