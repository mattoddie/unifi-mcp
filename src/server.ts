import { readFileSync } from "node:fs";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { ToolOptions } from "./config.js";
import { registerPrompts } from "./prompts.js";
import { registerAdminTools } from "./tools/admin.js";
import { registerClientsTools } from "./tools/clients.js";
import { registerDevicesTools } from "./tools/devices.js";
import { registerFirewallTools } from "./tools/firewall.js";
import { registerHotspotTools } from "./tools/hotspot.js";
import { registerMonitoringTools } from "./tools/monitoring.js";
import { registerNetworksTools } from "./tools/networks.js";
import { registerOverviewTools } from "./tools/overview.js";
import { registerRawTools } from "./tools/raw.js";
import { registerRoutingTools } from "./tools/routing.js";
import { registerSecurityTools } from "./tools/security.js";
import { registerSwitchingTools } from "./tools/switching.js";
import { registerVpnTools } from "./tools/vpn.js";
import { registerWifiTools } from "./tools/wifi.js";
import type { UnifiClient } from "./unifi/client.js";

export const SERVER_NAME = "unifi-mcp";
/** The package version; release builds stamp it from the Git tag. */
export const SERVER_VERSION: string = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")).version;

export function createServer(client: UnifiClient, options: ToolOptions): McpServer {
  const server = new McpServer(
    { name: SERVER_NAME, version: SERVER_VERSION },
    {
      instructions:
        "Tools for monitoring and managing a Ubiquiti UniFi Network controller. " +
        "Start with unifi_get_site_health, unifi_list_devices or unifi_list_clients. " +
        "Most tools take an optional `site`; use unifi_list_sites to find site names. " +
        "unifi_save_* tools create an object when no id is given and update it (changing only the fields passed) when an id is given. " +
        (options.allowWrites
          ? "Write tools are enabled: confirm with the user before restarting devices, blocking clients or changing configuration."
          : "The server is read-only; write tools are disabled (UNIFI_ALLOW_WRITES=false)."),
    },
  );
  const ctx = { server, client, ...options };
  registerOverviewTools(ctx);
  registerDevicesTools(ctx);
  registerClientsTools(ctx);
  registerSwitchingTools(ctx);
  registerWifiTools(ctx);
  registerNetworksTools(ctx);
  registerFirewallTools(ctx);
  registerRoutingTools(ctx);
  registerSecurityTools(ctx);
  registerVpnTools(ctx);
  registerHotspotTools(ctx);
  registerMonitoringTools(ctx);
  registerAdminTools(ctx);
  registerRawTools(ctx);
  registerPrompts(server);
  return server;
}
