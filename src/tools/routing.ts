import { z } from "zod";
import { pathSegment } from "../unifi/client.js";
import { compact } from "../unifi/format.js";
import { domainEntries, ipEntries, summarizeTrafficMatch, targetArgs, targetDevices } from "./firewall.js";
import { restDelete, restList, restSave, v2Delete, v2Get, v2List } from "./rest.js";
import { defined, defineTool, extraArg, idArg, siteArg, type ToolContext } from "./util.js";

type Obj = Record<string, any>;

const ROUTE_TYPES = { nexthop: "nexthop-route", interface: "interface-route", blackhole: "blackhole" } as const;

// Static route fields use hyphenated keys ("static-route_network"); older objects may use underscores.
const field = (r: Obj, name: string) => r[`static-route_${name}`] ?? r[`static_route_${name}`];

function summarizeRoute(r: Obj): Obj {
  const type = field(r, "type");
  return compact({
    id: r._id,
    name: r.name,
    enabled: r.enabled,
    type: (Object.keys(ROUTE_TYPES) as (keyof typeof ROUTE_TYPES)[]).find((k) => ROUTE_TYPES[k] === type) ?? type ?? r.type,
    destination: field(r, "network"),
    next_hop: field(r, "nexthop"),
    interface: field(r, "interface"),
    distance: field(r, "distance"),
  });
}

function summarizeTrafficRoute(r: Obj): Obj {
  return compact({
    id: r._id,
    description: r.description,
    enabled: r.enabled,
    matching_target: r.matching_target,
    network_id: r.network_id,
    next_hop: r.next_hop,
    kill_switch: r.kill_switch_enabled || undefined,
    ...summarizeTrafficMatch(r),
  });
}

