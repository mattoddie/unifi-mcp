import { z } from "zod";
import type { UnifiClient } from "../unifi/client.js";
import { compact, normalizeMac, redactSecrets } from "../unifi/format.js";
import { restDelete, restGet, restList, restSave, v2Delete, v2Get, v2List, v2Save } from "./rest.js";
import { defined, defineTool, extraArg, idArg, siteArg, type ToolContext } from "./util.js";

type Obj = Record<string, any>;

const rawArg = z.boolean().optional().describe("Return the full unprocessed objects from the controller (large)");
const saveIdArg = (what: string) =>
  z.string().optional().describe(`${what} ID to update (only the fields passed are changed). Omit to create a new one.`);

const RULESETS = [
  "WAN_IN",
  "WAN_OUT",
  "WAN_LOCAL",
  "LAN_IN",
  "LAN_OUT",
  "LAN_LOCAL",
  "GUEST_IN",
  "GUEST_OUT",
  "GUEST_LOCAL",
  "WANv6_IN",
  "WANv6_OUT",
  "WANv6_LOCAL",
  "LANv6_IN",
  "LANv6_OUT",
  "LANv6_LOCAL",
  "GUESTv6_IN",
  "GUESTv6_OUT",
  "GUESTv6_LOCAL",
] as const;

// ---------------------------------------------------------------- shared helpers (also used by routing.ts)

/** Traffic rule/route target_devices from convenience args; undefined when neither list is given. */
export function targetDevices(clientMacs?: string[], networkIds?: string[], allClients?: boolean): Obj[] | undefined {
  if (allClients) return [{ type: "ALL_CLIENTS" }];
  if (clientMacs === undefined && networkIds === undefined) return undefined;
  const targets = [
    ...(clientMacs ?? []).map((m) => ({ type: "CLIENT", client_mac: normalizeMac(m) })),
    ...(networkIds ?? []).map((n) => ({ type: "NETWORK", network_id: n })),
  ];
  return targets.length ? targets : [{ type: "ALL_CLIENTS" }];
}

export const targetArgs = {
  all_clients: z.boolean().optional().describe("Apply to all clients (default on create when no clients/networks are given)"),
  client_macs: z.array(z.string()).optional().describe("Apply to these client MAC addresses"),
  network_ids: z.array(z.string()).optional().describe("Apply to all clients on these network IDs (see unifi_list_networks)"),
};

export function domainEntries(domains?: string[]): Obj[] | undefined {
  return domains?.map((domain) => ({ domain, ports: [], port_ranges: [] }));
}

export function ipEntries(ips?: string[]): Obj[] | undefined {
  return ips?.map((ip) => ({ ip_or_subnet: ip, ip_version: ip.includes(":") ? "v6" : "v4", ports: [], port_ranges: [] }));
}

function summarizeTargets(targets: Obj[] | undefined): unknown {
  if (!targets?.length) return undefined;
  return targets.map((t) => (t.type === "CLIENT" ? `client:${t.client_mac}` : t.type === "NETWORK" ? `network:${t.network_id}` : t.type));
}

export function summarizeTrafficMatch(r: Obj): Obj {
  return compact({
    domains: r.domains?.length ? r.domains.map((d: any) => (typeof d === "string" ? d : d.domain)) : undefined,
    ip_addresses: r.ip_addresses?.length ? r.ip_addresses.map((i: any) => (typeof i === "string" ? i : i.ip_or_subnet)) : undefined,
    ip_ranges: r.ip_ranges?.length ? r.ip_ranges.map((i: any) => `${i.ip_start}-${i.ip_stop}`) : undefined,
    regions: r.regions?.length ? r.regions : undefined,
    targets: summarizeTargets(r.target_devices),
  });
}

/** Resolve a site name to the official integration API site UUID. */
async function integrationSiteId(client: UnifiClient, site: string): Promise<string> {
  const res: any = await client.request("GET", "/integration/v1/sites", { query: { limit: 200 } });
  const sites: Obj[] = res?.data ?? [];
  const match =
    sites.find((s) => [s.internalReference, s.name, s.id].includes(site)) ?? (sites.length === 1 ? sites[0] : undefined);
  if (!match?.id) throw new Error(`Could not find site "${site}" in the integration API (/integration/v1/sites)`);
  return match.id;
}

async function integrationZones(client: UnifiClient, siteId: string): Promise<Obj[]> {
  const res: any = await client.request("GET", `/integration/v1/sites/${siteId}/firewall/zones`, { query: { limit: 200 } });
  return res?.data ?? [];
}

