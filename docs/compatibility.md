# Compatibility

## Controllers

| Controller | Support | Authentication |
|---|---|---|
| UniFi OS consoles: UDM, UDM Pro, UDM SE, UDM Pro Max, UCG Ultra/Max/Fiber, UDR, UX, UDW | ✅ | API key or local account |
| Cloud Key Gen2 / Gen2 Plus (UniFi OS) | ✅ | API key or local account |
| UniFi OS Server | ✅ | API key or local account |
| Self-hosted UniFi Network application (Linux, Windows, Docker, e.g. `:8443`) | ✅ | Local account |
| Cloud-hosted UniFi (unifi.ui.com / Site Manager) | ❌ | Not supported. Connect to the console locally. |

The server detects UniFi OS automatically: a UniFi OS console answers `GET /` with `200`, while a legacy controller redirects. On UniFi OS, requests go through the `/proxy/network` prefix. Set `UNIFI_CONTROLLER_TYPE` if detection gets it wrong, for example behind a reverse proxy.

## UniFi Network versions

UniFi Network has three API styles. unifi-mcp uses whichever fits each feature:

| API | Paths | Used for |
|---|---|---|
| Classic | `/api/s/<site>/…` | Most things: devices, clients, WLANs, networks, legacy firewall, stats, events, settings |
| v2 | `/v2/api/site/<site>/…` | Newer features: traffic rules and routes, zone-based firewall policies, local DNS, system log, content filtering |
| Integration (official) | `/integration/v1/…` | Firewall zone editing and policy ordering. Needs an API key. |

Ubiquiti doesn't document the classic and v2 APIs, and they change between releases. Some features need a recent version:

| Feature | Needs |
|---|---|
| API key authentication | Network 9.0+ on UniFi OS |
| Zone-based firewall (policies, zones) | Network 9.0+, with the site migrated to zone-based firewall |
| Firewall zone create/update/delete and policy reordering | Network 9.0+ and an API key (Integration API) |
| Local DNS records | Network 8.2+ |
| Traffic rules and traffic routes | Network 7.x+ with a UniFi gateway |
| System log (`unifi_get_system_log`) | Recent Network 8.x/9.x. `unifi_list_events` falls back to it when the classic event API is gone. |
| Content filtering | Recent Network versions with a gateway that supports it |
| Legacy firewall rules and groups | Sites that haven't migrated to the zone-based firewall |

If a tool isn't available on your version, it fails with a clear message ending in *"endpoint not found; it may not exist on this Network application version"*. Nothing breaks. The tool just doesn't apply.

## Hardware-dependent features

Tools work on the devices that support them. Gateway features need a UniFi gateway: IDS/IPS, traffic rules, port forwards, WAN, VPN, routes and DPI. PoE tools need PoE switches, and `unifi_set_outlet` needs a SmartPower PDU or USP plug. Running a tool against the wrong device type returns the controller's error.

## Fields that couldn't be verified

The following field names come from community sources but couldn't be checked against every version. If a save seems to have no effect, fetch the object with `raw: true` and set the right fields via `extra`:

- WireGuard and OpenVPN server and client fields (`unifi_save_vpn`)
- WAN Smart Queues fields (`unifi_update_wan`)
- Country blocking fields (`unifi_update_country_blocking`)

## Not covered yet

These are known gaps. Contributions are welcome:

- WireGuard peer management and VPN config file import
- UniFi Protect, Access, Talk and other UniFi applications (only Network is covered)
- Site Manager / cloud API

## Help improve this page

Tested unifi-mcp on hardware or a Network version not listed here? Open a [compatibility report](https://github.com/mattoddie/unifi-mcp/issues/new?template=compatibility.yml), whether it worked or not. It helps everyone.
