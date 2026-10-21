package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// Name is the MCP server name.
const Name = "unifi-mcp"

// Version is set at build time with -ldflags "-X github.com/mattoddie/unifi-mcp/internal/tools.Version=1.2.3".
var Version = "0.0.0-dev"

func instructions(opts config.Tools) string {
	s := "Tools for monitoring and managing a Ubiquiti UniFi Network controller. "
	if opts.Lazy() {
		s += "The tools are reached through meta-tools: find them with " + searchToolName + " (a query or a toolset; no arguments lists the toolsets), " +
			"get their arguments with " + searchToolName + " names, and run read-only tools with " + callToolName + " {name, arguments}"
		if opts.AllowWrites {
			s += " and tools that change state with " + callWriteToolName
		}
		s += ". Start by calling unifi_get_site_health, unifi_list_devices or unifi_list_clients with " + callToolName + ". "
	} else {
		s += "Start with unifi_get_site_health, unifi_list_devices or unifi_list_clients. "
	}
	s += "Most tools take an optional `site`; use unifi_list_sites to find site names. " +
		listHelp +
		"unifi_save_* tools create an object when no id is given and update it (changing only the fields passed) when an id is given. "
	if opts.AllowWrites {
		s += "Write tools are enabled: confirm with the user before restarting devices, blocking clients or changing configuration. " +
			"Every tool that changes something accepts dry_run: true, which reads the current state and returns the request it would send (with a field-level diff for updates) without changing anything. " +
			"Preview big or risky changes with dry_run first and show the user the result before making them."
	} else {
		s += "The server is read-only; write tools are disabled (UNIFI_ALLOW_WRITES=false)."
	}
	return s
}

// NewServer creates the MCP server with the tools and prompts allowed by opts.
// registered, when non-nil, receives each registered tool in order.
func NewServer(client *unifi.Client, opts config.Tools, registered *[]Info) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Version: Version}, &mcp.ServerOptions{Instructions: instructions(opts)})
	c := &Context{Server: s, Client: client, Tools: opts, Registered: registered, audit: newAuditLog(opts.AuditLog)}
	if opts.Lazy() {
		c.lazy = newRegistry()
	}
	registerOverview(c)
	registerDevices(c)
	registerClients(c)
	registerSwitching(c)
	registerWifi(c)
	registerNetworks(c)
	registerFirewall(c)
	registerRouting(c)
	registerSecurity(c)
	registerVPN(c)
	registerHotspot(c)
	registerMonitoring(c)
	registerAdmin(c)
	registerRaw(c)
	if c.lazy != nil {
		registerMeta(c)
	}
	registerPrompts(s, opts.Lazy())
	return s
}
