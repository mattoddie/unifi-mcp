import { z } from "zod";
import { assertSafePath } from "../unifi/client.js";
import { redactSecrets } from "../unifi/format.js";
import { defineTool, type ToolContext } from "./util.js";

/**
 * Commands and endpoints that remove things even though they aren't sent with
 * DELETE, e.g. POST cmd/sitemgr {"cmd":"delete-site"}, cmd/stamgr forget-sta,
 * cmd/sitemgr revoke-admin or v2 .../batch-delete.
 */
const DELETE_LIKE = /delete|forget|revoke|remove|destroy|reset/i;

/** The reason a non-GET raw request counts as a delete, if it does. */
export function deleteLikeReason(path: string, body: unknown): string | undefined {
  const bodies = Array.isArray(body) ? body : [body];
  for (const b of bodies) {
    const cmd = b && typeof b === "object" ? (b as Record<string, unknown>).cmd : undefined;
    if (typeof cmd === "string" && DELETE_LIKE.test(cmd)) return `command "${cmd}"`;
  }
  const segment = path.split("/").find((s) => DELETE_LIKE.test(s));
  return segment ? `path segment "${segment}"` : undefined;
}

export function registerRawTools(ctx: ToolContext): void {
  const { client } = ctx;

  defineTool(ctx, "unifi_api_request", {
    toolset: "raw",
    title: "Raw UniFi API request",
    description:
      "Call any UniFi Network API endpoint directly, for anything the other tools don't cover. `path` is relative to the Network application, e.g. /api/s/default/stat/ccode, /v2/api/site/default/trafficroutes or /integration/v1/sites (official API, API-key auth). " +
      (!ctx.allowWrites
        ? "Only GET is allowed because write access is disabled (UNIFI_ALLOW_WRITES=false)."
        : ctx.allowDeletes
          ? "All HTTP methods are allowed."
          : "DELETE, and commands that delete, forget, revoke, remove, destroy or reset something, are not allowed because delete access is disabled (UNIFI_ALLOW_DELETES=false)."),
    readOnly: !ctx.allowWrites,
    destructive: ctx.allowWrites,
    input: {
      method: z.enum(["GET", "POST", "PUT", "DELETE", "PATCH"]).optional().describe("HTTP method (default GET)"),
      path: z.string().describe("Path relative to the Network application, starting with / (no '..' segments or query string)"),
      query: z.record(z.string(), z.string()).optional().describe("Query string parameters"),
      body: z.any().optional().describe("JSON request body"),
    },
    handler: async (a) => {
      const method = a.method ?? "GET";
      if (method !== "GET" && !ctx.allowWrites) {
        throw new Error("Write access is disabled. Set UNIFI_ALLOW_WRITES=true to allow non-GET requests.");
      }
      if (method === "DELETE" && !ctx.allowDeletes) {
        throw new Error("Delete access is disabled. Set UNIFI_ALLOW_DELETES=true to allow DELETE requests.");
      }
      assertSafePath(a.path);
      if (method !== "GET" && !ctx.allowDeletes) {
        const reason = deleteLikeReason(a.path, a.body);
        if (reason) {
          throw new Error(`Delete access is disabled and this request looks like a delete (${reason}). Set UNIFI_ALLOW_DELETES=true to allow it.`);
        }
      }
      const data = await client.request(method, a.path, { query: a.query, body: a.body });
      return redactSecrets(data);
    },
  });
}