/** Map a v2 zone _id (or an integration zone id/name) to the integration zone record. */
async function resolveIntegrationZone(client: UnifiClient, site: string, siteId: string, zoneId: string): Promise<Obj> {
  const zones = await integrationZones(client, siteId);
  const direct = zones.find((z) => z.id === zoneId);
  if (direct) return direct;
  const v2Zones = await v2List(client, site, "firewall/zone").catch(() => [] as Obj[]);
  const v2 = v2Zones.find((z) => z._id === zoneId || z.name === zoneId);
  if (v2?.external_id) {
    const byExternal = zones.find((z) => z.id === v2.external_id);
    if (byExternal) return byExternal;
  }
  const name = String(v2?.name ?? zoneId).toLowerCase();
  const byName = zones.filter((z) => String(z.name ?? "").toLowerCase() === name);
  if (byName.length === 1) return byName[0];
  throw new Error(`Could not map firewall zone "${zoneId}" to an integration API zone`);
}

const INTEGRATION_NOTE = "Uses the official integration API, so it requires API-key authentication (UNIFI_API_KEY) and Network 9.0+.";

// ---------------------------------------------------------------- summaries

function summarizeRule(r: Obj): Obj {
  return compact({
    id: r._id,
    name: r.name,
    enabled: r.enabled,
    ruleset: r.ruleset,
    index: r.rule_index,
    action: r.action,
    protocol: r.protocol,
    src: compact({
      address: r.src_address,
      network_id: r.src_networkconf_id,
      group_ids: r.src_firewallgroup_ids?.length ? r.src_firewallgroup_ids : undefined,
      mac: r.src_mac_address,
      port: r.src_port,
    }),
    dst: compact({
      address: r.dst_address,
      network_id: r.dst_networkconf_id,
      group_ids: r.dst_firewallgroup_ids?.length ? r.dst_firewallgroup_ids : undefined,
      port: r.dst_port,
    }),
    states: [r.state_new && "new", r.state_established && "established", r.state_related && "related", r.state_invalid && "invalid"].filter(Boolean),
    logging: r.logging || undefined,
  });
}

function summarizePortForward(r: Obj): Obj {
  return compact({
    id: r._id,
    name: r.name,
    enabled: r.enabled,
    protocol: r.proto,
    wan_interface: r.pfwd_interface,
    source: r.src,
    destination_ip: r.destination_ip,
    external_port: r.dst_port,
    forward_ip: r.fwd,
    forward_port: r.fwd_port,
    log: r.log,
  });
}

function summarizeEndpoint(e: Obj | undefined): Obj | undefined {
  if (!e) return undefined;
  return compact({
    zone_id: e.zone_id,
    match: e.matching_target,
    match_type: e.matching_target_type,
    ips: e.ips?.length ? e.ips : undefined,
    network_ids: e.network_ids?.length ? e.network_ids : undefined,
    client_macs: e.client_macs?.length ? e.client_macs : undefined,
    ip_group_id: e.ip_group_id,
    port: e.port_matching_type === "SPECIFIC" ? e.port : undefined,
    port_group_id: e.port_matching_type === "OBJECT" ? e.port_group_id : undefined,
    port_match: e.port_matching_type && e.port_matching_type !== "ANY" ? e.port_matching_type : undefined,
    negate: e.match_opposite_ips || e.match_opposite_ports || undefined,
  });
}

function summarizePolicy(p: Obj): Obj {
  return compact({
    id: p._id,
    name: p.name,
    description: p.description,
    enabled: p.enabled,
    action: p.action,
    index: p.index,
    predefined: p.predefined || undefined,
    protocol: p.protocol,
    ip_version: p.ip_version,
    connection_states: p.connection_state_type === "CUSTOM" ? p.connection_states : p.connection_state_type,
    source: summarizeEndpoint(p.source),
    destination: summarizeEndpoint(p.destination),
    schedule: p.schedule?.mode && p.schedule.mode !== "ALWAYS" ? p.schedule : undefined,
    logging: p.logging || undefined,
  });
}

function summarizeTrafficRule(r: Obj): Obj {
  return compact({
    id: r._id,
    description: r.description,
    enabled: r.enabled,
    action: r.action,
    matching_target: r.matching_target,
    ...summarizeTrafficMatch(r),
    app_ids: r.app_ids?.length ? r.app_ids : undefined,
    app_category_ids: r.app_category_ids?.length ? r.app_category_ids : undefined,
    bandwidth_limit: r.bandwidth_limit?.enabled
      ? { down_kbps: r.bandwidth_limit.download_limit_kbps, up_kbps: r.bandwidth_limit.upload_limit_kbps }
      : undefined,
    schedule: r.schedule?.mode && r.schedule.mode !== "ALWAYS" ? r.schedule : undefined,
  });
}

// ---------------------------------------------------------------- input schemas

