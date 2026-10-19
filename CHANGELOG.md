# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

For unifi-mcp, the "public API" is the set of tool names, their arguments and the configuration variables. Renaming or removing any of these is a breaking change.

## [Unreleased]

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

[Unreleased]: https://github.com/mattoddie/unifi-mcp/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/mattoddie/unifi-mcp/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/mattoddie/unifi-mcp/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/mattoddie/unifi-mcp/releases/tag/v0.1.0
