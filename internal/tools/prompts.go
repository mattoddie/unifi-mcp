package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type prompt struct {
	name, title, description string
	args                     []*mcp.PromptArgument
	text                     func(args map[string]string) string
}

func promptSiteArg() *mcp.PromptArgument {
	return &mcp.PromptArgument{Name: "site", Description: "Site name (defaults to the configured site)"}
}

// onSite is the sentence pinning a prompt to one site, if one was given.
func onSite(a map[string]string) string {
	if site := a["site"]; site != "" {
		return fmt.Sprintf(` Use site "%s" for every tool call.`, site)
	}
	return ""
}

// prompts are reusable requests that chain several tools together.
var prompts = []prompt{
	{
		name: "network_health_check", title: "Network health check",
		description: "Overall health review of a UniFi site: WAN, devices, clients, alarms and recent problems.",
		args:        []*mcp.PromptArgument{promptSiteArg()},
		text: func(a map[string]string) string {
			return `Do a health check of my UniFi network.` + onSite(a) + `
1. unifi_get_site_health: status of WAN/LAN/WLAN/VPN, ISP, latency, throughput.
2. unifi_list_devices: anything not "connected", high CPU/memory, firmware upgrades available, recent reboots (short uptime).
3. unifi_list_alarms and unifi_list_events (last 24h): recurring or serious issues.
4. unifi_list_clients (connection: wireless): clients with weak signal (below -70 dBm) or low satisfaction.
5. unifi_get_speedtest_results if available.
Report: a one-line verdict, then problems ordered by severity, each with evidence and a suggested fix. Keep healthy areas to one line each. Don't change anything.`
		},
	},
	{
		name: "troubleshoot_client", title: "Troubleshoot a client",
		description: "Investigate why a specific client (by MAC, name or IP) has connectivity problems.",
		args: []*mcp.PromptArgument{
			{Name: "client", Description: "Client MAC address, name, hostname or IP", Required: true},
			promptSiteArg(),
		},
		text: func(a map[string]string) string {
			return `Troubleshoot connectivity for the client "` + a["client"] + `" on my UniFi network.` + onSite(a) + `
1. Find it: unifi_list_clients (search) if online, otherwise unifi_list_known_clients; then unifi_get_client by MAC.
2. Check: online state, IP and network/VLAN, fixed IP, blocked/guest status, signal/satisfaction/rates (wireless) or switch port (wired).
3. The AP or switch it uses: unifi_get_device (port errors/speed, radio channel utilisation, uptime).
4. History: unifi_list_client_sessions for this MAC (frequent reconnects?), unifi_list_events.
5. If relevant: unifi_list_firewall_rules / unifi_list_traffic_rules / unifi_list_qos_rules / unifi_list_oon_policies / unifi_list_content_filters for rules affecting it, and unifi_list_traffic_flows (client_mac, action: blocked) for traffic being blocked.
Report the most likely cause(s) with evidence and concrete next steps. Ask before making any change.`
		},
	},
	{
		name: "security_review", title: "Security review",
		description: "Review the site's security posture: threat management, firewall, port forwards, WiFi security and admins.",
		args:        []*mcp.PromptArgument{promptSiteArg()},
		text: func(a map[string]string) string {
			return `Review the security of my UniFi network.` + onSite(a) + `
Check:
- unifi_get_security_settings: is IDS/IPS on, in which mode, on which networks; country blocking.
- unifi_list_threats (last 7 days, hours: 168): notable signatures, repeat offenders, internal hosts involved.
- unifi_list_port_forwards: exposed services, forwards open to any source.
- unifi_list_firewall_rules and unifi_list_firewall_policies: overly broad allow rules, missing inter-VLAN isolation.
- unifi_list_nat_rules: DNAT rules that expose internal hosts; unifi_list_acl_rules: switch-level isolation inside networks.
- unifi_list_wlans: open or WPA2-only networks where WPA3 is possible, guest networks without isolation.
- unifi_list_networks: guest/IoT segmentation.
- unifi_list_admins: number of admins and roles.
- unifi_get_system_log (class: admin-activity): recent logins and changes.
Report findings as High / Medium / Low with the reason and a specific remediation. Don't change anything.`
		},
	},
	{
		name: "firmware_review", title: "Firmware review",
		description: "List devices and the controller with available updates and propose a safe upgrade order.",
		args:        []*mcp.PromptArgument{promptSiteArg()},
		text: func(a map[string]string) string {
			return `Review firmware on my UniFi network.` + onSite(a) + `
1. unifi_check_updates: controller version and devices with upgrades.
2. unifi_list_devices: model, current firmware, uptime and client count for each device needing an update.
Propose an upgrade plan that minimises disruption: access points first (one at a time where they overlap), then switches from the edge inwards, gateway last; note which upgrades will drop clients. Don't start any upgrade unless I confirm.`
		},
	},
	{
		name: "wifi_optimization", title: "WiFi optimisation",
		description: "Analyse WiFi channels, utilisation, interference and client experience and suggest improvements.",
		args:        []*mcp.PromptArgument{promptSiteArg()},
		text: func(a map[string]string) string {
			return `Analyse and suggest improvements for the WiFi on my UniFi network.` + onSite(a) + `
1. unifi_list_devices (type: access_point), then unifi_get_device for each AP: channel, width, transmit power, channel utilisation, satisfaction, client count per radio.
2. unifi_list_rogue_aps: strongest neighbouring networks per channel.
3. unifi_list_clients (connection: wireless): weak-signal and low-rate clients, clients stuck on 2.4 GHz.
4. unifi_list_wlans: number of SSIDs, bands, minimum data rates.
Recommend specific changes (channel plan, widths, power levels, SSID consolidation, band steering) with the reason for each, ordered by expected impact. Don't apply changes unless I confirm.`
		},
	},
}

// lazyPromptNote tells the model how to reach the tools a prompt names in lazy mode.
const lazyPromptNote = "\nThe tools above are reached through " + callToolName + " {name, arguments}; look up their arguments with " + searchToolName + " names if needed."

func registerPrompts(s *mcp.Server, lazy bool) {
	for _, p := range prompts {
		s.AddPrompt(&mcp.Prompt{Name: p.name, Title: p.title, Description: p.description, Arguments: p.args},
			func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				args := map[string]string{}
				if req.Params != nil && req.Params.Arguments != nil {
					args = req.Params.Arguments
				}
				for _, a := range p.args {
					if _, ok := args[a.Name]; a.Required && !ok {
						return nil, &jsonrpc.Error{
							Code:    jsonrpc.CodeInvalidParams,
							Message: fmt.Sprintf("Invalid arguments for prompt %s: Invalid input: expected string, received undefined at %s", p.name, a.Name),
						}
					}
				}
				text := p.text(args)
				if lazy {
					text += lazyPromptNote
				}
				return &mcp.GetPromptResult{
					Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
				}, nil
			})
	}
}

// PromptNames lists the prompts in the order they are defined.
func PromptNames() []string {
	names := make([]string, len(prompts))
	for i, p := range prompts {
		names[i] = p.name
	}
	return names
}