const policyEndpoint = z
  .looseObject({
    zone_id: z.string().optional().describe("Firewall zone ID (see unifi_list_firewall_zones)"),
    matching_target: z
      .enum(["ANY", "IP", "NETWORK", "CLIENT", "REGION", "DOMAIN", "APP", "APP_CATEGORY", "WEB"])
      .optional()
      .describe("What to match inside the zone (default ANY)"),
    matching_target_type: z
      .enum(["ANY", "SPECIFIC", "OBJECT"])
      .optional()
      .describe("SPECIFIC with ips, OBJECT with network_ids or ip_group_id"),
    ips: z.array(z.string()).optional().describe("IPs/CIDRs/ranges for matching_target=IP, matching_target_type=SPECIFIC"),
    network_ids: z.array(z.string()).optional().describe("Network IDs for matching_target=NETWORK"),
    client_macs: z.array(z.string()).optional().describe("Client MACs for matching_target=CLIENT"),
    port_matching_type: z.enum(["ANY", "SPECIFIC", "OBJECT"]).optional().describe("SPECIFIC with port, OBJECT with port_group_id"),
    port: z.string().optional().describe("Port(s) as one string, e.g. '53' or '80,443' or '8000-8100'"),
    port_group_id: z.string().optional().describe("Port group ID (firewall group of type port-group)"),
  })
  .describe("Policy endpoint; extra controller fields are passed through");

function normalizeEndpoint(e: Obj | undefined): Obj | undefined {
  if (!e) return undefined;
  const out: Obj = { ...e };
  if (typeof out.matching_target === "string") out.matching_target = out.matching_target.toUpperCase();
  if (Array.isArray(out.client_macs)) out.client_macs = out.client_macs.map(normalizeMac);
  if (out.port !== undefined && out.port_matching_type === undefined) out.port_matching_type = "SPECIFIC";
  if (out.port_group_id !== undefined && out.port_matching_type === undefined) out.port_matching_type = "OBJECT";
  if (out.ips?.length && out.matching_target === undefined) out.matching_target = "IP";
  if (out.ips?.length && out.matching_target_type === undefined) out.matching_target_type = "SPECIFIC";
  if (out.network_ids?.length && out.matching_target === undefined) out.matching_target = "NETWORK";
  if (out.network_ids?.length && out.matching_target_type === undefined) out.matching_target_type = "OBJECT";
  if (out.client_macs?.length && out.matching_target === undefined) out.matching_target = "CLIENT";
  return out;
}

