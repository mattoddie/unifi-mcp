# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

For unifi-mcp, the "public API" is the set of tool names, their arguments and the configuration variables. Renaming or removing any of these is a breaking change.

## [Unreleased]

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

[Unreleased]: https://github.com/mattoddie/unifi-mcp/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/mattoddie/unifi-mcp/releases/tag/v0.1.0
