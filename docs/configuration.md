# Configuration

unifi-mcp is configured entirely through environment variables. With Docker Compose, put them in `.env`. Copy [`example.env`](../example.env) to get started.

- [Controller connection](#controller-connection)
- [What the assistant may do](#what-the-assistant-may-do)
- [MCP transport](#mcp-transport)
- [Docker secrets (`_FILE` variables)](#docker-secrets-_file-variables)
- [Lazy or full tool loading](#lazy-or-full-tool-loading)
- [Choosing toolsets](#choosing-toolsets)
- [Example configurations](#example-configurations)

The server checks the configuration at startup. If something is wrong, it exits with a clear message such as `UNIFI_ALLOW_DELETES=true also requires UNIFI_ALLOW_WRITES=true`. If the container keeps restarting, check `docker compose logs`.

## Controller connection

| Variable | Default | Description |
|---|---|---|
| `UNIFI_URL` | **required** | Controller URL, for example `https://192.168.1.1` or `https://unifi.local:8443`. Don't include a path or a username and password. `http://` works but sends credentials unencrypted, and logs a warning. |
| `UNIFI_API_KEY` | | API key. UniFi OS consoles only, Network 9.0+. Takes priority over username/password. |
| `UNIFI_USERNAME` | | Local account username, used when no API key is set. |
| `UNIFI_PASSWORD` | | Local account password. |
| `UNIFI_SITE` | `default` | Site to use when a tool call doesn't name one. Use the site's **API name**, which is the `name` field from `unifi_list_sites` (e.g. `default`, `x7k2pq9d`), not its display name. |
| `UNIFI_VERIFY_SSL` | unset (`true` when `UNIFI_CA_FILE` is set) | Verify the controller's TLS certificate against trusted CAs. For an `https` URL, the server refuses to start unless this, `UNIFI_CA_FILE` or `UNIFI_TLS_FINGERPRINT` is set. For the self-signed certificate that consoles ship with, pin it with `UNIFI_TLS_FINGERPRINT` (and leave this unset). Set it to `true` if you've installed a trusted certificate. `false` skips the check, which lets anyone on the network path impersonate the controller; the server logs a warning. |
| `UNIFI_TLS_FINGERPRINT` | | SHA-256 fingerprint of the controller's certificate, as `AA:BB:…` or plain hex. Connections to any other certificate are refused, even with verification off. Comma-separate several to allow for a certificate change. See [Security: TLS to the controller](security.md#about-tls-to-the-controller). |
| `UNIFI_CA_FILE` | | PEM file with the CA certificate(s) that signed the controller's certificate, for a private CA. Turns verification on. The certificate's name must match `UNIFI_URL`, unless `UNIFI_TLS_FINGERPRINT` is also set. |
| `UNIFI_CONTROLLER_TYPE` | `auto` | `auto`, `unifi-os` or `legacy`. With `auto`, the server detects the type on first use. Set it explicitly if detection fails, for example behind an unusual reverse proxy. |
| `UNIFI_TIMEOUT_MS` | `30000` | Timeout for each controller request, in milliseconds. |

Boolean variables accept `true`/`false`, `1`/`0`, `yes`/`no` and `on`/`off`.

### How authentication works

- **API key:** every request sends an `X-API-KEY` header. There are no sessions and no logins.
- **Username/password:** the server logs in on first use and keeps the session cookie (plus the CSRF token on UniFi OS). If the session expires, it logs in again and retries the request once. Several requests arriving together share a single login.

## What the assistant may do

| Variable | Default | Description |
|---|---|---|
| `UNIFI_ALLOW_WRITES` | `false` | Register tools that change state: restarting devices, blocking clients, editing WiFi, firewall, networks and so on. |
| `UNIFI_ALLOW_DELETES` | `false` | Also register tools that delete or forget things, such as networks, WLANs, firewall rules, sites, clients' history or hotspot vouchers. Requires `UNIFI_ALLOW_WRITES=true`. |
| `UNIFI_TOOLSETS` | `all` | Comma-separated list of [toolsets](#choosing-toolsets) to expose. |
| `UNIFI_TOOL_MODE` | `lazy` | `lazy` lists three meta-tools that find and call the others, which saves the model's context. `full` lists every enabled tool directly. See [Lazy or full tool loading](#lazy-or-full-tool-loading). |

Tools that aren't allowed are **not registered at all**. The assistant can't find them, so it can't call them or be talked into calling them.

## Audit log

| Variable | Default | Description |
|---|---|---|
| `UNIFI_AUDIT_LOG` | (off) | Record every call of a tool that changes things. A file path appends JSON Lines to that file, which is created with mode `0600` if it doesn't exist. `stderr` writes the lines to standard error, next to the server's other logs. |

Each line is one call of a write or delete tool, or of `unifi_api_request` with a method other than `GET`. Dry runs, calls that failed and calls refused for invalid arguments are all logged:

```json
{"time":"2026-10-21T09:14:03.120Z","tool":"unifi_save_wlan","dry_run":false,"arguments":{"id":"64f0c2…","name":"Guest","passphrase":"[redacted]"},"requests":[{"method":"PUT","path":"/api/s/default/rest/wlanconf/64f0c2…","status":200}],"outcome":"ok"}
```

| Field | Meaning |
|---|---|
| `time` | When the call finished, in UTC (RFC 3339). |
| `tool` | The tool name. In lazy mode this is the tool that `unifi_call_write_tool` ran, such as `unifi_save_wlan`. |
| `dry_run` | Whether the call was a [dry run](usage.md#previewing-changes-with-dry_run). |
| `arguments` | The arguments, with passwords, keys and other secrets `[redacted]`. |
| `requests` | The requests that change state that were actually sent: method, path and HTTP status (`null` if no response arrived). Reads aren't listed, and nothing from the responses is logged. |
| `previewed` | For a dry run, the request that was previewed and not sent. |
| `outcome` | `ok` or `error`. |
| `error` | The error message, when `outcome` is `error`. |

The server checks at startup that it can write to the file, and refuses to start if the directory doesn't exist or isn't writable. A write that fails later doesn't stop the tool call: the server logs one warning to standard error and carries on.

The file is opened for each entry, so it can be rotated by moving it away; the next entry creates a new one.

In Docker, the image runs as UID 65532 with no writable directories of its own, so mount one for the log:

```yaml
services:
  unifi-mcp:
    environment:
      UNIFI_AUDIT_LOG: /audit/unifi-mcp.jsonl
    volumes:
      - ./audit:/audit
```

Create the directory first and let UID 65532 write to it, for example `mkdir audit && sudo chown 65532 audit`. `UNIFI_AUDIT_LOG=stderr` needs no volume: the lines appear in `docker compose logs`.

## MCP transport

| Variable | Default | Description |
|---|---|---|
| `MCP_TRANSPORT` | `http` in the Docker image (`stdio` when the binary is run directly) | `http` for Streamable HTTP, or `stdio`. |
| `MCP_HTTP_HOST` | `0.0.0.0` in the Docker image (`127.0.0.1` when the binary is run directly) | Address to listen on (HTTP only). The `healthcheck` command connects to this address, or to `127.0.0.1` when it is `0.0.0.0` or `::`. |
| `MCP_HTTP_PORT` | `8080` | Port to listen on (HTTP only), also used by the `healthcheck` command. |
| `MCP_AUTH_TOKEN` | **required for HTTP** | Every request to `/mcp` must send `Authorization: Bearer <token>`. Generate one with `openssl rand -hex 32`. The server refuses to start in HTTP mode without it, or if it is shorter than 16 characters. |
| `MCP_ALLOW_UNAUTHENTICATED` | `false` | Start the HTTP transport without `MCP_AUTH_TOKEN`. Only set this when something in front of the server, such as an authenticating reverse proxy, already checks every request. It also requires `MCP_ALLOWED_HOSTS`: without a token, the Host check is what stops a web page from reaching the server through DNS rebinding. |
| `MCP_ALLOWED_HOSTS` | | Comma-separated allow-list of host names, such as `unifi-mcp.lan,unifi-mcp.lan:8080` (case-insensitive). For the `Host` header, an entry without a port matches any port, and `host:port` matches only that port. For the `Origin` header (sent by browsers), an entry without a port matches only the scheme's default port (80 or 443), so a page served from another port needs an explicit `host:port` entry. Requests with any other `Host`, or with an `Origin` that is not listed (including an empty or `null` one), are rejected. This protects against DNS-rebinding and cross-site attacks from web pages in your browser. |

The HTTP server exposes these endpoints:

| Path | Method | Purpose |
|---|---|---|
| `/mcp` | `POST` | The MCP endpoint (stateless Streamable HTTP, JSON responses). Other methods return `405`. |
| `/health`, `/healthz` | `GET` | Liveness check. Returns `{"status":"ok"}` and doesn't contact the controller. |

## Docker secrets (`_FILE` variables)

Every variable can also be read from a file: set `<NAME>_FILE` to the file's path. Leading and trailing whitespace is trimmed. This works with [Docker secrets](https://docs.docker.com/compose/how-tos/use-secrets/) and Kubernetes secret volumes:

```yaml
services:
  unifi-mcp:
    image: ghcr.io/mattoddie/unifi-mcp:latest
    environment:
      UNIFI_URL: https://192.168.1.1
      UNIFI_TLS_FINGERPRINT: "AB:CD:…"   # the console's certificate; see Security
      UNIFI_API_KEY_FILE: /run/secrets/unifi_api_key
      MCP_AUTH_TOKEN_FILE: /run/secrets/mcp_auth_token
    secrets: [unifi_api_key, mcp_auth_token]
    ports: ["8080:8080"]

secrets:
  unifi_api_key:
    file: ./secrets/unifi_api_key.txt
  mcp_auth_token:
    file: ./secrets/mcp_auth_token.txt
```

If both `NAME` and `NAME_FILE` are set, the file wins.

## Lazy or full tool loading

`UNIFI_TOOL_MODE` decides how the tools are offered to the client.

**`lazy` (the default)** lists three small meta-tools instead of every tool. The whole tool list is under 3 KB, against about 48 KB for the 59 read-only tools and 174 KB for all 145 tools in `full` mode:

| Meta-tool | What it does |
|---|---|
| `unifi_search_tools` | Finds tools by keywords or toolset and returns a one-line summary of each. With `names`, it returns each tool's full description and input schema. With no arguments, it lists the toolsets. |
| `unifi_call_tool` | Runs a read-only tool: `{"name": "unifi_list_devices", "arguments": {"type": "switch"}}`. It refuses tools that change state. Marked read-only, so clients don't ask before using it. |
| `unifi_call_write_tool` | Runs a tool that changes state. Only listed when `UNIFI_ALLOW_WRITES=true`. Marked destructive, so clients ask you before **every** call, and its description tells the model to confirm the change with you first. It refuses read-only tools. |

Behind them, everything works as in `full` mode: the same tools, arguments, validation errors, results and gates. A tool that writes, deletes or belongs to a toolset you haven't enabled can't be found or called. The raw API tool, `unifi_api_request`, counts as a write tool when writes are enabled, because it can then send `POST` and `PUT`.

**`full`** lists every enabled tool directly, as earlier versions did.

Which to choose:

- **Lazy** suits clients that load every tool into the model's context, such as Claude Desktop, Cursor, VS Code and most others. It leaves more room for your conversation and the controller's data, and stays clear of clients' limits on the number of tools. The model spends an extra call or two finding a tool the first time.
- **Full** is a good choice for Claude Code, which already defers large MCP tool lists behind its own tool search. It also lets you write permission rules for individual tools, such as allowing `mcp__unifi__unifi_list_devices` without asking. In lazy mode a client sees only the three meta-tools, so its permission rules can tell reads from writes, but not one write from another.

`UNIFI_TOOLSETS`, `UNIFI_ALLOW_WRITES` and `UNIFI_ALLOW_DELETES` apply in both modes.

## Choosing toolsets

In `full` mode, every tool's name, description and argument schema is sent to the model, which uses up context. With all 145 tools enabled, that's a lot of text, and some clients warn about or cap the number of tools. [Lazy mode](#lazy-or-full-tool-loading) avoids that, but narrower toolsets still help the model find the right tool, and limit what it can reach. Expose only what you need:

| Toolset | Tools | Use it for |
|---|---:|---|
| `overview` | 3 | Sites and health. **Always include it.** |
| `devices` | 19 | APs, switches, gateways, firmware |
| `clients` | 8 | Who's connected, blocking |
| `switching` | 7 | Switch ports, PoE |
| `wifi` | 4 | SSIDs |
| `networks` | 13 | VLANs, WAN, DNS, bandwidth profiles |
| `firewall` | 20 | Port forwards, rules, policies, zones, traffic rules |
| `routing` | 6 | Static routes, traffic routes |
| `security` | 7 | IDS/IPS, country blocking, content filters |
| `vpn` | 9 | VPNs, RADIUS |
| `hotspot` | 10 | Vouchers, guests |
| `monitoring` | 13 | Events, alarms, stats, logs |
| `admin` | 12 | Settings, admins, backups |
| `raw` | 1 | Any API path. Handy, but gives broad access. |

The counts include write and delete tools, so a read-only setup registers far fewer tools. Some suggested sets:

| Goal | `UNIFI_TOOLSETS` |
|---|---|
| Everyday monitoring | `overview,devices,clients,monitoring` |
| WiFi tuning | `overview,devices,clients,wifi,monitoring` |
| Firewall and security review | `overview,networks,firewall,routing,security,monitoring` |
| Guest network operations | `overview,clients,hotspot,wifi` |
| Everything (default) | `all` |

## Example configurations

**Read-only monitoring** (the safest setup):

```sh
UNIFI_URL=https://192.168.1.1
UNIFI_API_KEY=...
UNIFI_TLS_FINGERPRINT=AB:CD:…   # or UNIFI_VERIFY_SSL=false; see Security
MCP_AUTH_TOKEN=...
UNIFI_TOOLSETS=overview,devices,clients,monitoring
```

**Day-to-day admin.** Changes are allowed, but nothing can be deleted:

```sh
UNIFI_URL=https://192.168.1.1
UNIFI_API_KEY=...
UNIFI_TLS_FINGERPRINT=AB:CD:…   # or UNIFI_VERIFY_SSL=false; see Security
MCP_AUTH_TOKEN=...
UNIFI_ALLOW_WRITES=true
```

**Legacy self-hosted controller** with a trusted certificate:

```sh
UNIFI_URL=https://unifi.example.lan:8443
UNIFI_USERNAME=mcp
UNIFI_PASSWORD=...
UNIFI_VERIFY_SSL=true
UNIFI_CONTROLLER_TYPE=legacy
MCP_AUTH_TOKEN=...
```
