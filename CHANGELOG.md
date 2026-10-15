# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

For unifi-mcp, the "public API" is the set of tool names, their arguments and the configuration variables. Renaming or removing any of these is a breaking change.

## [Unreleased]

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

[Unreleased]: https://github.com/mattoddie/unifi-mcp/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/mattoddie/unifi-mcp/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/mattoddie/unifi-mcp/releases/tag/v0.1.0
