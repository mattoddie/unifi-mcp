import { z } from "zod";
import { compact, isoTime } from "../unifi/format.js";
import { updateSetting, v2List } from "./rest.js";
import { defineTool, defined, extraArg, idArg, limitArg, siteArg, type ToolContext } from "./util.js";

type Obj = Record<string, any>;

const GEO_FIELDS = [
  "geo_ip_filtering_enabled",
  "geo_ip_filtering_block",
  "geo_ip_filtering_countries",
  "geo_ip_filtering_traffic_direction",
];

function summarizeIps(ips: Obj): Obj {
  return compact({
    ips_mode: ips.ips_mode,
    enabled_categories: ips.enabled_categories,
    enabled_networks: ips.enabled_networks,
    advanced_filtering_preference: ips.advanced_filtering_preference,
    memory_optimized: ips.memory_optimized,
    honeypot_enabled: ips.honeypot_enabled,
    dns_filtering: ips.dns_filtering,
    ad_blocking_enabled: ips.ad_blocking_enabled,
    restrict_tor: ips.restrict_tor,
    restrict_torrents: ips.restrict_torrents,
  });
}

function summarizeContentFilter(f: Obj): Obj {
  return compact({
    id: f._id ?? f.id,
    name: f.name,
    enabled: f.enabled,
    blocked_categories: f.blocked_categories ?? f.categories,
    safe_search: f.safe_search,
    network_ids: f.network_ids,
    client_macs: f.client_macs,
    allow_list: f.allow_list,
    block_list: f.block_list,
    schedule: f.schedule,
  });
}

