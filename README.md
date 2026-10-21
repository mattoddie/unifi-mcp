<div align="center">

<img src="docs/assets/logo.svg" alt="unifi-mcp logo" width="112" height="112">

# unifi-mcp

**Talk to your UniFi network.**

An open-source [Model Context Protocol](https://modelcontextprotocol.io) server for Ubiquiti **UniFi Network**. It lets Claude and other AI assistants monitor and manage your sites, devices, clients, WiFi, firewall and more, all from one Docker container.

[![CI](https://github.com/mattoddie/unifi-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/mattoddie/unifi-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/mattoddie/unifi-mcp?sort=semver)](https://github.com/mattoddie/unifi-mcp/releases)
[![Container image](https://img.shields.io/badge/ghcr.io-mattoddie%2Funifi--mcp-2496ED?logo=docker&logoColor=white)](https://github.com/mattoddie/unifi-mcp/pkgs/container/unifi-mcp)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

**[unifi-mcp.mattoddie.dev](https://unifi-mcp.mattoddie.dev)**

[Quick start](#quick-start) · [Documentation](https://unifi-mcp.mattoddie.dev/docs/) · [Tool reference](docs/tools.md) · [Contributing](CONTRIBUTING.md)

</div>

---

> **"Why is the lounge TV buffering?"**
> *The TV is on the Lounge AP at -78 dBm and 12 Mbps, and that AP's 5 GHz radio is at 81% channel utilisation on channel 36. Moving the AP to channel 149 or adding a wired backhaul should fix it. Want me to change the channel?*

Ask about your network in plain English. The assistant picks from **132 tools** to look things up and, if you allow it, make changes. Some examples:

- 🩺 *"Give me a health check of the network."*
- 🔎 *"Which clients have a weak WiFi signal right now?"*
- 🔐 *"Review my firewall and port forwards for anything risky."*
- 🔄 *"Which devices have firmware updates, and when were they last rebooted?"*
- 🎟️ *"Create 10 one-day guest vouchers limited to 5 Mbps."*
- 🧱 *"Block the device called 'kids-tablet' until I unblock it."*

## Highlights

- **🐳 Docker-first.** There's one small distroless image (`ghcr.io/mattoddie/unifi-mcp`, about 22 MB) for `linux/amd64` and `linux/arm64`. It serves Streamable HTTP for a long-running service, or stdio for `docker run -i`.
- **🛡️ Safe by default.** The server is read-only unless you turn on writes, and deleting configuration needs a second switch. Destructive tools are labelled so your MCP client asks before running them, and passwords and keys are redacted from output.
- **🔍 Preview and audit.** Every write tool takes `dry_run: true` to show the request it would send, with a field-level diff of what would change, without changing anything. `UNIFI_AUDIT_LOG` records each change in a JSON Lines audit log.
- **🧰 Broad coverage.** It covers 14 toolsets: devices, clients, switch ports and PoE, WiFi, networks and VLANs, WAN, DNS, legacy and zone-based firewall, traffic rules and routes, IDS/IPS, VPN, RADIUS, hotspot vouchers, statistics, logs, admins and backups.
- **🪶 Light on context.** By default the client sees three small meta-tools (under 3 KB) that search for and call the 132 tools, instead of 40–132 KB of tool definitions. Reads and writes go through separate meta-tools, so your client still asks before every change. Set `UNIFI_TOOL_MODE=full` to list every tool directly.
- **🎛️ Tunable.** Expose only the toolsets you need, which keeps the model focused.
- **🔌 Works with your controller.** Supports UniFi OS consoles (UDM, UDM Pro/SE, UCG, UDR, UX, Cloud Key Gen2+, UniFi OS Server) and legacy self-hosted Network applications. Auth is an API key or a local account.
- **🧪 Tested.** The test suite runs against a mock controller on every build.

## Quick start

You need Docker, a UniFi controller and an MCP client such as [Claude Code](https://claude.com/claude-code).

**1. Get credentials.** On a UniFi OS console, create an API key: **UniFi Network → Settings → Control Plane → Integrations → Create API Key**. On older controllers, use a [local account](docs/getting-started.md#2-create-credentials) instead.

**2. Start the server.**

```sh
mkdir unifi-mcp && cd unifi-mcp
curl -fsSLO https://raw.githubusercontent.com/mattoddie/unifi-mcp/main/compose.yaml
curl -fsSL https://raw.githubusercontent.com/mattoddie/unifi-mcp/main/example.env -o .env
# Edit .env: set UNIFI_URL, UNIFI_API_KEY and a random MCP_AUTH_TOKEN (required; e.g. `openssl rand -hex 32`).
# For an https controller, also say how to check its certificate, or the server won't start.
# Recommended: pin it with UNIFI_TLS_FINGERPRINT (see docs/security.md). example.env sets
# UNIFI_VERIFY_SSL=false, which skips the check: remove it once the fingerprint is set.
docker compose up -d
```

**3. Connect your assistant.**

```sh
claude mcp add --transport http unifi http://<docker-host>:8080/mcp \
  --header "Authorization: Bearer <MCP_AUTH_TOKEN>"
```

Now ask *"How's my network doing?"* in Claude Code.

Using **Claude Desktop, VS Code, Cursor or Codex**? See [Connecting MCP clients](docs/clients.md). Prefer not to run a service? Use [stdio mode](docs/clients.md#stdio-mode) with `docker run -i`.

## Turning on changes

Out of the box the assistant can look but not touch. To let it make changes, add these to `.env` and run `docker compose up -d`:

```sh
UNIFI_ALLOW_WRITES=true      # restart devices, block clients, edit WiFi/firewall/networks…
UNIFI_ALLOW_DELETES=true     # also allow deleting configuration, forgetting clients… (needs writes too)
UNIFI_TOOLSETS=overview,devices,clients,wifi,monitoring   # optional: expose fewer tools
UNIFI_AUDIT_LOG=stderr       # optional: log every change (or a file path; see docs/configuration.md)
```

In the default lazy mode, changes go through the `unifi_call_write_tool` meta-tool, which clients treat as destructive, so they ask you before each one.

Ask for a dry run to preview a change first: *"Dry run: disable the guest WiFi. What would change?"* Read [Security](docs/security.md) before turning on writes for a production network.

## What it can do

| Toolset | What's covered |
|---|---|
| `overview` | Sites, controller info, site health |
| `devices` | Device status and details, restart/locate/provision, firmware upgrades (also rolling), radios, PoE outlets, adoption, RF scans, AP groups |
| `clients` | Online and known clients, block/unblock, reconnect, names, fixed IPs, forget |
| `switching` | Switch ports, port profiles, PoE power-cycling |
| `wifi` | WLANs (SSIDs, security, VLANs, bands), WiFi QR codes |
| `networks` | Networks/VLANs and DHCP, WAN, local DNS records, bandwidth profiles, dynamic DNS |
| `firewall` | Port forwards, firewall rules and groups, zone-based policies and zones, traffic rules |
| `routing` | Static routes, policy-based traffic routes |
| `security` | IDS/IPS threat management, threat log, country blocking, content filtering |
| `vpn` | VPN servers and site-to-site VPNs, RADIUS profiles and accounts |
| `hotspot` | Vouchers, hotspot operators, guest authorization, guest portal |
| `monitoring` | Events, alarms, traffic reports, DPI, speed tests, rogue APs, system log, sessions, anomalies, dashboards |
| `admin` | Site settings, admins, backups, controller updates, sites |
| `raw` | An escape hatch that calls any controller API path |

See the **[tool reference](docs/tools.md)** for every tool and argument. Ready-made prompts like `network_health_check`, `troubleshoot_client` and `security_review` chain the tools together for common jobs.

## Documentation

The docs are also published at **[unifi-mcp.mattoddie.dev/docs](https://unifi-mcp.mattoddie.dev/docs/)**, with search.

| | |
|---|---|
| 🚀 [Getting started](docs/getting-started.md) | Step-by-step setup, from credentials to first question |
| 🔌 [Connecting MCP clients](docs/clients.md) | Claude Code, Claude Desktop, VS Code, Cursor, Codex, stdio mode |
| ⚙️ [Configuration](docs/configuration.md) | Every environment variable, Docker secrets, toolsets |
| 🐳 [Deployment](docs/deployment.md) | Compose, image tags, upgrades, TLS reverse proxy, hardening |
| 💬 [Using unifi-mcp](docs/usage.md) | Example conversations, prompts, how saves and secrets work |
| 🧰 [Tool reference](docs/tools.md) | All 132 tools and 5 prompts, generated from the code |
| 🛡️ [Security](docs/security.md) | Threat model, least privilege, safe write setups |
| 📡 [Compatibility](docs/compatibility.md) | Supported controllers and versions, API caveats |
| 🩹 [Troubleshooting](docs/troubleshooting.md) | Common errors and how to fix them |
| 🛠️ [Development](docs/development.md) | Architecture, adding a tool, testing |

## Contributing

Contributions are very welcome, whether that's bug reports, compatibility reports from your hardware, docs or new tools. Read the [contributing guide](CONTRIBUTING.md) to get started, and please follow the [code of conduct](CODE_OF_CONDUCT.md). To report a vulnerability, see the [security policy](SECURITY.md). Please don't open a public issue for one.

## License

[MIT](LICENSE) © Matt Oddie

*unifi-mcp is an independent community project. It is not affiliated with, endorsed by or sponsored by Ubiquiti Inc. UniFi and Ubiquiti are trademarks of Ubiquiti Inc.*
