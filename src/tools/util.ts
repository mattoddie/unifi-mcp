import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
import type { ToolOptions, Toolset } from "../config.js";
import type { UnifiClient } from "../unifi/client.js";

export interface ToolContext extends ToolOptions {
  server: McpServer;
  client: UnifiClient;
}

export const siteArg = {
  site: z
    .string()
    .optional()
    .describe("Site name/ID as used in the API (e.g. 'default'). Defaults to the configured site. See unifi_list_sites."),
};

export const macArg = z.string().describe("MAC address, e.g. aa:bb:cc:dd:ee:ff");

export const idArg = (what: string) => z.string().describe(`${what} ID (the \`id\` field returned by the matching list tool)`);

/**
 * Free-form extra fields merged into the object sent to the controller, for
 * settings the typed arguments don't cover. Field names are the controller's own.
 */
export const extraArg = z
  .record(z.string(), z.any())
  .optional()
  .describe("Additional raw controller fields to set (advanced; field names as in the raw objects). Merged last.");

export function ok(data: unknown): CallToolResult {
  return { content: [{ type: "text", text: JSON.stringify(data, null, 2) ?? "null" }] };
}

export function fail(err: unknown): CallToolResult {
  const message = err instanceof Error ? err.message : String(err);
  return { content: [{ type: "text", text: `Error: ${message}` }], isError: true };
}

type Shape = z.ZodRawShape;

interface ToolDef<S extends Shape> {
  /** Toolset the tool belongs to; only registered when the toolset is enabled (UNIFI_TOOLSETS). */
  toolset: Toolset;
  title: string;
  description: string;
  input: S;
  /** true for tools that change controller state; they are only registered when writes are enabled. */
  write?: boolean;
  /** true for tools that delete configuration; only registered when deletes are enabled too. */
  delete?: boolean;
  destructive?: boolean;
  /** Override the readOnlyHint annotation (defaults to !write). */
  readOnly?: boolean;
  handler: (args: z.infer<z.ZodObject<S>>) => Promise<unknown>;
}

export function defineTool<S extends Shape>(ctx: ToolContext, name: string, def: ToolDef<S>): void {
  if (!ctx.toolsets.has(def.toolset)) return;
  const write = def.write || def.delete;
  if (write && !ctx.allowWrites) return;
  if (def.delete && !ctx.allowDeletes) return;
  const readOnly = def.readOnly ?? !write;
  ctx.server.registerTool(
    name,
    {
      title: def.title,
      description: def.description,
      inputSchema: def.input,
      annotations: {
        title: def.title,
        readOnlyHint: readOnly,
        destructiveHint: readOnly ? undefined : (def.destructive ?? Boolean(def.delete)),
        openWorldHint: false,
      },
    },
    (async (args: z.infer<z.ZodObject<S>>) => {
      try {
        return ok(await def.handler(args));
      } catch (err) {
        return fail(err);
      }
    }) as any,
  );
}

export function filterAndLimit<T>(items: T[], opts: { search?: string; limit?: number }, fields: (item: T) => unknown[]): {
  total: number;
  returned: number;
  items: T[];
} {
  let out = items;
  if (opts.search) {
    const q = opts.search.toLowerCase();
    out = out.filter((item) =>
      fields(item).some((f) => f !== undefined && f !== null && String(f).toLowerCase().includes(q)),
    );
  }
  const total = out.length;
  if (opts.limit !== undefined) out = out.slice(0, opts.limit);
  return { total, returned: out.length, items: out };
}

export const searchArg = z.string().optional().describe("Case-insensitive substring filter on name, hostname, MAC, IP and similar fields");
export const limitArg = (def: number) =>
  z.number().int().positive().max(5000).optional().describe(`Maximum number of results (default ${def})`);

/** Copy only the keys whose value is not undefined (lets "" and false through, unlike compact()). */
export function defined(obj: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(obj).filter(([, v]) => v !== undefined));
}