export function registerRoutingTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  // ---------------------------------------------------------------- static routes

  defineTool(ctx, "unifi_list_routes", {
    toolset: "routing",
    title: "List static routes",
    description:
      "List user-defined static routes. With active=true, return the gateway's live routing table instead (stat/routing; not available on every version).",
    input: { ...siteArg, active: z.boolean().optional().describe("Return the active routing table (including system routes)") },
    handler: async (a) => {
      if (a.active) return client.siteGet(site(a.site), "stat/routing");
      return (await restList(client, site(a.site), "routing")).map(summarizeRoute);
    },
  });

  defineTool(ctx, "unifi_save_route", {
    toolset: "routing",
    title: "Create or update static route",
    description:
      "Create a static route (name and destination required; next_hop for type=nexthop, interface for type=interface) or update one by id. Defaults on create: type nexthop, distance 1, enabled.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: z.string().optional().describe("Route ID to update (only the fields passed are changed). Omit to create."),
      name: z.string().optional(),
      enabled: z.boolean().optional(),
      type: z
        .enum(["nexthop", "interface", "blackhole"])
        .optional()
        .describe("nexthop: via a gateway IP; interface: out of an interface (WAN1/WAN2 or a network ID); blackhole: drop"),
      destination: z.string().optional().describe("Destination network in CIDR form, e.g. 10.20.0.0/16"),
      next_hop: z.string().optional().describe("Next-hop IP address (type=nexthop)"),
      interface: z.string().optional().describe("Interface (type=interface), e.g. WAN1, WAN2 or a network ID"),
      distance: z.number().int().min(1).max(255).optional().describe("Administrative distance"),
      extra: extraArg,
    },
    handler: async (a) => {
      let body: Obj = defined({
        name: a.name,
        enabled: a.enabled,
        "static-route_type": a.type && ROUTE_TYPES[a.type],
        "static-route_network": a.destination,
        "static-route_nexthop": a.next_hop,
        "static-route_interface": a.interface,
        "static-route_distance": a.distance,
      });
      if (!a.id) {
        const type = a.type ?? "nexthop";
        if (!a.name || !a.destination) throw new Error("name and destination are required to create a route");
        if (type === "nexthop" && !a.next_hop) throw new Error("next_hop is required for a nexthop route");
        if (type === "interface" && !a.interface) throw new Error("interface is required for an interface route");
        body = {
          type: "static-route",
          enabled: true,
          gateway_type: "default",
          "static-route_type": ROUTE_TYPES[type],
          "static-route_distance": 1,
          ...body,
        };
      }
      return summarizeRoute(await restSave(client, site(a.site), "routing", a.id, { ...body, ...a.extra }));
    },
  });

  defineTool(ctx, "unifi_delete_route", {
    toolset: "routing",
    title: "Delete static route",
    description: "Delete a static route.",
    delete: true,
    input: { ...siteArg, id: idArg("Route") },
    handler: async (a) => {
      await restDelete(client, site(a.site), "routing", a.id);
      return { deleted: a.id };
    },
  });

  // ---------------------------------------------------------------- traffic routes (policy-based routing, v2)

  defineTool(ctx, "unifi_list_traffic_routes", {
    toolset: "routing",
    title: "List traffic routes",
    description: "List traffic routes (policy-based routing): which clients/networks and destinations are sent out a specific WAN or VPN client.",
    input: { ...siteArg, raw: z.boolean().optional().describe("Return the full unprocessed objects") },
    handler: async (a) => {
      const routes = await v2List(client, site(a.site), "trafficroutes");
      return a.raw ? routes : routes.map(summarizeTrafficRoute);
    },
  });

  defineTool(ctx, "unifi_save_traffic_route", {
    toolset: "routing",
    title: "Create or update traffic route",
    description:
      "Create a traffic route (description, matching_target and network_id required) or update one by id. Routes matching traffic from the target clients/networks via network_id (a WAN or VPN client network; see unifi_list_networks). " +
      "matching_target: DOMAIN (domains), IP (ip_addresses), REGION (regions) or INTERNET (all traffic; requires explicit client_macs/network_ids). Targets default to all clients for DOMAIN/IP/REGION.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: z.string().optional().describe("Traffic route ID to update (only the fields passed are changed). Omit to create."),
      description: z.string().optional().describe("Route name"),
      enabled: z.boolean().optional(),
      matching_target: z.enum(["DOMAIN", "IP", "REGION", "INTERNET"]).optional(),
      network_id: z.string().optional().describe("Network ID to route through (WAN or VPN client)"),
      ...targetArgs,
      domains: z.array(z.string()).optional().describe("Domains (matching_target=DOMAIN)"),
      ip_addresses: z.array(z.string()).optional().describe("IPs or CIDRs (matching_target=IP)"),
      regions: z.array(z.string()).optional().describe("ISO country codes (matching_target=REGION)"),
      kill_switch: z.boolean().optional().describe("Block the traffic if the route's interface/VPN is down"),
      next_hop: z.string().optional().describe("Optional next-hop IP"),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      const patch: Obj = defined({
        description: a.description,
        enabled: a.enabled,
        matching_target: a.matching_target,
        network_id: a.network_id,
        target_devices: targetDevices(a.client_macs, a.network_ids, a.all_clients),
        domains: domainEntries(a.domains),
        ip_addresses: ipEntries(a.ip_addresses),
        regions: a.regions,
        kill_switch_enabled: a.kill_switch,
        next_hop: a.next_hop,
      });
      if (a.id) {
        const current = await v2Get(client, s, "trafficroutes", a.id);
        const merged = { ...current, ...patch, ...a.extra };
        const saved = await client.v2<Obj>("PUT", s, `trafficroutes/${pathSegment(a.id)}`, merged);
        return summarizeTrafficRoute(saved && typeof saved === "object" && !Array.isArray(saved) ? saved : merged);
      }
      if (!a.description || !a.matching_target || !a.network_id) {
        throw new Error("description, matching_target and network_id are required to create a traffic route");
      }
      if (a.matching_target === "INTERNET" && !patch.target_devices?.some((t: Obj) => t.type !== "ALL_CLIENTS")) {
        throw new Error("INTERNET traffic routes need explicit client_macs or network_ids");
      }
      const body: Obj = {
        enabled: true,
        domains: [],
        ip_addresses: [],
        ip_ranges: [],
        regions: [],
        kill_switch_enabled: false,
        next_hop: "",
        target_devices: [{ type: "ALL_CLIENTS" }],
        ...patch,
        ...a.extra,
      };
      const created: any = await client.v2("POST", s, "trafficroutes", body);
      return summarizeTrafficRoute(Array.isArray(created) ? created[0] : (created ?? body));
    },
  });

  defineTool(ctx, "unifi_delete_traffic_route", {
    toolset: "routing",
    title: "Delete traffic route",
    description: "Delete a traffic route (policy-based route).",
    delete: true,
    input: { ...siteArg, id: idArg("Traffic route") },
    handler: async (a) => {
      await v2Delete(client, site(a.site), "trafficroutes", a.id);
      return { deleted: a.id };
    },
  });
}
