# Using unifi-mcp

Once the server is connected, just talk to your assistant about your network. This page shows what works well and explains how the tools behave, so you know what to expect.

- [Example questions](#example-questions)
- [Built-in prompts](#built-in-prompts)
- [Making changes](#making-changes)
- [Multiple sites](#multiple-sites)
- [Summaries, raw objects and secrets](#summaries-raw-objects-and-secrets)
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

**Clients**

- *"Who's connected to the guest WiFi?"*
- *"Find the device with IP 192.168.1.73 and tell me what it is."*
- *"Give the NAS a fixed IP of 192.168.1.10 and name it 'NAS'."* (writes)
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

With `UNIFI_ALLOW_WRITES=true` the assistant can change things. Good clients ask before running tools marked *destructive*, such as restarts, blocking, firmware upgrades and configuration edits. It's still a good habit to ask for a plan first: *"What would you change? Don't do it yet."*

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

- **Compact by default.** List tools return trimmed summaries with the useful fields, friendly units (Mbps, ISO timestamps, readable uptimes) and filters like `search`, `type` and `limit`. This keeps responses small enough for the model to reason about.
- **`raw: true`** on list and get tools returns the controller's full objects when you need a field the summary leaves out.
- **Secrets are redacted.** Passphrases, pre-shared keys, RADIUS secrets and other `x_*` fields show as `[redacted]`. To include them, ask explicitly so the tool is called with `include_secrets: true` where it's supported. `unifi_get_wifi_qr` returns the WiFi password by design, because a join QR code needs it.

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
- **Fewer tools, better focus.** If the assistant picks the wrong tools, narrow `UNIFI_TOOLSETS`.
- **Check compatibility.** Some features depend on your Network version. If a tool says *endpoint not found*, see [Compatibility](compatibility.md).
