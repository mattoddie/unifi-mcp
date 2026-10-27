# Using unifi-mcp

Once the server is connected, just talk to your assistant about your network. This page shows what works well and explains how the tools behave, so you know what to expect.

- [Example questions](#example-questions)
- [Built-in prompts](#built-in-prompts)
- [Making changes](#making-changes)
- [Multiple sites](#multiple-sites)
- [Summaries, raw objects and secrets](#summaries-raw-objects-and-secrets)
- [Narrowing list results](#narrowing-list-results)
- [The `extra` argument](#the-extra-argument)
- [The raw API tool](#the-raw-api-tool)
- [Tips for good results](#tips-for-good-results)

## Example questions

**Health and monitoring**

- *"How's the network? Anything I should worry about?"*
- *"What's the WAN latency and throughput, and when did the last speed test run?"*
- *"Show me alarms and notable events from the last 24 hours."*
- *"Which clients used the most data today?"* (`unifi_get_traffic_report`, `unifi_get_dpi_stats`)
- *"Are there rogue or neighbouring APs on my channels?"*

**Devices and firmware**

- *"List devices that are offline or have a firmware update available."*
- *"Which switch ports are running at 100 Mbps instead of gigabit?"*
- *"How much PoE power is the office switch using?"*
- *"Upgrade the access points one at a time."* (writes)
- *"Make the garage AP's LED blink so I can find it."* (writes)
- *"What's plugged into each port of the rack switch?"* (LLDP neighbours in `unifi_list_switch_ports`)
- *"Mirror port 3 to port 8 so I can capture its traffic."* (writes)
- *"Bond ports 23 and 24 on the rack switch for the NAS."* (writes; link aggregation)

**Clients**

- *"Who's connected to the guest WiFi?"*
- *"Find the device with IP 192.168.1.73 and tell me what it is."*
- *"Give the NAS a fixed IP of 192.168.1.10 and name it 'NAS'."* (writes)
- *"Lock the living-room TV to the lounge AP."* (writes)
- *"What has the kids' tablet been talking to in the last 15 minutes, and was anything blocked?"* (`unifi_list_traffic_flows`)
- *"Block 'kids-tablet' from 9pm."* The assistant can block it now, but scheduling is up to you or your client.

**WiFi**

- *"Do any SSIDs still use WPA2 only when they could use WPA3 transition mode?"*
- *"Create an IoT SSID on VLAN 30, 2.4 GHz only, with client isolation."* (writes)
- *"Give me the QR code string for the guest WiFi."*

**Firewall and security**

- *"Which ports are forwarded to the internet, and to which hosts?"*
- *"Is IDS/IPS on? What threats were blocked this week?"*
- *"Add a traffic rule that blocks social media on the kids' network."* (writes)
- *"Block traffic from the IoT VLAN to the main LAN."* (writes)
- *"Which NAT rules and switch ACLs are there, and do any expose an internal host?"*
- *"Limit the kids' devices to 10 Mbps down, and prioritise video calls."* (writes; QoS rules)
- *"Turn off internet for the kids' group from 21:00 to 07:00."* (writes; object-oriented network policy)

**Site settings**

`unifi_get_site_settings` and `unifi_update_site_setting` cover every site setting object, including SNMP, mDNS and auto-backup:

- *"Turn on SNMP with community 'monitoring'."* → `key: snmp`, `changes: {enabled: true, community: "monitoring"}` (writes)
- *"Set mDNS to automatic."* → `key: mdns`, `changes: {mode: "auto"}` (writes)
- *"Back up the controller every Monday at 03:00 and keep 7 backups."* → `key: super_mgmt`, `changes: {autobackup_enabled: true, autobackup_cron_expr: "0 3 * * 1", autobackup_max_files: 7}` (writes)

**Guests and hotspot**

- *"Create 20 single-use vouchers valid for 8 hours, limited to 10 Mbps down."* (writes)
- *"Revoke all unused vouchers with the note 'conference'."* (writes)

## Built-in prompts

The server includes five prompts. Each one is a ready-made, multi-step request that tells the model which tools to use and how to report. Most clients show them as slash commands or in a prompt picker:

| Prompt | What it does |
|---|---|
| `network_health_check` | Checks WAN, devices, clients, alarms, events and speed tests, then reports problems ordered by severity. |
| `troubleshoot_client` | Finds a client by MAC, name or IP and checks its signal, AP or switch port, history and rules to work out why it has problems. |
| `security_review` | Reviews IDS/IPS, threats, port forwards, firewall rules, WiFi security, segmentation and admins, and reports findings as High, Medium or Low. |
| `firmware_review` | Lists available updates and proposes a safe upgrade order. |
| `wifi_optimization` | Analyses channels, utilisation, interference and client experience, then suggests improvements. |

All five are read-only: they tell the model not to change anything, or to ask first.

## Making changes

With `UNIFI_ALLOW_WRITES=true` the assistant can change things. Good clients ask before running tools marked *destructive*, such as restarts, blocking, firmware upgrades, configuration edits and anything that grants access, like inviting an admin or authorizing a guest. It's still a good habit to ask for a plan first: *"What would you change? Don't do it yet."*, or for a [dry run](#previewing-changes-with-dry_run). With `UNIFI_AUDIT_LOG` set, every change is also recorded in an [audit log](configuration.md#audit-log).

### Previewing changes with `dry_run`

Every write and delete tool, and `unifi_api_request` for methods other than `GET`, takes `dry_run: true`. The tool then does everything up to the first request that would change something: it reads the current state, checks the arguments and builds the request, but doesn't send it. Nothing on the controller changes. Ask for it in plain words:

> *"Do a dry run of turning off PoE on port 4 of the office switch and show me what would change."*

For an update, the preview lists the fields that would change instead of the whole object:

```json
{
  "dry_run": true,
  "would_send": {"method": "PUT", "path": "/api/s/default/rest/wlanconf/64f0c2…"},
  "changes": {
    "changed": {
      "name": {"from": "Home", "to": "Guest"},
      "x_passphrase": {"from": "[redacted]", "to": "[redacted]"}
    }
  },
  "note": "Nothing was changed: this was a dry run. …"
}
```

- **`would_send`** is the request: method, path and, when there's no diff, the body. Secrets are `[redacted]`.
- **`changes`** groups the differences into `changed` (`from` and `to`), `added` and `removed` fields. Nested fields are named by path, such as `port_overrides[1].poe_mode`. A partial update, such as a switch's `port_overrides`, only lists the fields it sends; the rest stay as they are.
- **`current`** shows the object a delete would remove, when the tool read it first.
- Commands and creates show the body that would be sent.

Only the first change is previewed. A tool that would do more afterwards, such as reading back the vouchers it created, stops there, and the controller's own follow-up work (a rolling upgrade going through each device, say) isn't simulated. To make the change, ask again without the dry run.

### Create or update: `save_*` tools

Configuration objects such as networks, WLANs, firewall rules, port forwards and routes are managed with one `unifi_save_*` tool each:

- **Without `id`** it **creates** a new object. Sensible defaults fill in anything not given.
- **With `id`** it **updates** that object, changing **only the fields you pass**. Everything else stays as it is.

So *"turn off the guest WiFi"* becomes `unifi_save_wlan {id: "…", enabled: false}`, and the SSID's password, VLAN and other settings stay untouched.

### Deleting

Deleting needs `UNIFI_ALLOW_DELETES=true` as well. This covers forgetting clients and revoking hotspot vouchers too. With it off, the delete tools don't exist, and the raw API tool refuses `DELETE` requests and requests that delete, forget, revoke, remove, reset, clear, purge or factory-reset something, and only sends classic `cmd/<manager>` commands that are known not to delete anything. You can still *disable* most objects with `enabled: false`, which is easy to undo.

### Actions

Some tools act rather than edit configuration: `unifi_restart_device`, `unifi_locate_device`, `unifi_upgrade_device`, `unifi_power_cycle_port`, `unifi_reconnect_client`, `unifi_run_speedtest`, `unifi_create_backup` and others. They return once the controller accepts the command. The action itself, such as a reboot or an upgrade, finishes in the background.

## Multiple sites

Most tools take an optional `site` argument. Without it, they use `UNIFI_SITE`, which defaults to `default`. Ask *"list my sites"* to see them, then name the site in your question: *"…on the Office site"*. The assistant maps the display name to the site's API name.

## Summaries, raw objects and secrets

- **Compact by default.** List tools return trimmed summaries with the useful fields and friendly units (Mbps, ISO timestamps, readable uptimes). This keeps responses small enough for the model to reason about. See [Narrowing list results](#narrowing-list-results) to make them smaller still.
- **`raw: true`** on list and get tools returns the controller's full objects when you need a field the summary leaves out.
- **Secrets are redacted.** Passphrases, pre-shared keys, RADIUS secrets and other `x_*` fields show as `[redacted]`. With `UNIFI_ALLOW_SECRETS=true`, you can ask explicitly so the tool is called with `include_secrets: true` where it's supported, and `unifi_get_wifi_qr` is available; it returns the WiFi password by design, because a join QR code needs it. Without it, neither exists.

## Narrowing list results

Every `unifi_list_*` tool, and `unifi_get_speedtest_results`, returns the same shape:

```json
{"total": 42, "returned": 10, "items": [ ... ]}
```

`total` is how many items matched, and `returned` how many were sent after `limit`. When `returned` is smaller than `total`, there are more. The tools share these arguments, so the model can fetch only what it needs:

| Argument | What it does |
|---|---|
| `search` | Case-insensitive substring match on the fields that identify an item: name, hostname, MAC, IP, model, SSID, network and similar, depending on the tool. |
| `fields` | Keeps only these fields in each item. Use dot paths for nested fields, such as `uplink.device_mac`, and for fields inside arrays, such as `auth_servers.ip`. It also works with `raw: true`. Unknown fields are ignored. If none of them exist, the result includes a `hint` listing the fields the items do have. |
| `limit` | The maximum number of items. Only lists that can grow long have it: devices, clients, events, alarms, threats, sessions, vouchers and other history. The default is in each tool's reference. |

Some tools also have exact-match filters where the controller data supports them reliably:

| Tool | Filters |
|---|---|
| `unifi_list_devices` | `type` (`access_point`, `switch`, `gateway`, `other`), `state` (`connected`, `disconnected`, `upgrading`, …) |
| `unifi_list_clients` | `connection` (`wired`, `wireless`), `guests_only`, `network` (network name), `ap_mac` |
| `unifi_list_known_clients` | `filter` (`blocked`, `fixed_ip`, `noted`, `named`) |
| `unifi_list_switch_ports` | `up` (`true` for linked ports, `false` for the rest) |
| `unifi_list_firewall_policies` | `include_predefined` |
| `unifi_list_rogue_aps` | `rogue_only` |
| `unifi_list_acl_rules` | `network_id` |
| `unifi_list_traffic_flows` | `client_mac`, `client_ip`, `action`, `risk`, `destination_domain` and `page`. The controller applies these and `search` itself, and `limit` is its page size (at most 500). |

Search and filters are applied first, then `limit`, then `fields`. For example:

- *"List only the offline APs, just name and IP"* becomes `unifi_list_devices {type: "access_point", state: "disconnected", fields: ["name", "ip"]}`.
- *"Which clients are on the IoT network, and which AP are they on?"* becomes `unifi_list_clients {network: "IoT", fields: ["name", "ip", "ap_mac", "signal_dbm"]}`.
- *"Which ports on the office switch are down?"* becomes `unifi_list_switch_ports {mac: "…", up: false, fields: ["idx", "name"]}`.
- *"Any 'lost contact' events today?"* becomes `unifi_list_events {search: "lost_contact"}`.

Results larger than 256 KiB are cut off with a note asking for a narrower request, so these arguments are also the way out of a truncated result.

## The `extra` argument

`save_*` and update tools have typed arguments for the common settings. The controller has hundreds more fields, and their names vary between versions. For anything not covered, `extra` accepts an object of raw controller fields that is merged last:

> *"Enable the schedule on the guest SSID. Use `extra` with `schedule_enabled: true`."*

To find the right field names, ask the assistant to fetch the object with `raw: true` first.

## The raw API tool

`unifi_api_request` (toolset `raw`) calls any path on the Network application, such as `/api/s/default/stat/ccode`, `/v2/api/site/default/trafficroutes` or `/integration/v1/sites`. It's a fallback for things the other tools don't cover. It follows the same switches as everything else:

| Settings | Allowed methods |
|---|---|
| Default (read-only) | `GET` |
| `UNIFI_ALLOW_WRITES=true` | `GET`, `POST`, `PUT`, `PATCH`, except delete-like commands such as `{"cmd": "delete-site"}` or `forget-sta` |
| `+ UNIFI_ALLOW_DELETES=true` | All, including `DELETE` |

Paths must stay inside the Network application: `.` and `..` segments (also percent-encoded), backslashes and query strings in the path are refused. Pass query parameters with the `query` argument.

Leave `raw` out of `UNIFI_TOOLSETS` if you want the assistant limited to the curated tools.

## Tips for good results

- **Be specific about scope.** *"APs on the first floor"* or *"the last 2 hours"* lead to tighter tool calls.
- **Ask for evidence.** *"…and show the numbers you based that on"* keeps answers grounded.
- **Plan, then apply.** For changes, ask for a plan, review it, then say *"go ahead"*.
- **Fewer tools, better focus.** If the assistant picks the wrong tools, narrow `UNIFI_TOOLSETS`. You can also name the tool: *"use unifi_list_switch_ports"*.
- **Check compatibility.** Some features depend on your Network version. If a tool says *endpoint not found*, see [Compatibility](compatibility.md).
