import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { GetPromptResult } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";

const siteArg = { site: z.string().optional().describe("Site name (defaults to the configured site)") };

function userPrompt(text: string): GetPromptResult {
  return { messages: [{ role: "user", content: { type: "text", text } }] };
}

function onSite(site?: string): string {
  return site ? ` Use site "${site}" for every tool call.` : "";
}

/** Reusable MCP prompts that chain several tools together. */
export function registerPrompts(server: McpServer): void {
  server.registerPrompt(
    "network_health_check",
    {
      title: "Network health check",
      description: "Overall health review of a UniFi site: WAN, devices, clients, alarms and recent problems.",
      argsSchema: siteArg,
    },
    ({ site }) =>
      userPrompt(
        `Do a health check of my UniFi network.${onSite(site)}
1. unifi_get_site_health: status of WAN/LAN/WLAN/VPN, ISP, latency, throughput.
2. unifi_list_devices: anything not "connected", high CPU/memory, firmware upgrades available, recent reboots (short uptime).
3. unifi_list_alarms and unifi_list_events (last 24h): recurring or serious issues.
4. unifi_list_clients (connection: wireless): clients with weak signal (below -70 dBm) or low satisfaction.
5. unifi_get_speedtest_results if available.
Report: a one-line verdict, then problems ordered by severity, each with evidence and a suggested fix. Keep healthy areas to one line each. Don't change anything.`,
      ),
  );

  server.registerPrompt(
    "troubleshoot_client",
    {
      title: "Troubleshoot a client",
      description: "Investigate why a specific client (by MAC, name or IP) has connectivity problems.",
      argsSchema: { client: z.string().describe("Client MAC address, name, hostname or IP"), ...siteArg },
    },
    ({ client, site }) =>
      userPrompt(
        `Troubleshoot connectivity for the client "${client}" on my UniFi network.${onSite(site)}
1. Find it: unifi_list_clients (search) if online, otherwise unifi_list_known_clients; then unifi_get_client by MAC.
2. Check: online state, IP and network/VLAN, fixed IP, blocked/guest status, signal/satisfaction/rates (wireless) or switch port (wired).
3. The AP or switch it uses: unifi_get_device (port errors/speed, radio channel utilisation, uptime).
4. History: unifi_list_client_sessions for this MAC (frequent reconnects?), unifi_list_events.
5. If relevant: unifi_list_firewall_rules / unifi_list_traffic_rules / unifi_list_content_filters for rules affecting it.
Report the most likely cause(s) with evidence and concrete next steps. Ask before making any change.`,
      ),
  );

  server.registerPrompt(
    "security_review",
    {
      title: "Security review",
      description: "Review the site's security posture: threat management, firewall, port forwards, WiFi security and admins.",
      argsSchema: siteArg,
    },
    ({ site }) =>
      userPrompt(
        `Review the security of my UniFi network.${onSite(site)}
Check:
- unifi_get_security_settings: is IDS/IPS on, in which mode, on which networks; country blocking.
- unifi_list_threats (last 7 days, hours: 168): notable signatures, repeat offenders, internal hosts involved.
- unifi_list_port_forwards: exposed services, forwards open to any source.
- unifi_list_firewall_rules and unifi_list_firewall_policies: overly broad allow rules, missing inter-VLAN isolation.
- unifi_list_wlans: open or WPA2-only networks where WPA3 is possible, guest networks without isolation.
- unifi_list_networks: guest/IoT segmentation.
- unifi_list_admins: number of admins and roles.
- unifi_get_system_log (class: admin-activity): recent logins and changes.
Report findings as High / Medium / Low with the reason and a specific remediation. Don't change anything.`,
      ),
  );

  server.registerPrompt(
    "firmware_review",
    {
      title: "Firmware review",
      description: "List devices and the controller with available updates and propose a safe upgrade order.",
      argsSchema: siteArg,
    },
    ({ site }) =>
      userPrompt(
        `Review firmware on my UniFi network.${onSite(site)}
1. unifi_check_updates: controller version and devices with upgrades.
2. unifi_list_devices: model, current firmware, uptime and client count for each device needing an update.
Propose an upgrade plan that minimises disruption: access points first (one at a time where they overlap), then switches from the edge inwards, gateway last; note which upgrades will drop clients. Don't start any upgrade unless I confirm.`,
      ),
  );

  server.registerPrompt(
    "wifi_optimization",
    {
      title: "WiFi optimisation",
      description: "Analyse WiFi channels, utilisation, interference and client experience and suggest improvements.",
      argsSchema: siteArg,
    },
    ({ site }) =>
      userPrompt(
        `Analyse and suggest improvements for the WiFi on my UniFi network.${onSite(site)}
1. unifi_list_devices (type: access_point), then unifi_get_device for each AP: channel, width, transmit power, channel utilisation, satisfaction, client count per radio.
2. unifi_list_rogue_aps: strongest neighbouring networks per channel.
3. unifi_list_clients (connection: wireless): weak-signal and low-rate clients, clients stuck on 2.4 GHz.
4. unifi_list_wlans: number of SSIDs, bands, minimum data rates.
Recommend specific changes (channel plan, widths, power levels, SSID consolidation, band steering) with the reason for each, ordered by expected impact. Don't apply changes unless I confirm.`,
      ),
  );
}