export function registerFirewallTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  // ---------------------------------------------------------------- port forwards

  defineTool(ctx, "unifi_list_port_forwards", {
    toolset: "firewall",
    title: "List port forwards",
    description: "List port forwarding rules.",
    input: { ...siteArg },
    handler: async (a) => (await restList(client, site(a.site), "portforward")).map(summarizePortForward),
  });

  defineTool(ctx, "unifi_save_port_forward", {
    toolset: "firewall",
    title: "Create or update port forward",
    description:
      "Create a port forward (name, external_port and forward_ip required) or update one by id. Defaults on create: WAN interface 'wan', protocol tcp_udp, any source, enabled.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: saveIdArg("Port forward"),
      name: z.string().optional(),
      enabled: z.boolean().optional(),
      wan_interface: z.enum(["wan", "wan2", "both"]).optional().describe("WAN interface to listen on"),
      protocol: z.enum(["tcp_udp", "tcp", "udp"]).optional(),
      source: z.string().optional().describe("Allowed source: 'any' or an IP/CIDR"),
      destination_ip: z.string().optional().describe("WAN IP to match ('any' for all WAN IPs; newer Network versions)"),
      external_port: z.string().optional().describe("External port or range, e.g. '443' or '8000-8100'"),
      forward_ip: z.string().optional().describe("Internal IP to forward to"),
      forward_port: z.string().optional().describe("Internal port or range (defaults to external_port on create)"),
      log: z.boolean().optional().describe("Log forwarded connections"),
      extra: extraArg,
    },
    handler: async (a) => {
      let body: Obj = defined({
        name: a.name,
        enabled: a.enabled,
        pfwd_interface: a.wan_interface,
        proto: a.protocol,
        src: a.source,
        destination_ip: a.destination_ip,
        dst_port: a.external_port,
        fwd: a.forward_ip,
        fwd_port: a.forward_port,
        log: a.log,
      });
      if (!a.id) {
        if (!a.name || !a.external_port || !a.forward_ip) throw new Error("name, external_port and forward_ip are required to create a port forward");
        body = { enabled: true, pfwd_interface: "wan", proto: "tcp_udp", src: "any", log: false, fwd_port: a.external_port, ...body };
      }
      const saved = await restSave(client, site(a.site), "portforward", a.id, { ...body, ...a.extra });
      return summarizePortForward(saved);
    },
  });

  defineTool(ctx, "unifi_delete_port_forward", {
    toolset: "firewall",
    title: "Delete port forward",
    description: "Delete a port forwarding rule.",
    delete: true,
    input: { ...siteArg, id: idArg("Port forward") },
    handler: async (a) => {
      await restDelete(client, site(a.site), "portforward", a.id);
      return { deleted: a.id };
    },
  });

  // ---------------------------------------------------------------- classic firewall rules

  defineTool(ctx, "unifi_list_firewall_rules", {
    toolset: "firewall",
    title: "List firewall rules (legacy)",
    description:
      "List classic firewall rules (WAN_IN, LAN_IN, ...). On Network 9+ with zone-based firewall use unifi_list_firewall_policies instead.",
    input: { ...siteArg, raw: rawArg },
    handler: async (a) => {
      const rules = await restList(client, site(a.site), "firewallrule");
      rules.sort((x, y) => String(x.ruleset).localeCompare(String(y.ruleset)) || (x.rule_index ?? 0) - (y.rule_index ?? 0));
      return a.raw ? rules : rules.map(summarizeRule);
    },
  });

  defineTool(ctx, "unifi_save_firewall_rule", {
    toolset: "firewall",
    title: "Create or update firewall rule (legacy)",
    description:
      "Create a classic firewall rule (name, ruleset and action required) or update one by id. Not for zone-based firewall (Network 9+ with zones migrated) — use unifi_save_firewall_policy there. " +
      "On create, rule_index defaults to the next free index from 2000 in the ruleset (rules before the predefined ones).",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: saveIdArg("Firewall rule"),
      name: z.string().optional(),
      enabled: z.boolean().optional(),
      ruleset: z.enum(RULESETS).optional(),
      rule_index: z.number().int().optional().describe("Order within the ruleset (2000-2999 run before predefined rules, 4000+ after)"),
      action: z.enum(["accept", "drop", "reject"]).optional(),
      protocol: z.string().optional().describe("all (default), tcp, udp, tcp_udp, icmp, or another protocol name/number"),
      protocol_match_excepted: z.boolean().optional().describe("Match every protocol except the one given"),
      src_network_id: z.string().optional().describe("Source network ID"),
      src_network_type: z.enum(["NETv4", "ADDRv4"]).optional().describe("NETv4 = whole subnet, ADDRv4 = gateway IP of the network"),
      src_address: z.string().optional().describe("Source IP/CIDR"),
      src_mac: z.string().optional().describe("Source MAC address"),
      src_group_ids: z.array(z.string()).optional().describe("Source firewall group IDs (address and/or port group)"),
      src_port: z.string().optional().describe("Source port(s) (tcp/udp only)"),
      dst_network_id: z.string().optional(),
      dst_network_type: z.enum(["NETv4", "ADDRv4"]).optional(),
      dst_address: z.string().optional().describe("Destination IP/CIDR"),
      dst_group_ids: z.array(z.string()).optional().describe("Destination firewall group IDs"),
      dst_port: z.string().optional().describe("Destination port(s) (tcp/udp only)"),
      states: z
        .array(z.enum(["new", "established", "related", "invalid"]))
        .optional()
        .describe("Connection states to match (none = all states)"),
      logging: z.boolean().optional(),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      let body: Obj = defined({
        name: a.name,
        enabled: a.enabled,
        ruleset: a.ruleset,
        rule_index: a.rule_index,
        action: a.action,
        protocol: a.protocol,
        protocol_match_excepted: a.protocol_match_excepted,
        src_networkconf_id: a.src_network_id,
        src_networkconf_type: a.src_network_type,
        src_address: a.src_address,
        src_mac_address: a.src_mac === undefined ? undefined : a.src_mac && normalizeMac(a.src_mac),
        src_firewallgroup_ids: a.src_group_ids,
        src_port: a.src_port,
        dst_networkconf_id: a.dst_network_id,
        dst_networkconf_type: a.dst_network_type,
        dst_address: a.dst_address,
        dst_firewallgroup_ids: a.dst_group_ids,
        dst_port: a.dst_port,
        logging: a.logging,
      });
      if (a.states) {
        for (const st of ["new", "established", "related", "invalid"] as const) body[`state_${st}`] = a.states.includes(st);
      }
      if (!a.id) {
        if (!a.name || !a.ruleset || !a.action) throw new Error("name, ruleset and action are required to create a firewall rule");
        if (body.rule_index === undefined) {
          const existing = await restList(client, s, "firewallrule");
          const used = existing.filter((r) => r.ruleset === a.ruleset && r.rule_index >= 2000 && r.rule_index < 3000).map((r) => r.rule_index);
          body.rule_index = used.length ? Math.max(...used) + 1 : 2000;
        }
        body = {
          enabled: true,
          protocol: "all",
          protocol_match_excepted: false,
          logging: false,
          state_new: false,
          state_established: false,
          state_related: false,
          state_invalid: false,
          ipsec: "",
          src_firewallgroup_ids: [],
          src_mac_address: "",
          src_address: "",
          src_networkconf_id: "",
          src_networkconf_type: "NETv4",
          dst_firewallgroup_ids: [],
          dst_address: "",
          dst_networkconf_id: "",
          dst_networkconf_type: "NETv4",
          ...body,
        };
      }
      return summarizeRule(await restSave(client, s, "firewallrule", a.id, { ...body, ...a.extra }));
    },
  });

  defineTool(ctx, "unifi_delete_firewall_rule", {
    toolset: "firewall",
    title: "Delete firewall rule (legacy)",
    description: "Delete a classic firewall rule.",
    delete: true,
    input: { ...siteArg, id: idArg("Firewall rule") },
    handler: async (a) => {
      await restDelete(client, site(a.site), "firewallrule", a.id);
      return { deleted: a.id };
    },
  });

  // ---------------------------------------------------------------- firewall groups

  defineTool(ctx, "unifi_list_firewall_groups", {
    toolset: "firewall",
    title: "List firewall groups",
    description: "List firewall groups (address groups, IPv6 address groups and port groups) referenced by firewall rules and policies.",
    input: { ...siteArg },
    handler: async (a) =>
      (await restList(client, site(a.site), "firewallgroup")).map((g) =>
        compact({ id: g._id, name: g.name, type: g.group_type, members: g.group_members }),
      ),
  });

  defineTool(ctx, "unifi_save_firewall_group", {
    toolset: "firewall",
    title: "Create or update firewall group",
    description:
      "Create a firewall group (name and type required) or update one by id. members replaces the whole member list. The type of an existing group cannot be changed.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: saveIdArg("Firewall group"),
      name: z.string().optional(),
      type: z.enum(["address-group", "ipv6-address-group", "port-group"]).optional(),
      members: z.array(z.string()).optional().describe("IPs/CIDRs (address groups) or ports/ranges like '80' or '8000-8100' (port groups)"),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      let body: Obj = defined({ name: a.name, group_type: a.type, group_members: a.members });
      if (a.id) {
        if (a.type) {
          const current = await restGet(client, s, "firewallgroup", a.id);
          if (current.group_type !== a.type) throw new Error(`The type of an existing firewall group cannot be changed (it is ${current.group_type})`);
        }
      } else {
        if (!a.name || !a.type) throw new Error("name and type are required to create a firewall group");
        body = { group_members: [], ...body };
      }
      const g = await restSave(client, s, "firewallgroup", a.id, { ...body, ...a.extra });
      return compact({ id: g._id, name: g.name, type: g.group_type, members: g.group_members });
    },
  });

  defineTool(ctx, "unifi_delete_firewall_group", {
    toolset: "firewall",
    title: "Delete firewall group",
    description: "Delete a firewall group. The controller refuses to delete groups still referenced by rules or policies.",
    delete: true,
    input: { ...siteArg, id: idArg("Firewall group") },
    handler: async (a) => {
      await restDelete(client, site(a.site), "firewallgroup", a.id);
      return { deleted: a.id };
    },
  });

  // ---------------------------------------------------------------- zone-based firewall (Network 9.0+)

  defineTool(ctx, "unifi_list_firewall_policies", {
    toolset: "firewall",
    title: "List firewall policies (zone-based)",
    description: "List zone-based firewall policies (UniFi Network 9.0+). Predefined (system) policies are hidden unless include_predefined is set.",
    input: {
      ...siteArg,
      include_predefined: z.boolean().optional().describe("Also return predefined/system policies"),
      raw: rawArg,
    },
    handler: async (a) => {
      let policies = await v2List(client, site(a.site), "firewall-policies");
      if (!a.include_predefined) policies = policies.filter((p) => !p.predefined);
      policies.sort((x, y) => (x.index ?? 0) - (y.index ?? 0));
      return a.raw ? policies : policies.map(summarizePolicy);
    },
  });

  defineTool(ctx, "unifi_save_firewall_policy", {
    toolset: "firewall",
    title: "Create or update firewall policy (zone-based)",
    description:
      "Create a zone-based firewall policy (name, action, source and destination required) or update one by id (Network 9.0+). " +
      "source/destination: {zone_id, matching_target: ANY|IP|NETWORK|CLIENT|REGION|..., ips | network_ids | client_macs, port_matching_type, port | port_group_id}; on update they are merged into the current endpoint. " +
      "Use unifi_list_firewall_zones for zone IDs. Predefined policies cannot be changed.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: saveIdArg("Firewall policy"),
      name: z.string().optional(),
      description: z.string().optional(),
      enabled: z.boolean().optional(),
      action: z.enum(["ALLOW", "BLOCK", "REJECT"]).optional(),
      source: policyEndpoint.optional(),
      destination: policyEndpoint.optional(),
      protocol: z.string().optional().describe("all (default), tcp, udp, tcp_udp, icmp, icmpv6, ..."),
      ip_version: z.enum(["BOTH", "IPV4", "IPV6"]).optional(),
      connection_state_type: z.enum(["ALL", "RESPOND_ONLY", "CUSTOM"]).optional(),
      connection_states: z
        .array(z.enum(["NEW", "ESTABLISHED", "RELATED", "INVALID"]))
        .optional()
        .describe("With connection_state_type=CUSTOM"),
      create_allow_respond: z.boolean().optional().describe("For ALLOW policies: also create the return-traffic policy"),
      logging: z.boolean().optional(),
      schedule: z.looseObject({ mode: z.string() }).optional().describe("e.g. {mode: 'ALWAYS'} (default) or a custom schedule object"),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      const patch: Obj = defined({
        name: a.name,
        description: a.description,
        enabled: a.enabled,
        action: a.action,
        protocol: a.protocol,
        ip_version: a.ip_version,
        connection_state_type: a.connection_states && !a.connection_state_type ? "CUSTOM" : a.connection_state_type,
        connection_states: a.connection_states,
        create_allow_respond: a.create_allow_respond,
        logging: a.logging,
        schedule: a.schedule,
      });
      if (a.id) {
        const current = await v2Get(client, s, "firewall-policies", a.id);
        if (current.predefined) throw new Error("Predefined (system) firewall policies cannot be changed");
        if (a.source) patch.source = { ...current.source, ...normalizeEndpoint(a.source) };
        if (a.destination) patch.destination = { ...current.destination, ...normalizeEndpoint(a.destination) };
        const merged = { ...current, ...patch, ...a.extra };
        const saved = await client.v2<Obj>("PUT", s, `firewall-policies/${encodeURIComponent(a.id)}`, merged);
        return summarizePolicy(saved && typeof saved === "object" && !Array.isArray(saved) ? saved : merged);
      }
      if (!a.name || !a.action || !a.source || !a.destination) {
        throw new Error("name, action, source and destination are required to create a firewall policy");
      }
      const body: Obj = {
        enabled: true,
        protocol: "all",
        ip_version: "BOTH",
        logging: false,
        connection_state_type: "ALL",
        schedule: { mode: "ALWAYS" },
        ...patch,
        source: { matching_target: "ANY", ...normalizeEndpoint(a.source) },
        destination: { matching_target: "ANY", ...normalizeEndpoint(a.destination) },
        ...a.extra,
      };
      if (body.action !== "ALLOW") {
        if (body.create_allow_respond) throw new Error("create_allow_respond can only be used with ALLOW policies");
        body.create_allow_respond = false;
      }
      for (const side of ["source", "destination"] as const) {
        if (!body[side].zone_id) throw new Error(`${side}.zone_id is required (see unifi_list_firewall_zones)`);
      }
      const created: any = await client.v2("POST", s, "firewall-policies", body);
      return summarizePolicy(Array.isArray(created) ? created[0] : (created ?? body));
    },
  });

  defineTool(ctx, "unifi_delete_firewall_policy", {
    toolset: "firewall",
    title: "Delete firewall policy (zone-based)",
    description: "Delete a user-defined zone-based firewall policy (predefined policies are refused).",
    delete: true,
    input: { ...siteArg, id: idArg("Firewall policy") },
    handler: async (a) => {
      const s = site(a.site);
      const current = await v2Get(client, s, "firewall-policies", a.id);
      if (current.predefined) throw new Error("Predefined (system) firewall policies cannot be deleted");
      await v2Delete(client, s, "firewall-policies", a.id);
      return { deleted: a.id, name: current.name };
    },
  });

  defineTool(ctx, "unifi_get_firewall_policy_ordering", {
    toolset: "firewall",
    title: "Get firewall policy order",
    description: `Get the evaluation order of user-defined policies between a source and destination zone (beforeSystemDefined / afterSystemDefined policy ID lists). ${INTEGRATION_NOTE}`,
    input: {
      ...siteArg,
      source_zone_id: z.string().describe("Source zone ID (v2 _id from unifi_list_firewall_zones, or integration API id)"),
      destination_zone_id: z.string().describe("Destination zone ID"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const siteId = await integrationSiteId(client, s);
      const [src, dst] = await Promise.all([
        resolveIntegrationZone(client, s, siteId, a.source_zone_id),
        resolveIntegrationZone(client, s, siteId, a.destination_zone_id),
      ]);
      return client.request("GET", `/integration/v1/sites/${siteId}/firewall/policies/ordering`, {
        query: { sourceFirewallZoneId: src.id, destinationFirewallZoneId: dst.id },
      });
    },
  });

  defineTool(ctx, "unifi_set_firewall_policy_ordering", {
    toolset: "firewall",
    title: "Reorder firewall policies",
    description: `Set the evaluation order of user-defined policies between two zones. Pass every policy ID from unifi_get_firewall_policy_ordering, reordered (IDs are the integration API's). ${INTEGRATION_NOTE}`,
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      source_zone_id: z.string().describe("Source zone ID"),
      destination_zone_id: z.string().describe("Destination zone ID"),
      before_system_defined: z.array(z.string()).describe("Policy IDs evaluated before the predefined policies, in order"),
      after_system_defined: z.array(z.string()).optional().describe("Policy IDs evaluated after the predefined policies, in order"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const siteId = await integrationSiteId(client, s);
      const [src, dst] = await Promise.all([
        resolveIntegrationZone(client, s, siteId, a.source_zone_id),
        resolveIntegrationZone(client, s, siteId, a.destination_zone_id),
      ]);
      const path = `/integration/v1/sites/${siteId}/firewall/policies/ordering`;
      const query = { sourceFirewallZoneId: src.id, destinationFirewallZoneId: dst.id };
      const current: any = await client.request("GET", path, { query });
      const ordered = {
        beforeSystemDefined: a.before_system_defined,
        afterSystemDefined: a.after_system_defined ?? [],
      };
      const have = [
        ...(current?.orderedFirewallPolicyIds?.beforeSystemDefined ?? current?.beforeSystemDefined ?? []),
        ...(current?.orderedFirewallPolicyIds?.afterSystemDefined ?? current?.afterSystemDefined ?? []),
      ].sort();
      const want = [...ordered.beforeSystemDefined, ...ordered.afterSystemDefined].sort();
      if (have.length && JSON.stringify(have) !== JSON.stringify(want)) {
        throw new Error(`The new order must contain exactly the current policy IDs: ${have.join(", ")}`);
      }
      return client.request("PUT", path, { query, body: { orderedFirewallPolicyIds: ordered } });
    },
  });

  defineTool(ctx, "unifi_list_firewall_zones", {
    toolset: "firewall",
    title: "List firewall zones",
    description: "List zone-based firewall zones and the networks in each (UniFi Network 9.0+).",
    input: { ...siteArg, raw: rawArg },
    handler: async (a) => {
      const zones = await v2List(client, site(a.site), "firewall/zone");
      if (a.raw) return zones;
      return zones.map((z) =>
        compact({
          id: z._id,
          name: z.name,
          zone_key: z.zone_key,
          system_defined: z.default_zone || undefined,
          network_ids: z.network_ids?.length ? z.network_ids : undefined,
          integration_id: z.external_id,
        }),
      );
    },
  });

  defineTool(ctx, "unifi_save_firewall_zone", {
    toolset: "firewall",
    title: "Create or update firewall zone",
    description: `Create a custom firewall zone (name required) or update one by id: rename it and/or set which networks it contains (network_ids replaces the list). System-defined zones cannot be renamed. ${INTEGRATION_NOTE}`,
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: saveIdArg("Firewall zone"),
      name: z.string().min(1).optional(),
      network_ids: z.array(z.string()).optional().describe("Network IDs in the zone (see unifi_list_networks)"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const siteId = await integrationSiteId(client, s);
      if (!a.id) {
        if (!a.name) throw new Error("name is required to create a firewall zone");
        return client.request("POST", `/integration/v1/sites/${siteId}/firewall/zones`, {
          body: { name: a.name, networkIds: a.network_ids ?? [] },
        });
      }
      const zone = await resolveIntegrationZone(client, s, siteId, a.id);
      const systemDefined = zone.metadata?.origin === "SYSTEM_DEFINED";
      if (systemDefined && a.name && a.name !== zone.name) throw new Error(`Cannot rename system-defined zone "${zone.name}"`);
      return client.request("PUT", `/integration/v1/sites/${siteId}/firewall/zones/${zone.id}`, {
        body: { name: a.name ?? zone.name, networkIds: a.network_ids ?? zone.networkIds ?? [] },
      });
    },
  });

  defineTool(ctx, "unifi_delete_firewall_zone", {
    toolset: "firewall",
    title: "Delete firewall zone",
    description: `Delete a custom firewall zone (system-defined zones are refused). ${INTEGRATION_NOTE}`,
    delete: true,
    input: { ...siteArg, id: idArg("Firewall zone") },
    handler: async (a) => {
      const s = site(a.site);
      const siteId = await integrationSiteId(client, s);
      const zone = await resolveIntegrationZone(client, s, siteId, a.id);
      if (zone.metadata?.origin === "SYSTEM_DEFINED") throw new Error(`Cannot delete system-defined zone "${zone.name}"`);
      await client.request("DELETE", `/integration/v1/sites/${siteId}/firewall/zones/${zone.id}`);
      return { deleted: zone.id, name: zone.name };
    },
  });

  // ---------------------------------------------------------------- traffic rules (v2)

  defineTool(ctx, "unifi_list_traffic_rules", {
    toolset: "firewall",
    title: "List traffic rules",
    description: "List traffic management rules (block/allow/limit by app, domain, IP, region or internet) from the v2 API.",
    input: { ...siteArg, raw: rawArg },
    handler: async (a) => {
      const rules = await v2List(client, site(a.site), "trafficrules");
      return a.raw ? redactSecrets(rules) : rules.map(summarizeTrafficRule);
    },
  });

  defineTool(ctx, "unifi_save_traffic_rule", {
    toolset: "firewall",
    title: "Create or update traffic rule",
    description:
      "Create a traffic rule (action and matching_target required) or update one by id. Targets default to all clients on create. " +
      "matching_target picks what is matched: INTERNET (all traffic), DOMAIN (domains), APP (app_ids), APP_CATEGORY (app_category_ids), IP (ip_addresses), REGION (regions). " +
      "Setting a bandwidth limit makes it a rate-limiting rule.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: saveIdArg("Traffic rule"),
      description: z.string().optional().describe("Rule name/description"),
      enabled: z.boolean().optional(),
      action: z.enum(["BLOCK", "ALLOW"]).optional(),
      matching_target: z.enum(["INTERNET", "DOMAIN", "APP", "APP_CATEGORY", "IP", "REGION"]).optional(),
      ...targetArgs,
      domains: z.array(z.string()).optional().describe("Domains, e.g. ['example.com'] (matching_target=DOMAIN)"),
      ip_addresses: z.array(z.string()).optional().describe("IPs or CIDRs (matching_target=IP)"),
      app_ids: z.array(z.number().int()).optional().describe("DPI application IDs (matching_target=APP)"),
      app_category_ids: z.array(z.number().int()).optional().describe("DPI category IDs (matching_target=APP_CATEGORY)"),
      regions: z.array(z.string()).optional().describe("ISO country codes, e.g. ['CN','RU'] (matching_target=REGION)"),
      download_limit_kbps: z.number().int().positive().optional().describe("Download limit (enables the bandwidth limit)"),
      upload_limit_kbps: z.number().int().positive().optional().describe("Upload limit (enables the bandwidth limit)"),
      remove_bandwidth_limit: z.boolean().optional().describe("Disable the bandwidth limit"),
      schedule: z.looseObject({ mode: z.string() }).optional().describe("e.g. {mode:'ALWAYS'} (default) or {mode:'EVERY_DAY', time_range_start:'22:00', time_range_end:'06:00', ...}"),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      const patch: Obj = defined({
        description: a.description,
        enabled: a.enabled,
        action: a.action,
        matching_target: a.matching_target,
        target_devices: targetDevices(a.client_macs, a.network_ids, a.all_clients),
        domains: domainEntries(a.domains),
        ip_addresses: ipEntries(a.ip_addresses),
        app_ids: a.app_ids,
        app_category_ids: a.app_category_ids,
        regions: a.regions,
        schedule: a.schedule,
      });
      let current: Obj | undefined;
      if (a.id) current = await v2Get(client, s, "trafficrules", a.id);
      if (a.download_limit_kbps !== undefined || a.upload_limit_kbps !== undefined) {
        const prev = current?.bandwidth_limit ?? {};
        patch.bandwidth_limit = {
          ...prev,
          enabled: true,
          download_limit_kbps: a.download_limit_kbps ?? prev.download_limit_kbps ?? 1024,
          upload_limit_kbps: a.upload_limit_kbps ?? prev.upload_limit_kbps ?? 1024,
        };
      } else if (a.remove_bandwidth_limit) {
        patch.bandwidth_limit = { ...(current?.bandwidth_limit ?? { download_limit_kbps: 1024, upload_limit_kbps: 1024 }), enabled: false };
      }

      if (current) {
        const merged = { ...current, ...patch, ...a.extra };
        const saved = await client.v2<Obj>("PUT", s, `trafficrules/${encodeURIComponent(a.id!)}`, merged);
        return summarizeTrafficRule(saved && typeof saved === "object" && !Array.isArray(saved) ? saved : merged);
      }
      if (!a.action || !a.matching_target) throw new Error("action and matching_target are required to create a traffic rule");
      const body: Obj = {
        enabled: true,
        description: a.description ?? "",
        app_category_ids: [],
        app_ids: [],
        domains: [],
        ip_addresses: [],
        ip_ranges: [],
        network_ids: [],
        regions: [],
        target_devices: [{ type: "ALL_CLIENTS" }],
        bandwidth_limit: { enabled: false, download_limit_kbps: 1024, upload_limit_kbps: 1024 },
        schedule: { mode: "ALWAYS", repeat_on_days: [], time_all_day: false },
        ...patch,
        ...a.extra,
      };
      return summarizeTrafficRule(await v2Save(client, s, "trafficrules", undefined, body));
    },
  });

  defineTool(ctx, "unifi_delete_traffic_rule", {
    toolset: "firewall",
    title: "Delete traffic rule",
    description: "Delete a traffic management rule.",
    delete: true,
    input: { ...siteArg, id: idArg("Traffic rule") },
    handler: async (a) => {
      await v2Delete(client, site(a.site), "trafficrules", a.id);
      return { deleted: a.id };
    },
  });
}
