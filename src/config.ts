import { readFileSync } from "node:fs";
import { normalizeFingerprint } from "./unifi/client.js";

export type ControllerType = "auto" | "unifi-os" | "legacy";
export type TransportType = "stdio" | "http";

export interface UnifiConfig {
  url: string;
  apiKey?: string;
  username?: string;
  password?: string;
  site: string;
  verifySsl: boolean;
  /** PEM file with CA certificates to trust for the controller (UNIFI_CA_FILE). */
  caFile?: string;
  /** Accepted SHA-256 fingerprints of the controller's certificate (UNIFI_TLS_FINGERPRINT). */
  tlsFingerprints?: string[];
  controllerType: ControllerType;
  timeoutMs: number;
}

export const TOOLSETS = [
  "overview",
  "devices",
  "clients",
  "switching",
  "wifi",
  "networks",
  "firewall",
  "routing",
  "security",
  "vpn",
  "hotspot",
  "monitoring",
  "admin",
  "raw",
] as const;
export type Toolset = (typeof TOOLSETS)[number];

export interface ToolOptions {
  allowWrites: boolean;
  /** Tools that delete configuration objects; requires allowWrites as well. */
  allowDeletes: boolean;
  toolsets: ReadonlySet<Toolset>;
}

export interface Config {
  unifi: UnifiConfig;
  tools: ToolOptions;
  transport: TransportType;
  http: {
    host: string;
    port: number;
    authToken?: string;
    /** Serve /mcp without a bearer token (MCP_ALLOW_UNAUTHENTICATED). */
    allowUnauthenticated: boolean;
    allowedHosts?: string[];
  };
}

type Env = Record<string, string | undefined>;

/**
 * Reads a variable, also honouring the `<NAME>_FILE` convention so secrets can
 * be supplied as Docker secrets mounted under /run/secrets.
 */
function read(env: Env, name: string): string | undefined {
  const file = env[`${name}_FILE`];
  if (file) {
    try {
      return readFileSync(file, "utf8").trim() || undefined;
    } catch (err) {
      throw new Error(`Could not read ${name}_FILE (${file}): ${(err as Error).message}`);
    }
  }
  const value = env[name]?.trim();
  return value ? value : undefined;
}

function bool(value: string | undefined, fallback: boolean): boolean {
  if (value === undefined) return fallback;
  return ["1", "true", "yes", "on"].includes(value.toLowerCase());
}

function int(name: string, value: string | undefined, fallback: number): number {
  if (value === undefined) return fallback;
  const n = Number.parseInt(value, 10);
  if (!Number.isFinite(n) || n <= 0) throw new Error(`${name} must be a positive integer, got "${value}"`);
  return n;
}

