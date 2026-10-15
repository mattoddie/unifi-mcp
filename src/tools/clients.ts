import { z } from "zod";
import { normalizeMac, summarizeClient } from "../unifi/format.js";
import type { UnifiClient } from "../unifi/client.js";
import { defineTool, filterAndLimit, limitArg, macArg, searchArg, siteArg, type ToolContext } from "./util.js";

const rawArg = z.boolean().optional().describe("Return the full unprocessed objects from the controller (large)");

async function findClientRecord(client: UnifiClient, site: string, mac: string): Promise<any> {
  const [record]: any[] = await client.siteGet(site, `stat/user/${mac}`).catch(() => []);
  if (!record) throw new Error(`Client ${mac} is not known to the controller on site ${site}`);
  return record;
}

export function registerClientsTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  defineTool(ctx, "unifi_list_clients", {
    toolset: "clients",
    title: "List connected clients",
    description:
      "List clients that are currently connected, with IP, network, AP/switch port, signal, rates, usage and uptime.",
    input: {
      ...siteArg,
      connection: z.enum(["all", "wired", "wireless"]).optional().describe("Filter by connection type (default all)"),
      guests_only: z.boolean().optional().describe("Only guest clients"),
      search: searchArg,
      limit: limitArg(200),
      raw: rawArg,
    },
    handler: async (a) => {
      let clients: any[] = await client.siteGet(site(a.site), "stat/sta");
      if (a.connection === "wired") clients = clients.filter((c) => c.is_wired);
      if (a.connection === "wireless") clients = clients.filter((c) => !c.is_wired);
      if (a.guests_only) clients = clients.filter((c) => c.is_guest);
      const res = filterAndLimit(clients, { search: a.search, limit: a.limit ?? 200 }, (c) => [
        c.name,
        c.hostname,
        c.mac,
        c.ip,
        c.oui,
        c.essid,
        c.network,
      ]);
      return { ...res, items: a.raw ? res.items : res.items.map(summarizeClient) };
    },
  });

  defineTool(ctx, "unifi_list_known_clients", {
    toolset: "clients",
    title: "List known clients",
    description:
      "List all clients the controller has ever seen (including offline ones), with alias, note, fixed IP and block status.",
    input: {
      ...siteArg,
      filter: z
        .enum(["all", "blocked", "fixed_ip", "noted", "named"])
        .optional()
        .describe("Only blocked clients, clients with a fixed IP, clients with a note, or clients with an alias"),
      search: searchArg,
      limit: limitArg(200),
      raw: rawArg,
    },
    handler: async (a) => {
      let users: any[] = await client.siteGet(site(a.site), "rest/user");
      if (a.filter === "blocked") users = users.filter((u) => u.blocked);
      if (a.filter === "fixed_ip") users = users.filter((u) => u.use_fixedip);
      if (a.filter === "noted") users = users.filter((u) => u.noted || u.note);
      if (a.filter === "named") users = users.filter((u) => u.name);
      users.sort((x, y) => (y.last_seen ?? 0) - (x.last_seen ?? 0));
      const res = filterAndLimit(users, { search: a.search, limit: a.limit ?? 200 }, (u) => [
        u.name,
        u.hostname,
        u.mac,
        u.fixed_ip,
        u.last_ip,
        u.oui,
        u.note,
      ]);
      return { ...res, items: a.raw ? res.items : res.items.map(summarizeClient) };
    },
  });

  defineTool(ctx, "unifi_get_client", {
    toolset: "clients",
    title: "Get client details",
    description: "Get everything known about a client by MAC: stored settings plus live connection details if it is online.",
    input: { ...siteArg, mac: macArg, raw: rawArg },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      const [stored, active] = await Promise.all([
        client.siteGet<any[]>(site(a.site), `stat/user/${mac}`).catch(() => []),
        client.siteGet<any[]>(site(a.site), "stat/sta").then((all) => all.find((c) => c.mac === mac)),
      ]);
      const record = stored[0];
      if (!record && !active) throw new Error(`No client with MAC ${mac} on site ${site(a.site)}`);
      if (a.raw) return { stored: record ?? null, active: active ?? null };
      return { online: Boolean(active), ...summarizeClient({ ...record, ...active }) };
    },
  });

  defineTool(ctx, "unifi_block_client", {
    toolset: "clients",
    title: "Block client",
    description: "Block a client from connecting to the network.",
    write: true,
    destructive: true,
    input: { ...siteArg, mac: macArg },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "stamgr", "block-sta", { mac });
      return { blocked: mac };
    },
  });

  defineTool(ctx, "unifi_unblock_client", {
    toolset: "clients",
    title: "Unblock client",
    description: "Unblock a previously blocked client.",
    write: true,
    input: { ...siteArg, mac: macArg },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "stamgr", "unblock-sta", { mac });
      return { unblocked: mac };
    },
  });

  defineTool(ctx, "unifi_reconnect_client", {
    toolset: "clients",
    title: "Reconnect (kick) client",
    description: "Disconnect a wireless client so it reconnects (useful to force a roam or re-authentication).",
    write: true,
    input: { ...siteArg, mac: macArg },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "stamgr", "kick-sta", { mac });
      return { reconnected: mac };
    },
  });

  defineTool(ctx, "unifi_forget_clients", {
    toolset: "clients",
    title: "Forget clients",
    description: "Remove clients and their history (alias, note, fixed IP, statistics) from the controller.",
    delete: true,
    input: { ...siteArg, macs: z.array(z.string()).min(1).describe("MAC addresses to forget") },
    handler: async (a) => {
      const macs = a.macs.map(normalizeMac);
      await client.cmd(site(a.site), "stamgr", "forget-sta", { macs });
      return { forgotten: macs };
    },
  });

  defineTool(ctx, "unifi_update_client", {
    toolset: "clients",
    title: "Update client settings",
    description:
      "Set a known client's alias (name) and note, or assign/clear a fixed IP (DHCP reservation). Only the fields given are changed.",
    write: true,
    input: {
      ...siteArg,
      mac: macArg,
      name: z.string().optional().describe("Alias to show instead of the hostname (empty string clears it)"),
      note: z.string().optional().describe("Free-text note (empty string clears it)"),
      fixed_ip: z.string().optional().describe("IPv4 address to reserve for this client"),
      network_id: z
        .string()
        .optional()
        .describe("Network ID the fixed IP belongs to (defaults to the client's current network; see unifi_list_networks)"),
      clear_fixed_ip: z.boolean().optional().describe("Remove the fixed IP reservation"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const record = await findClientRecord(client, s, normalizeMac(a.mac));
      const patch: Record<string, unknown> = {};
      if (a.name !== undefined) patch.name = a.name;
      if (a.note !== undefined) Object.assign(patch, { note: a.note, noted: a.note !== "" });
      if (a.fixed_ip && a.clear_fixed_ip) throw new Error("Pass either fixed_ip or clear_fixed_ip, not both");
      if (a.fixed_ip) {
        const networkId = a.network_id ?? record.network_id;
        if (!networkId) throw new Error("network_id is required because the client's current network is unknown");
        Object.assign(patch, { use_fixedip: true, fixed_ip: a.fixed_ip, network_id: networkId });
      }
      if (a.clear_fixed_ip) patch.use_fixedip = false;
      if (Object.keys(patch).length === 0) throw new Error("Nothing to update: pass name, note, fixed_ip or clear_fixed_ip");

      const [updated]: any[] = await client.sitePut(s, `rest/user/${record._id}`, patch);
      return summarizeClient(updated ?? { ...record, ...patch });
    },
  });
}
