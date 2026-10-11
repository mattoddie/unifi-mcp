import { z } from "zod";
import { redactSecrets } from "../unifi/format.js";
import { defineTool, type ToolContext } from "./util.js";

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
          : "DELETE is not allowed because delete access is disabled (UNIFI_ALLOW_DELETES=false)."),
    readOnly: !ctx.allowWrites,
    destructive: ctx.allowWrites,
    input: {
      method: z.enum(["GET", "POST", "PUT", "DELETE", "PATCH"]).optional().describe("HTTP method (default GET)"),
      path: z.string().describe("Path relative to the Network application, starting with /"),
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
      if (!a.path.startsWith("/") || a.path.includes("..")) throw new Error("path must be absolute and must not contain '..'");
      const data = await client.request(method, a.path, { query: a.query, body: a.body });
      return redactSecrets(data);
    },
  });
}
