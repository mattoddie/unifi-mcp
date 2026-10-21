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
| v2 | `/v2/api/site/<site>/…` | Newer features: traffic rules and routes, zone-based firewall policies, QoS, NAT and ACL rules, object-oriented network policies, traffic flows, local DNS, system log, content filtering |
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
| NAT rules (`unifi_*_nat_rule*`) | Network 9.0+ with a UniFi gateway and the zone-based firewall |
| QoS rules (`unifi_*_qos_rule*`) | Recent Network 9.x/10.x with a UniFi gateway. Field names come from Network 10.6. |
| Switch ACL rules (`unifi_*_acl_rule*`) | Network 9.4+ with UniFi switches that enforce them |
| Object-oriented network policies (`unifi_*_oon_polic*`) | Network 9.4+ with a UniFi gateway |
| Traffic flows (`unifi_list_traffic_flows`) | Recent Network 9.x/10.x with a UniFi gateway |
| LLDP neighbours (`unifi_list_switch_ports`) | Switches that report an LLDP table; the neighbour must send LLDP |
| Port mirroring and link aggregation (`unifi_set_switch_port`) | Managed UniFi switches. Most allow one mirror session and up to 8 aggregation groups. |
| Locking a client to an AP (`unifi_update_client`) | Wireless clients |

If a tool isn't available on your version, it fails with a clear message ending in *"endpoint not found; it may not exist on this Network application version"*. Nothing breaks. The tool just doesn't apply.

## Hardware-dependent features

Tools work on the devices that support them. Gateway features need a UniFi gateway: IDS/IPS, traffic rules, port forwards, WAN, VPN, routes and DPI. PoE tools need PoE switches, and `unifi_set_outlet` needs a SmartPower PDU or USP plug. Running a tool against the wrong device type returns the controller's error.

## Fields that couldn't be verified

The following field names come from community sources but couldn't be checked against every version. If a save seems to have no effect, fetch the object with `raw: true` and set the right fields via `extra`:

- WireGuard and OpenVPN server and client fields (`unifi_save_vpn`)
- WAN Smart Queues fields (`unifi_update_wan`)
- Country blocking fields (`unifi_update_country_blocking`)
- QoS rule fields: `objective`, `download_limit_kbps`, `upload_limit_kbps`, `download_burst`, `upload_burst`, `wan_or_vpn_network`, `source.matching_target`/`client_macs`/`network_ids`, `destination.matching_target`/`app_ids`/`app_category_ids`/`ips`/`regions`/`web_domains`/`port_matching_type`/`port`, and the create defaults (`unifi_save_qos_rule`)
- NAT rule fields: `in_interface` and `out_interface` values, `source_filter`/`destination_filter` (`filter_type`, `address`, `port`), `setting_preference`, and whether `rule_index` must be unique (`unifi_save_nat_rule`)
- ACL rule fields: `traffic_source`/`traffic_destination` (`type: CLIENT_MAC`, `specific_mac_addresses`, `mac_mask`), `specific_enforcers`, and whether a side with no MACs matches any device (`unifi_save_acl_rule`)
- Object-oriented network policies: the `secure`, `qos` and `route` section shapes, target types `MAC` and `NETWORK_GROUP_ID`, and the plural list path / singular write path (`unifi_save_oon_policy`)
- Traffic flows: the request body (`timestampFrom`, `pageSize`, the filter arrays), the `action` and `risk` filter values, and the record fields (`unifi_list_traffic_flows`)
- LLDP table entries (`local_port_idx`, `chassis_id`, `chassis_descr`, `port_id`, `port_descr`) and the port table's `aggregated_by` (`unifi_list_switch_ports`)
- Link aggregation override fields: `aggregate_members` and `lag_idx` (newer versions) and `aggregate_num_ports` (older versions) are both sent, and the other members' overrides are removed (`unifi_set_switch_port`)
- Locking a client to an AP with a partial `rest/user` update (`fixed_ap_enabled`, `fixed_ap_mac`) instead of the full user record (`unifi_update_client`)
- Writing `super_*` settings (such as `super_mgmt` auto-backup) through `set/setting/<key>/<id>` (`unifi_update_site_setting`)

## Not covered yet

These are known gaps. Contributions are welcome:

- WireGuard peer management and VPN config file import
- IP-based switch ACL rules (`unifi_save_acl_rule` creates MAC ACLs; other types need `extra`)
- Traffic flow statistics (top apps and clients over a period)
- UniFi Protect, Access, Talk and other UniFi applications (only Network is covered)
- Site Manager / cloud API

## Help improve this page

Tested unifi-mcp on hardware or a Network version not listed here? Open a [compatibility report](https://github.com/mattoddie/unifi-mcp/issues/new?template=compatibility.yml), whether it worked or not. It helps everyone.
