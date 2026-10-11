import { z } from "zod";
import { compact, duration, isoTime, normalizeMac } from "../unifi/format.js";
import { defineTool, limitArg, siteArg, type ToolContext } from "./util.js";

const REPORT_ATTRS: Record<string, string[]> = {
  site: ["bytes", "wan-tx_bytes", "wan-rx_bytes", "wlan_bytes", "num_sta", "lan-num_sta", "wlan-num_sta", "time"],
  user: ["rx_bytes", "tx_bytes", "time"],
  ap: ["bytes", "num_sta", "time"],
  gw: ["time", "mem", "cpu", "loadavg_5", "lan-rx_bytes", "lan-tx_bytes", "wan-rx_bytes", "wan-tx_bytes"],
};

const DEFAULT_LOOKBACK_HOURS: Record<string, number> = { "5minutes": 12, hourly: 24 * 7, daily: 24 * 30, monthly: 24 * 365 };

export function registerMonitoringTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  defineTool(ctx, "unifi_list_events", {
    toolset: "monitoring",
    title: "List recent events",
    description:
      "List recent controller events (client connects/roams, device restarts, upgrades...). Uses the classic event log and falls back to the v2 system log (device and client alerts) on Network versions where the classic log is gone or empty. For other log classes use unifi_get_system_log.",
    input: {
      ...siteArg,
      hours: z.number().int().positive().max(24 * 30).optional().describe("Look back this many hours (default 24)"),
      limit: limitArg(100),
    },
    handler: async (a) => {
      const s = site(a.site);
      const hours = a.hours ?? 24;
      const limit = a.limit ?? 100;
      let legacyError: string | undefined;
      try {
        const events: any[] = await client.sitePost(s, "stat/event", { within: hours, _limit: limit, _sort: "-time" });
        if (events.length) {
          return events.map((e) =>
            compact({ time: isoTime(e.time) ?? e.datetime, key: e.key, subsystem: e.subsystem, message: e.msg }),
          );
        }
      } catch (err) {
        legacyError = (err as Error).message;
      }
      const end = Date.now();
      const start = end - hours * 3600_000;
      const pages = await Promise.all(
        (["device-alert", "client-alert"] as const).map((cls) =>
          client
            .v2<any>("POST", s, `system-log/${cls}`, systemLogPayload(cls, start, end, 0, limit))
            .then((r) => logEntries(r))
            .catch(() => [] as any[]),
        ),
      );
      const entries = pages.flat().sort((x, y) => (y.timestamp ?? 0) - (x.timestamp ?? 0)).slice(0, limit);
      return {
        source: "v2 system-log (device-alert, client-alert)",
        legacy_error: legacyError,
        events: entries.map(summarizeLogEntry),
      };
    },
  });

  defineTool(ctx, "unifi_list_alarms", {
    toolset: "monitoring",
    title: "List alarms",
    description: "List controller alarms (alerts). By default only active (unarchived) alarms are returned.",
    input: {
      ...siteArg,
      include_archived: z.boolean().optional().describe("Include archived alarms"),
      limit: limitArg(100),
    },
    handler: async (a) => {
      let alarms: any[] = await client.siteGet(site(a.site), "list/alarm");
      if (!a.include_archived) alarms = alarms.filter((x) => !x.archived);
      alarms.sort((x, y) => (y.time ?? 0) - (x.time ?? 0));
      return alarms.slice(0, a.limit ?? 100).map((x) =>
        compact({
          id: x._id,
          time: isoTime(x.time) ?? x.datetime,
          key: x.key,
          subsystem: x.subsystem,
          message: x.msg,
          archived: x.archived || undefined,
        }),
      );
    },
  });

  defineTool(ctx, "unifi_archive_alarms", {
    toolset: "monitoring",
    title: "Archive alarms",
    description: "Archive one alarm by ID, or all alarms when no ID is given.",
    write: true,
    input: { ...siteArg, id: z.string().optional().describe("Alarm ID; omit to archive all alarms") },
    handler: async (a) => {
      if (a.id) await client.cmd(site(a.site), "evtmgr", "archive-alarm", { _id: a.id });
      else await client.cmd(site(a.site), "evtmgr", "archive-all-alarms");
      return { archived: a.id ?? "all" };
    },
  });

  defineTool(ctx, "unifi_get_traffic_report", {
    toolset: "monitoring",
    title: "Get traffic/usage report",
    description:
      "Get historical time-series statistics. scope=site for whole-site traffic and client counts, gw for gateway CPU/memory/WAN traffic, ap for per-AP traffic, user for per-client traffic (pass macs).",
    input: {
      ...siteArg,
      scope: z.enum(["site", "gw", "ap", "user"]).describe("What to report on"),
      interval: z.enum(["5minutes", "hourly", "daily", "monthly"]).optional().describe("Bucket size (default hourly)"),
      hours: z
        .number()
        .positive()
        .optional()
        .describe("Look back this many hours (default: 12 for 5minutes, 168 for hourly, 720 for daily, 8760 for monthly)"),
      macs: z.array(z.string()).optional().describe("Limit to these device/client MACs (ap and user scopes)"),
      attrs: z.array(z.string()).optional().describe("Override the attributes to fetch, e.g. ['rx_bytes','tx_bytes','time']"),
    },
    handler: async (a) => {
      const interval = a.interval ?? "hourly";
      const end = Date.now();
      const start = end - (a.hours ?? DEFAULT_LOOKBACK_HOURS[interval]) * 3600_000;
      const attrs = a.attrs ?? REPORT_ATTRS[a.scope];
      if (!attrs.includes("time")) attrs.push("time");
      const body: Record<string, unknown> = { attrs, start, end };
      if (a.macs?.length) body.macs = a.macs.map(normalizeMac);
      const rows: any[] = await client.sitePost(site(a.site), `stat/report/${interval}.${a.scope}`, body);
      return rows.map(({ oid, o, site_id, ...row }) => compact({ ...row, time: isoTime(row.time), oid: a.scope === "site" ? undefined : oid }));
    },
  });

  defineTool(ctx, "unifi_get_dpi_stats", {
    toolset: "monitoring",
    title: "Get DPI statistics",
    description:
      "Get deep packet inspection (traffic identification) statistics, by application or by category: site-wide, or per client when macs are given. Requires traffic identification to be enabled.",
    input: {
      ...siteArg,
      by: z.enum(["app", "category"]).optional().describe("Group by application or category (default category)"),
      macs: z.array(z.string()).optional().describe("Client MACs to report on instead of the whole site"),
    },
    handler: async (a) => {
      const type = a.by === "app" ? "by_app" : "by_cat";
      if (a.macs?.length) return client.sitePost(site(a.site), "stat/stadpi", { type, macs: a.macs.map(normalizeMac) });
      return client.sitePost(site(a.site), "stat/sitedpi", { type });
    },
  });

  defineTool(ctx, "unifi_get_speedtest_results", {
    toolset: "monitoring",
    title: "Get speed test history",
    description: "Get historical WAN speed test results run by the gateway.",
    input: {
      ...siteArg,
      hours: z.number().positive().optional().describe("Look back this many hours (default 168)"),
    },
    handler: async (a) => {
      const end = Date.now();
      const rows: any[] = await client.sitePost(site(a.site), "stat/report/archive.speedtest", {
        attrs: ["xput_download", "xput_upload", "latency", "rundate", "server", "time"],
        start: end - (a.hours ?? 168) * 3600_000,
        end,
      });
      return rows.map((r) =>
        compact({
          time: isoTime(r.time ?? r.rundate),
          download_mbps: r.xput_download,
          upload_mbps: r.xput_upload,
          latency_ms: r.latency,
          server: r.server?.city ?? r.server?.name,
        }),
      );
    },
  });

  defineTool(ctx, "unifi_run_speedtest", {
    toolset: "monitoring",
    title: "Run WAN speed test",
    description: "Start a WAN speed test on the gateway. Results appear in unifi_get_speedtest_results after a minute or so.",
    write: true,
    input: { ...siteArg },
    handler: async (a) => {
      await client.cmd(site(a.site), "devmgr", "speedtest");
      return { started: true };
    },
  });

  defineTool(ctx, "unifi_list_rogue_aps", {
    toolset: "monitoring",
    title: "List neighbouring/rogue APs",
    description: "List nearby access points seen by your APs (neighbours and potential rogue APs).",
    input: {
      ...siteArg,
      hours: z.number().int().positive().optional().describe("Seen within this many hours (default 24)"),
      rogue_only: z.boolean().optional().describe("Only APs flagged as rogue"),
      limit: limitArg(100),
    },
    handler: async (a) => {
      let aps: any[] = await client.sitePost(site(a.site), "stat/rogueap", { within: a.hours ?? 24 });
      if (a.rogue_only) aps = aps.filter((x) => x.is_rogue);
      aps.sort((x, y) => (y.rssi ?? -999) - (x.rssi ?? -999));
      return aps.slice(0, a.limit ?? 100).map((x) =>
        compact({
          ssid: x.essid,
          bssid: x.bssid,
          channel: x.channel,
          band: x.band ?? x.radio,
          signal_dbm: x.signal,
          rssi: x.rssi,
          security: x.security,
          vendor: x.oui,
          seen_by_ap: x.ap_mac,
          rogue: x.is_rogue || undefined,
          last_seen: isoTime(x.last_seen),
        }),
      );
    },
  });

  defineTool(ctx, "unifi_get_system_log", {
    toolset: "monitoring",
    title: "Get system log",
    description:
      "Read the v2 system log (Network 8+), one class at a time, paged: device-alert, client-alert, admin-activity (logins and config changes), update-alert, threat-alert (IPS/honeypot), vpn-alert, triggers (traffic/firewall rule hits), next-ai-alert.",
    input: {
      ...siteArg,
      class: z
        .enum(["device-alert", "client-alert", "admin-activity", "update-alert", "threat-alert", "vpn-alert", "triggers", "next-ai-alert"])
        .optional()
        .describe("Log class (default device-alert)"),
      hours: z.number().positive().max(24 * 90).optional().describe("Look back this many hours (default 168)"),
      page: z.number().int().min(0).optional().describe("Page number, from 0"),
      page_size: z.number().int().min(1).max(500).optional().describe("Entries per page (default 50)"),
      raw: z.boolean().optional().describe("Return raw entries"),
    },
    handler: async (a) => {
      const cls = a.class ?? "device-alert";
      const end = Date.now();
      const start = end - (a.hours ?? 168) * 3600_000;
      const res = await client.v2<any>("POST", site(a.site), `system-log/${cls}`, systemLogPayload(cls, start, end, a.page ?? 0, a.page_size ?? 50));
      const entries = logEntries(res);
      const total = res?.totalElementCount ?? res?.total_element_count ?? res?.total;
      return compact({
        class: cls,
        page: a.page ?? 0,
        total,
        entries: a.raw ? entries : entries.map(summarizeLogEntry),
      });
    },
  });

  defineTool(ctx, "unifi_list_client_sessions", {
    toolset: "monitoring",
    title: "List client sessions",
    description: "List client connection sessions (association history): when each client connected, for how long, to which AP/network, and traffic used.",
    input: {
      ...siteArg,
      mac: z.string().optional().describe("Only sessions for this client"),
      type: z.enum(["all", "user", "guest"]).optional().describe("Client type (default all)"),
      hours: z.number().positive().max(24 * 90).optional().describe("Look back this many hours (default 168)"),
      limit: limitArg(100),
    },
    handler: async (a) => {
      const end = Math.floor(Date.now() / 1000);
      const body: Record<string, unknown> = { type: a.type ?? "all", start: end - Math.round((a.hours ?? 168) * 3600), end };
      if (a.mac) body.mac = normalizeMac(a.mac);
      const sessions: any[] = await client.sitePost(site(a.site), "stat/session", body);
      sessions.sort((x, y) => (y.assoc_time ?? 0) - (x.assoc_time ?? 0));
      return sessions.slice(0, a.limit ?? 100).map((x) =>
        compact({
          mac: x.mac,
          name: x.name ?? x.hostname,
          ip: x.ip,
          connected: isoTime(x.assoc_time),
          disconnected: isoTime(x.disassoc_time),
          duration: duration(x.duration),
          ap_mac: x.ap_mac,
          ssid: x.essid,
          network: x.network,
          guest: x.is_guest || undefined,
          rx_bytes: x.rx_bytes,
          tx_bytes: x.tx_bytes,
        }),
      );
    },
  });

  defineTool(ctx, "unifi_list_guest_authorizations", {
    toolset: "monitoring",
    title: "List guest authorizations",
    description: "List hotspot/guest portal authorizations (who was authorized, how, when and until when).",
    input: {
      ...siteArg,
      hours: z.number().positive().max(24 * 365).optional().describe("Look back this many hours (default 168)"),
      limit: limitArg(100),
    },
    handler: async (a) => {
      const end = Math.floor(Date.now() / 1000);
      const auths: any[] = await client.sitePost(site(a.site), "stat/authorization", {
        start: end - Math.round((a.hours ?? 168) * 3600),
        end,
      });
      auths.sort((x, y) => (y.start ?? 0) - (x.start ?? 0));
      return auths.slice(0, a.limit ?? 100).map((x) =>
        compact({
          id: x._id,
          mac: x.mac,
          ip: x.ip,
          method: x.authorized_by,
          voucher: x.voucher_code,
          start: isoTime(x.start),
          end: isoTime(x.end),
          rx_bytes: x.rx_bytes,
          tx_bytes: x.tx_bytes,
          expired: x.expired || undefined,
        }),
      );
    },
  });

  defineTool(ctx, "unifi_list_anomalies", {
    toolset: "monitoring",
    title: "List anomalies",
    description: "List anomalies the controller detected (e.g. high latency, DNS timeouts, poor WiFi experience) per client/device. Not available on every Network version.",
    input: {
      ...siteArg,
      hours: z.number().positive().max(24 * 30).optional().describe("Look back this many hours (default 24)"),
      limit: limitArg(100),
    },
    handler: async (a) => {
      const end = Date.now();
      const rows: any[] = await client.sitePost(site(a.site), "stat/anomalies", { start: end - (a.hours ?? 24) * 3600_000, end });
      // Row shape varies by version, so rows are returned as-is apart from readable timestamps.
      return rows.slice(0, a.limit ?? 100).map(({ time, ...r }) => compact({ time: isoTime(time) ?? time, ...r }));
    },
  });

  defineTool(ctx, "unifi_get_dashboard", {
    toolset: "monitoring",
    title: "Get dashboard metrics",
    description:
      "Get the aggregated dashboard the UniFi UI shows: WAN/ISP quality history (latency, packet loss, availability, throughput), WiFi experience and client mix. v2 API, Network 7.4+; large response.",
    input: {
      ...siteArg,
      hours: z.number().positive().max(24 * 30).optional().describe("History window in hours (default 24)"),
    },
    handler: async (a) =>
      client.v2("GET", site(a.site), `aggregated-dashboard?historySeconds=${Math.round((a.hours ?? 24) * 3600)}`),
  });
}

