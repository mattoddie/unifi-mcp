import { compact } from "../unifi/format.js";
import { defineTool, siteArg, type ToolContext } from "./util.js";

export function registerOverviewTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  defineTool(ctx, "unifi_list_sites", {
    toolset: "overview",
    title: "List sites",
    description: "List the UniFi sites on the controller. The `name` field is the site ID used by the `site` argument of other tools.",
    input: {},
    handler: async () => {
      const sites: any[] = await client.request("GET", "/api/self/sites");
      return sites.map((s) =>
        compact({ id: s._id, name: s.name, description: s.desc, role: s.role, device_count: s.device_count }),
      );
    },
  });

  defineTool(ctx, "unifi_get_system_info", {
    toolset: "overview",
    title: "Get controller system info",
    description: "Get controller/Network application version, hostname, timezone, uptime and update availability.",
    input: { ...siteArg },
    handler: async (a) => {
      const [info]: any[] = await client.siteGet(site(a.site), "stat/sysinfo");
      return info ?? {};
    },
  });

  defineTool(ctx, "unifi_get_site_health", {
    toolset: "overview",
    title: "Get site health",
    description:
      "Get the health of each subsystem (WAN, LAN, WLAN, VPN, www) for a site: status, ISP, WAN IP, latency, throughput, device and client counts.",
    input: { ...siteArg },
    handler: async (a) => {
      const subsystems: any[] = await client.siteGet(site(a.site), "stat/health");
      return subsystems.map(({ "gw_system-stats": gwStats, ...rest }) => compact({ ...rest, gw_system_stats: gwStats }));
    },
  });
}
