# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

For unifi-mcp, the "public API" is the set of tool names, their arguments and the configuration variables. Renaming or removing any of these is a breaking change.

## [Unreleased]

### Added

- `UNIFI_ALLOW_SECRETS` (off by default) gates access to secrets on the server side. When off, no tool offers `include_secrets`, a client that sends it anyway gets redacted output, and `unifi_get_wifi_qr` isn't registered. The startup log line shows `secrets redacted` or `secrets ALLOWED`, and the Claude Desktop extension has an **Allow secrets** setting.

### Changed

- **Breaking:** `include_secrets` and `unifi_get_wifi_qr` now need `UNIFI_ALLOW_SECRETS=true`. Before, any client holding the token could ask for unredacted Wi-Fi passphrases, VPN keys and passwords, even with writes off. Set `UNIFI_ALLOW_SECRETS=true` to keep the old behaviour.

## [0.6.1] - 2026-10-22

### Added

- unifi-mcp is published to the [MCP Registry](https://registry.modelcontextprotocol.io) as `io.github.mattoddie/unifi-mcp` with each stable release. The entry lists the container image, run over stdio, and the six Claude Desktop bundles with their SHA-256s. The image carries the `io.modelcontextprotocol.server.name` label the registry checks. See [Releasing](docs/releasing.md#about-the-mcp-registry).

## [0.6.0] - 2026-10-22

### Removed

- **Breaking: lazy tool loading was removed.** Every enabled tool is listed directly again, as in 0.4.0, and the meta-tools `unifi_search_tools`, `unifi_call_tool` and `unifi_call_write_tool` are gone. Clients with their own tool search, such as Claude Code, already defer large tool lists, and lazy mode hid the real tools from them. It also hid each tool's annotations behind two meta-tools, so client confirmations and per-tool permission rules couldn't tell one change from another, and it cost the model extra calls with untyped arguments. `UNIFI_TOOL_MODE` now stops the server if it is set to anything; remove it and use `UNIFI_TOOLSETS` to keep the tool list small. An empty value, or the unfilled `${user_config.tool_mode}` from a 0.5.0 Claude Desktop extension, still counts as unset. See [Keeping the tool list small](docs/configuration.md#keeping-the-tool-list-small) for ready-made toolset combinations and their sizes.
- The Claude Desktop extension no longer has a **Tool mode** setting, and the startup log line no longer shows the tool mode.

## [0.5.0] - 2026-10-22

### Added

- Dry runs: every write and delete tool, and `unifi_api_request` when writes are enabled, takes `dry_run: true`. The tool reads the current state but sends nothing that changes it, and returns the first request it would send with secrets redacted. For updates the preview is a field-level diff (`changed`, `added`, `removed`) against the object the tool read. In a dry run the first state-changing request is stopped before any login, so a preview never changes anything. Controller reads that use `POST` (statistics reports, event, session and log queries) are marked as reads and still run. In lazy mode, pass `dry_run` in the `arguments` of `unifi_call_write_tool`.
- Audit log: `UNIFI_AUDIT_LOG` set to a file path (JSON Lines, created with mode `0600`) or `stderr` records every call of a tool that changes things, including dry runs, failed calls and refused ones: time, tool, arguments with secrets redacted, the state-changing requests sent with their HTTP status, and the outcome. It's off by default. In lazy mode, calls made through `unifi_call_write_tool` are logged under the name of the tool they ran. The server checks the file is writable at startup; a failed write later logs one warning and doesn't affect the tool call.
- The startup log line says whether the audit log is on.
- Every `unifi_list_*` tool, and `unifi_get_speedtest_results`, takes `search` (case-insensitive substring match on names, MACs, IPs and similar) and `fields` (keep only these fields in each item, with dot paths such as `uplink.device_mac`; it also works with `raw: true`). Lists that can grow long also take `limit`. If none of the requested fields exist, the result includes a `hint` listing the available ones. See [Narrowing list results](docs/usage.md#narrowing-list-results).
- New exact-match filters: `state` on `unifi_list_devices`, `network` and `ap_mac` on `unifi_list_clients`, and `up` on `unifi_list_switch_ports`.
- `limit` on `unifi_list_devices` (default 500), `unifi_list_vouchers` (default 200) and `unifi_get_speedtest_results` (default 100).
- The server instructions explain the list arguments once, instead of every tool repeating the descriptions. In full mode `tools/list` grows by about 2.7 KB (6.7%) with the read-only tools and 2% with writes and deletes enabled; in lazy mode it is unchanged and the instructions grow by about 270 bytes.
- QoS rules: `unifi_list_qos_rules`, `unifi_save_qos_rule` and `unifi_delete_qos_rule` limit or prioritise traffic by client, network, app, IP, region or domain.
- NAT rules (DNAT, SNAT and masquerade, separate from port forwards): `unifi_list_nat_rules`, `unifi_save_nat_rule` and `unifi_delete_nat_rule`. Predefined rules can't be changed or deleted.
- Switch ACL rules (MAC ACLs): `unifi_list_acl_rules`, `unifi_save_acl_rule` and `unifi_delete_acl_rule`.
- Object-oriented network policies: `unifi_list_oon_policies`, `unifi_save_oon_policy` and `unifi_delete_oon_policy`.
- `unifi_list_traffic_flows` lists recent flows filtered by client, action, risk, domain or free text. It looks back 15 minutes and returns 50 flows unless asked for more.
- `unifi_list_switch_ports` shows each port's LLDP neighbours and the port that aggregates it.
- `unifi_set_switch_port` configures port mirroring (`mirror_port_idx`) and link aggregation (`aggregate_ports`). Other ports' overrides are kept, except those of the new group's other members, which the controller manages. `op_mode: switch` turns either off and clears their settings.
- `unifi_update_client` locks a wireless client to an access point (`fixed_ap_mac`) or unlocks it (`clear_fixed_ap`). Client summaries show `locked_to_ap`.
- The `security_review` and `troubleshoot_client` prompts look at the new rules and flows.
- **Claude Desktop extension bundles.** Each release now has a one-click `.mcpb` bundle for macOS (Apple silicon and Intel), Windows (x64 and Arm) and Linux (amd64 and arm64). Open it in Claude Desktop and fill in the settings form: no Docker, service or token needed. The form covers the controller, certificate checks, writes and deletes, toolsets, the tool mode (lazy by default) and the audit log. See `docs/clients.md`. `make mcpb` builds them locally.

### Changed

- **Breaking: tools are now loaded lazily by default.** Instead of listing every enabled tool (about 48 KB of definitions read-only, 174 KB with writes and deletes), the server lists three meta-tools, under 3 KB in all: `unifi_search_tools` finds tools by keyword or toolset and returns their full description and input schema, `unifi_call_tool` runs read-only tools, and `unifi_call_write_tool` (only with `UNIFI_ALLOW_WRITES=true`) runs tools that change state. `unifi_call_write_tool` is marked destructive, so clients ask before every change, while reads never prompt. Toolsets, the write and delete switches, argument validation and results are unchanged. To keep the old behaviour, set `UNIFI_TOOL_MODE=full`, which also suits Claude Code (it defers large tool lists itself) and lets you write permission rules for individual tools. See `docs/configuration.md`.
- The startup log line includes the tool mode.
- **Breaking:** every list tool now returns `{total, returned, items}`, as `unifi_list_devices` and `unifi_list_clients` already did. Before, most returned a bare array, so a model couldn't tell when `limit` had cut the list. Tools that returned an object keep their other keys next to the list: `unifi_list_switch_ports` returns `{switch, total, returned, items}` instead of `{switch, ports}`, `unifi_list_wans` `{total, returned, items, health}` instead of `{wans, health}`, and `unifi_list_events` keeps `source` and `legacy_error` when it falls back to the system log, with the events in `items` instead of `events`.
- When `search` is given, `unifi_list_events` and `unifi_list_threats` ask the controller for up to 3000 entries and then apply `limit`, so a search isn't limited to the newest `limit` entries.
- `unifi_get_speedtest_results` lists the newest results first.
- The output-size note now suggests `search` and `fields` too.
- A configuration variable set to exactly `${user_config.<key>}` is treated as unset, like an empty one. Claude Desktop passes an extension setting that was left empty through like that.

### Fixed

- `unifi_update_site_setting` writes `super_*` settings such as `super_mgmt` (auto-backup) through `set/setting/<key>/<id>`, as the UniFi UI does, instead of `rest/setting`.

### Security

- Built with Go 1.27.3, which fixes a `crypto/x509` vulnerability in the standard library (GO-2026-6809).

## [0.4.0] - 2026-10-20

### Security

- **Breaking: the controller's TLS certificate must now be checked, or explicitly not checked.** For an `https` `UNIFI_URL`, the server refuses to start unless `UNIFI_TLS_FINGERPRINT`, `UNIFI_CA_FILE` or `UNIFI_VERIFY_SSL` is set. Before, verification was silently off, so anyone on the network path could impersonate the controller and capture the API key or password. To upgrade, pin the console's certificate with `UNIFI_TLS_FINGERPRINT` (recommended; see `docs/security.md`), or set `UNIFI_VERIFY_SSL=false` to keep the old behaviour. `example.env` already sets `UNIFI_VERIFY_SSL=false`, so a `.env` copied from it keeps working.
- A plain `http://` `UNIFI_URL` now logs a warning that credentials travel unencrypted.
- **Breaking:** `UNIFI_URL` may no longer contain a username or password (`https://user:pass@host`); they ended up in logs and error messages. Use `UNIFI_API_KEY`, or `UNIFI_USERNAME` and `UNIFI_PASSWORD`.
- **Breaking:** `MCP_ALLOW_UNAUTHENTICATED=true` now also requires `MCP_ALLOWED_HOSTS`. Without a token, the Host check is the only protection against a web page reaching the server through DNS rebinding, so the server refuses to start without it.
- **Breaking:** `MCP_AUTH_TOKEN` must be at least 16 characters. Generate one with `openssl rand -hex 32`.
- An `MCP_ALLOWED_HOSTS` entry without a port now only allows an `Origin` on the scheme's default port (80 or 443). List `host:port` to allow a page on another port. Before, `localhost` let a page on any `localhost:<port>` through. Every `Origin` header is checked, and an empty one is rejected.
- The HTTP server now has a 60 second read timeout and a 120 second idle timeout, on top of the header timeout.
- The `healthcheck` command connects to `MCP_HTTP_HOST` when it is a specific address (and to `127.0.0.1` when it is `0.0.0.0` or `::`), and reads `MCP_HTTP_PORT_FILE`.
- With writes on and deletes off, the raw API tool could be made to delete by percent-encoding a letter of the path, e.g. `POST /v2/api/site/default/firewall-policies/batch-d%65lete`, which the delete check didn't recognise but the controller decoded and ran. API paths that percent-encode letters, digits, `-`, `_` or `~` are now refused, and the delete check runs on the decoded path. The check also recognises more verbs (`set-default`, `factory`, `clear`, `purge`, `erase`, `wipe`, `del-`), matches `cmd` keys in any letter case, and, for the classic `cmd/<manager>` endpoints, only sends commands known not to delete anything.
- Controller responses are read up to 64 MiB (a bigger one is an error instead of being read into memory), and JSON nesting is capped at 1000 levels. Tool output returned to the model is capped at 256 KiB, with a note saying it was truncated and how to narrow the request.
- With a username and password, the server logs in again only when the controller answers 401, not 403 (which means the account lacks permission). After a rejected login it waits before trying again (5 seconds, doubling up to 5 minutes) instead of retrying on every tool call, which could lock the account.
- Tools that grant access or can disrupt connectivity are now marked destructive, so MCP clients ask before running them: `unifi_invite_admin`, `unifi_create_site`, `unifi_reconnect_client`, `unifi_unblock_client`, `unifi_update_client` (fixed IPs), `unifi_authorize_guest`, `unifi_unauthorize_guest`, `unifi_create_vouchers`, `unifi_save_hotspot_operator`, `unifi_save_radius_account`, `unifi_save_radius_profile`, `unifi_provision_device`, `unifi_adopt_device`, `unifi_save_ap_group`, `unifi_save_dns_record`, `unifi_save_content_filter`, `unifi_save_bandwidth_profile` and `unifi_save_dynamic_dns`.
- Device and client IDs taken from controller responses are now encoded as path segments.

### Changed

- The Docker base images are pinned by digest (Dependabot keeps them current), the Go toolchain is 1.27.2 in both `go.mod` and the image, and CI runs `govulncheck`.

### Fixed

- The zone-based firewall tools that use the integration API no longer fall back to the only site when the `site` name doesn't match; they report that the site wasn't found.

## [0.3.0] - 2026-10-16

### Changed

- unifi-mcp is rewritten in Go on the official MCP Go SDK, as a drop-in replacement: the tools, their arguments, descriptions and annotations, the prompts, the configuration variables and the output are unchanged (`docs/tools.md` is generated identically, apart from the command named in its header), and every v0.2.0 security fix is kept. The few differences visible from outside:
  - The Docker image is now a single static binary on `gcr.io/distroless/static-debian13` (about 22 MB, with no shell or package manager) and runs as `nonroot` (UID 65532) instead of `node` (UID 1000). Files you mount for `*_FILE` secrets or `UNIFI_CA_FILE` must be readable by that user. The health check is now the binary's own `healthcheck` command.
  - Outside Docker, run the `unifi-mcp` binary (`go install github.com/mattoddie/unifi-mcp/cmd/unifi-mcp@latest` or `make build`) instead of `node dist/index.js`. It also has `version` and `healthcheck` commands, and refuses unknown arguments.
  - At the MCP protocol level, `tools/list` and `prompts/list` return entries in alphabetical order rather than definition order, tool annotations include `idempotentHint: false`, optional prompt arguments omit `required: false`, and calling an unknown tool returns a JSON-RPC error instead of an error result. Clients don't depend on any of these.
  - `UNIFI_CA_FILE` must contain at least one PEM certificate; the server now says so at startup instead of failing on the first request.
  - Connection errors keep the Node-style codes (`ECONNREFUSED`, `ENOTFOUND`, `DEPTH_ZERO_SELF_SIGNED_CERT`, ...) for the common cases; rarer network errors are described in Go's words.

### Fixed

- `unifi_get_dashboard`, and `unifi_get_security_settings` with `include_categories: true`, failed since 0.2.0 because their query string was refused by the new path checks. The query is now sent separately.

## [0.2.0] - 2026-10-15

### Security

- **Breaking:** the HTTP transport now refuses to start without `MCP_AUTH_TOKEN`. Set `MCP_ALLOW_UNAUTHENTICATED=true` only when something in front of the server already authenticates requests.
- **Breaking:** when the server runs with Node directly, `MCP_HTTP_HOST` now defaults to `127.0.0.1` instead of `0.0.0.0`. The Docker image still listens on `0.0.0.0`.
- API paths can no longer escape the Network application. `.` and `..` segments, also percent-encoded (`%2e%2e`), backslashes, control characters, `?` and `#` are refused, and every request is checked to resolve exactly to the intended path. Before, the raw API tool could reach console endpoints outside `/proxy/network` with paths like `/%2e%2e/%2e%2e/api/system`. Site names and object IDs of `.` or `..` are refused too.
- With `UNIFI_ALLOW_DELETES=false`, the raw API tool now also refuses non-`DELETE` requests that delete things, identified by a JSON `cmd` (or path segment) containing delete, forget, revoke, remove, destroy or reset, such as `POST cmd/sitemgr {"cmd": "delete-site"}`.
- New `UNIFI_TLS_FINGERPRINT` pins the controller's certificate by its SHA-256 fingerprint, and new `UNIFI_CA_FILE` trusts a private CA. The server logs a warning at startup when the certificate is neither verified nor pinned, and certificate errors now suggest pinning instead of turning verification off.
- `MCP_ALLOWED_HOSTS` now also rejects requests whose `Origin` header names a host that isn't allowed, and its entries are compared case-insensitively.
- Secret redaction now covers well-known secret fields without the `x_` prefix, such as `password`, `passphrase`, `psk`, `community`, `private_key`, `*_key`, `*secret*`, `*token*`, `configuration_file` and passwords nested in `private_preshared_keys`.

### Changed

- **Breaking:** `unifi_forget_clients` and `unifi_revoke_voucher` are now delete tools, so they need `UNIFI_ALLOW_DELETES=true` as well as `UNIFI_ALLOW_WRITES=true`.
- `unifi_get_firmware_status` with `refresh: true` now needs `UNIFI_ALLOW_WRITES=true`, like `unifi_check_updates`, because it sends a command to the controller.
- The Docker image is based on Node.js 24 LTS again (`node:24-alpine`); Node.js 25 is end of life. Dependabot no longer proposes odd (non-LTS) Node.js majors.

### Fixed

- With username/password auth, concurrent requests that hit an expired session could throw away a session another request had just created and log in again. Re-logins are now tied to the session each request was sent with, and concurrent requests share one login.

## [0.1.0] - 2026-10-11

### Added

- MCP server for UniFi Network with 132 tools in 14 toolsets: overview, devices, clients, switching, wifi, networks, firewall, routing, security, vpn, hotspot, monitoring, admin and raw.
- Five built-in prompts: `network_health_check`, `troubleshoot_client`, `security_review`, `firmware_review` and `wifi_optimization`.
- Support for UniFi OS consoles (API key or local account) and legacy self-hosted controllers (local account), with automatic detection.
- Streamable HTTP (stateless) and stdio transports, with bearer-token auth and a `Host` header allow-list for HTTP.
- Safety switches: read-only by default, `UNIFI_ALLOW_WRITES`, `UNIFI_ALLOW_DELETES` and `UNIFI_TOOLSETS`. Secrets are redacted from output.
- `_FILE` variants of every variable for Docker secrets.
- Multi-arch Docker image (`linux/amd64`, `linux/arm64`) published to GHCR on each release, with SBOM and provenance attestation.
- Documentation, including a tool reference generated from the code.

[Unreleased]: https://github.com/mattoddie/unifi-mcp/compare/v0.6.1...HEAD
[0.6.1]: https://github.com/mattoddie/unifi-mcp/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/mattoddie/unifi-mcp/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/mattoddie/unifi-mcp/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/mattoddie/unifi-mcp/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/mattoddie/unifi-mcp/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/mattoddie/unifi-mcp/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/mattoddie/unifi-mcp/releases/tag/v0.1.0