/** Request body for /v2/api/site/<site>/system-log/<class>, per the class-specific filters the UI sends. */
function systemLogPayload(cls: string, start: number, end: number, pageNumber: number, pageSize: number): Record<string, unknown> {
  const payload: Record<string, unknown> = { pageNumber, pageSize, timestampFrom: start, timestampTo: end };
  switch (cls) {
    case "next-ai-alert":
      payload.nextAiCategory = ["CLIENT", "DEVICE", "INTERNET", "VPN"];
      break;
    case "admin-activity":
      payload.activity_keys = ["ACCESSED_NETWORK_WEB", "ACCESSED_NETWORK_IOS", "ACCESSED_NETWORK_ANDROID"];
      payload.change_keys = ["CLIENT", "DEVICE", "HOTSPOT", "INTERNET", "NETWORK", "PROFILE", "ROUTING", "SECURITY", "SYSTEM", "VPN", "WIFI"];
      break;
    case "update-alert":
      payload.systemLogDeviceTypes = ["GATEWAYS", "SWITCHES", "ACCESS_POINT", "SMART_POWER", "BUILDING_TO_BUILDING_BRIDGES", "UNIFI_LTE"];
      break;
    case "client-alert":
      payload.clientType = ["GUEST", "TELEPORT", "VPN", "WIRELESS", "RADIUS", "WIRED"];
      payload.guestAuthorizationMethod = ["FACEBOOK_SOCIAL_GATEWAY", "FREE_TRIAL", "GOOGLE_SOCIAL_GATEWAY", "NONE", "PASSWORD", "PAYMENT", "RADIUS", "VOUCHER"];
      break;
    case "threat-alert":
      payload.threatTypes = ["HONEYPOT", "THREAT"];
      break;
    case "triggers":
      payload.triggerTypes = ["TRAFFIC_RULE", "TRAFFIC_ROUTE", "FIREWALL_RULE"];
      break;
  }
  return payload;
}

function logEntries(res: any): any[] {
  if (Array.isArray(res)) return res;
  return res?.data ?? res?.logs ?? [];
}

function summarizeLogEntry(e: any): Record<string, unknown> {
  const params = e.parameters ?? {};
  return compact({
    time: isoTime(e.timestamp ?? e.time),
    key: e.key ?? e.type,
    category: e.category,
    severity: e.severity,
    title: e.title?.text ?? e.title,
    message: e.message?.text ?? e.message ?? e.msg ?? e.message_raw,
    device: params.DEVICE?.name ?? params.DEVICE?.id,
    client: params.CLIENT?.name ?? params.CLIENT?.hostname ?? params.CLIENT?.id,
  });
}