export function registerSecurityTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  defineTool(ctx, "unifi_get_security_settings", {
    toolset: "security",
    title: "Get security settings",
    description:
      "Get threat management (IDS/IPS mode, categories, protected networks), traffic identification (DPI) and country/region blocking (geo-IP) settings. Optionally list the IPS categories the controller supports (Network 8+).",
    input: {
      ...siteArg,
      include_categories: z
        .boolean()
        .optional()
        .describe("Also fetch the catalog of available IPS categories (v2 API, Network 8+)"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const all: Obj[] = await client.siteGet(s, "rest/setting");
      const byKey = (k: string) => all.find((x) => x.key === k);
      const ips = byKey("ips");
      const dpi = byKey("dpi");
      const usg = byKey("usg");
      const geo = usg ? compact(Object.fromEntries(GEO_FIELDS.map((f) => [f.replace("geo_ip_filtering_", ""), usg[f]]))) : undefined;
      const result: Obj = {
        threat_management: ips ? summarizeIps(ips) : "not available (no 'ips' setting; requires a UniFi gateway)",
        traffic_identification: dpi
          ? compact({ enabled: dpi.enabled, device_fingerprinting: dpi.fingerprintingEnabled })
          : undefined,
        country_blocking: geo && Object.keys(geo).length ? geo : undefined,
      };
      if (a.include_categories) {
        const memory = ips?.memory_optimized === true;
        result.available_ips_categories = await client
          .v2("GET", s, `settings/ips/available-categories?memoryOptimized=${memory}`)
          .catch((err: Error) => `unavailable: ${err.message}`);
      }
      return result;
    },
  });

  defineTool(ctx, "unifi_update_threat_management", {
    toolset: "security",
    title: "Update threat management / DPI",
    description:
      "Change IDS/IPS (threat management) and traffic identification settings. Only the fields given are changed. ips_mode: disabled, ids (detect only), ips (detect and block), ipsInline (inline blocking, newer gateways). Changing IPS mode can briefly interrupt traffic and raise gateway CPU load.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      ips_mode: z.enum(["disabled", "ids", "ips", "ipsInline"]).optional().describe("Threat management mode"),
      enabled_categories: z
        .array(z.string())
        .optional()
        .describe("IPS signature categories to enable (see unifi_get_security_settings include_categories)"),
      enabled_networks: z.array(z.string()).optional().describe("Network IDs protected by threat management"),
      dpi_enabled: z.boolean().optional().describe("Traffic identification (DPI)"),
      device_fingerprinting: z.boolean().optional().describe("Device identification / fingerprinting"),
      extra: extraArg.describe("Additional raw fields for the 'ips' setting (e.g. honeypot or ad blocking keys on your version)"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const ipsPatch = defined({
        ips_mode: a.ips_mode,
        enabled_categories: a.enabled_categories,
        enabled_networks: a.enabled_networks,
        ...(a.extra ?? {}),
      });
      if (a.ips_mode === "disabled" && !a.enabled_categories) ipsPatch.enabled_categories = [];
      const dpiPatch = defined({ enabled: a.dpi_enabled, fingerprintingEnabled: a.device_fingerprinting });
      if (!Object.keys(ipsPatch).length && !Object.keys(dpiPatch).length) throw new Error("Nothing to update");
      const out: Obj = {};
      if (Object.keys(ipsPatch).length) out.threat_management = summarizeIps(await updateSetting(client, s, "ips", ipsPatch));
      if (Object.keys(dpiPatch).length) {
        const dpi = await updateSetting(client, s, "dpi", dpiPatch);
        out.traffic_identification = compact({ enabled: dpi.enabled, device_fingerprinting: dpi.fingerprintingEnabled });
      }
      return out;
    },
  });

  defineTool(ctx, "unifi_update_country_blocking", {
    toolset: "security",
    title: "Update country/region blocking",
    description:
      "Configure geo-IP (country/region) blocking on the gateway (stored in the 'usg' site setting). Field names are version-dependent: on some Network 9+ releases region blocking moved into the zone-based firewall UI; check unifi_get_security_settings first.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      enabled: z.boolean().optional().describe("Turn country blocking on or off"),
      mode: z.enum(["block", "allow"]).optional().describe("block = block listed countries; allow = allow only listed countries"),
      countries: z.array(z.string().length(2)).optional().describe("ISO 3166-1 alpha-2 country codes, e.g. ['CN','RU']"),
      direction: z.enum(["both", "ingress", "egress"]).optional().describe("Traffic direction to filter"),
    },
    handler: async (a) => {
      const patch = defined({
        geo_ip_filtering_enabled: a.enabled,
        geo_ip_filtering_block: a.mode,
        geo_ip_filtering_countries: a.countries?.map((c) => c.toUpperCase()).join(","),
        geo_ip_filtering_traffic_direction: a.direction,
      });
      if (!Object.keys(patch).length) throw new Error("Nothing to update");
      const usg = await updateSetting(client, site(a.site), "usg", patch);
      return compact(Object.fromEntries(GEO_FIELDS.map((f) => [f.replace("geo_ip_filtering_", ""), usg[f]])));
    },
  });

  defineTool(ctx, "unifi_list_threats", {
    toolset: "security",
    title: "List detected threats",
    description: "List IDS/IPS threat events detected by the gateway (newest first): signature, category, source/destination, action taken.",
    input: {
      ...siteArg,
      hours: z.number().positive().max(24 * 90).optional().describe("Look back this many hours (default 24)"),
      limit: limitArg(100),
    },
    handler: async (a) => {
      const end = Date.now();
      const events: Obj[] = await client.sitePost(site(a.site), "stat/ips/event", {
        start: end - (a.hours ?? 24) * 3600_000,
        end,
        _limit: a.limit ?? 100,
      });
      events.sort((x, y) => (y.timestamp ?? y.time ?? 0) - (x.timestamp ?? x.time ?? 0));
      return events.slice(0, a.limit ?? 100).map((e) =>
        compact({
          time: isoTime(e.timestamp ?? e.time) ?? e.datetime,
          signature: e.inner_alert_signature ?? e.msg,
          category: e.inner_alert_category ?? e.catname,
          severity: e.inner_alert_severity,
          action: e.inner_alert_action,
          protocol: e.proto,
          app_protocol: e.app_proto,
          src: compact({ ip: e.src_ip, port: e.src_port, mac: e.src_mac, country: e.srcipCountry }),
          dst: compact({ ip: e.dest_ip ?? e.dst_ip, port: e.dest_port ?? e.dst_port, mac: e.dst_mac, country: e.dstipCountry }),
          host: e.host,
        }),
      );
    },
  });

  // ---------------------------------------------------------------- content filtering (v2)

  defineTool(ctx, "unifi_list_content_filters", {
    toolset: "security",
    title: "List content filters",
    description: "List content filtering profiles (blocked categories, safe search, target networks/clients). v2 API, Network 8.1+.",
    input: { ...siteArg, raw: z.boolean().optional().describe("Return the full controller objects") },
    handler: async (a) => {
      const filters = await v2List(client, site(a.site), "content-filtering");
      return a.raw ? filters : filters.map(summarizeContentFilter);
    },
  });

  defineTool(ctx, "unifi_save_content_filter", {
    toolset: "security",
    title: "Create/update content filter",
    description:
      "Create a content filtering profile (no id) or update one (id; only given fields change). v2 API, Network 8.1+.",
    write: true,
    input: {
      ...siteArg,
      id: idArg("Content filter").optional(),
      name: z.string().optional(),
      enabled: z.boolean().optional(),
      blocked_categories: z
        .array(z.string())
        .optional()
        .describe("Category identifiers as used by the controller (see raw output of unifi_list_content_filters)"),
      safe_search: z.array(z.string()).optional().describe("Search engines to force safe search on, e.g. ['GOOGLE','BING','YOUTUBE']"),
      network_ids: z.array(z.string()).optional().describe("Networks the filter applies to"),
      client_macs: z.array(z.string()).optional().describe("Clients the filter applies to"),
      allow_list: z.array(z.string()).optional().describe("Domains always allowed"),
      block_list: z.array(z.string()).optional().describe("Domains always blocked"),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      const fields = defined({
        name: a.name,
        enabled: a.enabled,
        blocked_categories: a.blocked_categories,
        safe_search: a.safe_search,
        network_ids: a.network_ids,
        client_macs: a.client_macs?.map((m) => m.toLowerCase()),
        allow_list: a.allow_list,
        block_list: a.block_list,
        ...(a.extra ?? {}),
      });
      if (a.id) {
        const current = (await v2List(client, s, "content-filtering")).find((f) => (f._id ?? f.id) === a.id);
        if (!current) throw new Error(`No content filter with id ${a.id} on site ${s}`);
        const updated = await client.v2("PUT", s, `content-filtering/${encodeURIComponent(a.id)}`, { ...current, ...fields });
        return summarizeContentFilter(updated ?? { ...current, ...fields });
      }
      if (!a.name) throw new Error("name is required when creating a content filter");
      const body = {
        enabled: true,
        blocked_categories: [],
        safe_search: [],
        network_ids: [],
        client_macs: [],
        schedule: { mode: "ALWAYS", repeat_on_days: [], time_all_day: true },
        ...fields,
      };
      const created = await client.v2("POST", s, "content-filtering/create", body);
      return summarizeContentFilter(created ?? body);
    },
  });

  defineTool(ctx, "unifi_delete_content_filter", {
    toolset: "security",
    title: "Delete content filter",
    description: "Delete a content filtering profile.",
    delete: true,
    input: { ...siteArg, id: idArg("Content filter") },
    handler: async (a) => {
      await client.v2("DELETE", site(a.site), `content-filtering/${encodeURIComponent(a.id)}`);
      return { deleted: a.id };
    },
  });
}