export function loadConfig(env: Env = process.env): Config {
  const url = read(env, "UNIFI_URL");
  if (!url) throw new Error("UNIFI_URL is required (e.g. https://192.168.1.1)");
  try {
    new URL(url);
  } catch {
    throw new Error(`UNIFI_URL is not a valid URL: ${url}`);
  }

  const apiKey = read(env, "UNIFI_API_KEY");
  const username = read(env, "UNIFI_USERNAME");
  const password = read(env, "UNIFI_PASSWORD");
  if (!apiKey && !(username && password)) {
    throw new Error("Set either UNIFI_API_KEY or both UNIFI_USERNAME and UNIFI_PASSWORD");
  }

  const controllerType = (read(env, "UNIFI_CONTROLLER_TYPE") ?? "auto") as ControllerType;
  if (!["auto", "unifi-os", "legacy"].includes(controllerType)) {
    throw new Error(`UNIFI_CONTROLLER_TYPE must be auto, unifi-os or legacy, got "${controllerType}"`);
  }
  if (apiKey && controllerType === "legacy") {
    throw new Error("API keys are only supported by UniFi OS consoles; use UNIFI_USERNAME/UNIFI_PASSWORD for legacy controllers");
  }

  const transport = (read(env, "MCP_TRANSPORT") ?? "stdio") as TransportType;
  if (!["stdio", "http"].includes(transport)) {
    throw new Error(`MCP_TRANSPORT must be stdio or http, got "${transport}"`);
  }

  const allowWrites = bool(read(env, "UNIFI_ALLOW_WRITES"), false);
  const allowDeletes = bool(read(env, "UNIFI_ALLOW_DELETES"), false);
  if (allowDeletes && !allowWrites) {
    throw new Error("UNIFI_ALLOW_DELETES=true also requires UNIFI_ALLOW_WRITES=true");
  }

  const toolsetList = (read(env, "UNIFI_TOOLSETS") ?? "all")
    .split(",")
    .map((t) => t.trim().toLowerCase())
    .filter(Boolean);
  const toolsets = new Set<Toolset>();
  for (const t of toolsetList) {
    if (t === "all") TOOLSETS.forEach((x) => toolsets.add(x));
    else if ((TOOLSETS as readonly string[]).includes(t)) toolsets.add(t as Toolset);
    else throw new Error(`Unknown toolset "${t}" in UNIFI_TOOLSETS; valid: all, ${TOOLSETS.join(", ")}`);
  }

  const allowedHosts = read(env, "MCP_ALLOWED_HOSTS")
    ?.split(",")
    .map((h) => h.trim().toLowerCase())
    .filter(Boolean);

  const authToken = read(env, "MCP_AUTH_TOKEN");
  const allowUnauthenticated = bool(read(env, "MCP_ALLOW_UNAUTHENTICATED"), false);
  if (transport === "http" && !authToken && !allowUnauthenticated) {
    throw new Error(
      "MCP_AUTH_TOKEN is required for the http transport (generate one with: openssl rand -hex 32); set MCP_ALLOW_UNAUTHENTICATED=true only behind something else that authenticates",
    );
  }

  const caFile = read(env, "UNIFI_CA_FILE");
  if (caFile) {
    try {
      readFileSync(caFile);
    } catch (err) {
      throw new Error(`Could not read UNIFI_CA_FILE (${caFile}): ${(err as Error).message}`);
    }
  }
  const verifySslSetting = read(env, "UNIFI_VERIFY_SSL");
  // A custom CA only makes sense with verification on, so it switches it on by default.
  const verifySsl = bool(verifySslSetting, Boolean(caFile));
  if (caFile && !verifySsl) {
    throw new Error("UNIFI_CA_FILE needs certificate verification; remove UNIFI_VERIFY_SSL=false");
  }
  const tlsFingerprints = read(env, "UNIFI_TLS_FINGERPRINT")
    ?.split(",")
    .map((f) => f.trim())
    .filter(Boolean)
    .map((f) => {
      try {
        return normalizeFingerprint(f);
      } catch (err) {
        throw new Error(`UNIFI_TLS_FINGERPRINT: ${(err as Error).message}`);
      }
    });

  return {
    unifi: {
      url: url.replace(/\/+$/, ""),
      apiKey,
      username,
      password,
      site: read(env, "UNIFI_SITE") ?? "default",
      verifySsl,
      caFile,
      tlsFingerprints: tlsFingerprints?.length ? tlsFingerprints : undefined,
      controllerType,
      timeoutMs: int("UNIFI_TIMEOUT_MS", read(env, "UNIFI_TIMEOUT_MS"), 30000),
    },
    tools: { allowWrites, allowDeletes, toolsets },
    transport,
    http: {
      host: read(env, "MCP_HTTP_HOST") ?? "127.0.0.1",
      port: int("MCP_HTTP_PORT", read(env, "MCP_HTTP_PORT"), 8080),
      authToken,
      allowUnauthenticated,
      allowedHosts: allowedHosts?.length ? allowedHosts : undefined,
    },
  };
}

/**
 * Startup warning for an https controller whose certificate is neither verified
 * nor pinned, so anyone on the path could impersonate it and capture credentials.
 */
export function insecureTlsWarning(cfg: UnifiConfig): string | undefined {
  if (cfg.verifySsl || cfg.tlsFingerprints?.length || !cfg.url.toLowerCase().startsWith("https:")) return undefined;
  return "the controller's TLS certificate is not verified (UNIFI_VERIFY_SSL=false) and not pinned. Set UNIFI_TLS_FINGERPRINT to pin it (see docs/security.md).";
}
