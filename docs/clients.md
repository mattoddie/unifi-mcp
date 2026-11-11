# Connecting MCP clients

unifi-mcp supports both standard MCP transports:

| Transport | How it runs | Best for |
|---|---|---|
| **Streamable HTTP** (the default in the image) | One long-running container. Clients connect to `http://<host>:8080/mcp`. | Most setups. One server can be shared by several clients and machines. |
| **stdio** | The client starts a fresh container for each session with `docker run -i`. | A single machine, no network service, or clients that only support stdio. |

On **Claude Desktop** you can skip Docker altogether: install the [extension bundle](#claude-desktop), which runs the server for you over stdio.

The HTTP endpoint is **stateless**: each request is handled on its own, so you can restart the container at any time. Every request must send `Authorization: Bearer <token>` with your `MCP_AUTH_TOKEN`, which the HTTP transport requires.

In the examples below, replace `<docker-host>` with the machine running the container and `<token>` with your `MCP_AUTH_TOKEN`.

Most clients load every tool's definition into the model's context: about 48 KB for the 59 read-only tools, and up to 174 KB for all 145 tools with writes and deletes on. Enable only the toolsets you need: see [Keeping the tool list small](configuration.md#keeping-the-tool-list-small). Claude Code is the exception, see [below](#claude-code).

- [Claude Code](#claude-code)
- [Claude Desktop](#claude-desktop)
- [VS Code (GitHub Copilot)](#vs-code-github-copilot)
- [Cursor](#cursor)
- [OpenAI Codex CLI](#openai-codex-cli)
- [Other clients](#other-clients)
- [stdio mode](#stdio-mode)

## Claude Code

```sh
claude mcp add --transport http unifi http://<docker-host>:8080/mcp \
  --header "Authorization: Bearer <token>"
```

Add `--scope user` to make it available in every project, or `--scope project` to save it in a `.mcp.json` you can share with your team. Don't commit the token: in `.mcp.json`, reference an environment variable instead:

```json
{
  "mcpServers": {
    "unifi": {
      "type": "http",
      "url": "http://<docker-host>:8080/mcp",
      "headers": { "Authorization": "Bearer ${UNIFI_MCP_TOKEN}" }
    }
  }
}
```

Run `claude mcp list` to check the connection, and `/mcp` inside Claude Code to see the tools. The built-in prompts are available as slash commands, such as `/mcp__unifi__security_review`.

Claude Code defers large MCP tool lists behind its own tool search, so the full list doesn't fill the model's context and you don't need to narrow `UNIFI_TOOLSETS` for it. You can write permission rules for individual tools, such as allowing `mcp__unifi__unifi_list_devices` without asking.

## Claude Desktop

The easiest way is the **extension bundle**: a single `.mcpb` file that installs unifi-mcp into Claude Desktop with a settings form. It needs no Docker, no service and no token, because Claude Desktop runs the server itself over stdio.

### Install the extension

1. Open the [latest release](https://github.com/mattoddie/unifi-mcp/releases/latest) and download the bundle for your computer:

   | Computer | Bundle |
   |---|---|
   | Mac with Apple silicon (M1 and later) | `unifi-mcp-<version>-darwin-arm64.mcpb` |
   | Mac with an Intel processor | `unifi-mcp-<version>-darwin-amd64.mcpb` |
   | Windows on x64 (most PCs) | `unifi-mcp-<version>-windows-amd64.mcpb` |
   | Windows on Arm | `unifi-mcp-<version>-windows-arm64.mcpb` |

   There are Linux bundles too (`linux-amd64`, `linux-arm64`) for other clients that install MCP Bundles.

2. Double-click the file, or drag it onto **Settings → Extensions** in Claude Desktop, and click **Install**.
3. Fill in the settings and turn the extension on. The tools appear under the tools (🔨) menu, and the prompts under the **+** menu. You can allow or block each tool separately there.

To update, install the bundle from a newer release the same way.

### Settings

| Setting | Variable | What to enter |
|---|---|---|
| **Controller URL** (required) | `UNIFI_URL` | The console's local address, such as `https://192.168.1.1`. See [Getting started](getting-started.md#1-find-your-controller-url). |
| **API key** | `UNIFI_API_KEY` | An API key from **UniFi Network → Settings → Control Plane → Integrations**. Recommended on UniFi OS consoles. |
| **Username**, **Password** | `UNIFI_USERNAME`, `UNIFI_PASSWORD` | A local account, instead of an API key. Needed for legacy controllers. |
| **TLS certificate fingerprint** | `UNIFI_TLS_FINGERPRINT` | **Recommended.** The SHA-256 fingerprint of the console's certificate, so nobody on the network can impersonate it. |
| **Verify certificate (true/false)** | `UNIFI_VERIFY_SSL` | Leave empty when you set a fingerprint. `true` for a publicly trusted certificate, `false` to skip the check (not recommended). |
| **CA certificate file** | `UNIFI_CA_FILE` | A PEM file with the CA that signed the controller's certificate, instead of a fingerprint. |
| **Site** | `UNIFI_SITE` | The site used when a request doesn't name one. Usually `default`. |
| **Allow changes** | `UNIFI_ALLOW_WRITES` | Off by default, which keeps the assistant read-only. Read [Security](security.md) before turning it on. |
| **Allow deletes** | `UNIFI_ALLOW_DELETES` | Also allows deleting configuration and forgetting clients. Needs **Allow changes**. |
| **Allow secrets** | `UNIFI_ALLOW_SECRETS` | Lets Claude ask for unredacted passwords and keys, and use the WiFi QR tool. Off keeps all output redacted. |
| **Toolsets** | `UNIFI_TOOLSETS` | Optional, such as `overview,devices,clients,wifi,monitoring`. Empty means all, which is 48 KB of the model's context read-only and up to 174 KB with changes allowed. See [Keeping the tool list small](configuration.md#keeping-the-tool-list-small). |
| **Audit log file** | `UNIFI_AUDIT_LOG` | Optional full path to a file, such as `/Users/you/unifi-mcp-audit.jsonl`, that records every change Claude makes or previews. The folder must exist; the file is created. `stderr` writes the entries to the extension's log instead. Off when empty. See [Audit log](configuration.md#audit-log). |

Fill in either the API key or the username and password. Settings you leave empty use their defaults, as described in [Configuration](configuration.md).

For an `https` controller, the server won't start until you say how to check its certificate. Most consoles use a self-signed certificate, so **set the TLS certificate fingerprint**. Get it with:

```sh
openssl s_client -connect 192.168.1.1:443 </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256
```

You can also read it from your browser's certificate viewer. See [Security](security.md#about-tls-to-the-controller) for more.

### If it doesn't start

- **Read the error.** Claude Desktop shows that the extension failed to start. The server's log, which says what's wrong (for example a missing certificate choice or a bad fingerprint), is in `~/Library/Logs/Claude/` on macOS and `%APPDATA%\Claude\logs\` on Windows, in a file named after the extension.
- **macOS blocks the server.** The binary in the bundle isn't notarised by Apple. If macOS says it can't check it for malicious software, open **System Settings → Privacy & Security** and click **Open Anyway** next to the message about `unifi-mcp`, then turn the extension off and on again.
- **A password with `$` in it fails to log in.** Some Claude Desktop versions alter setting values that contain `$`. Use an API key, or a password without `$`.

### Or run it with Docker

If you'd rather run the container image, use [stdio mode](#stdio-mode). Open **Settings → Developer → Edit Config** and add:

```json
{
  "mcpServers": {
    "unifi": {
      "command": "docker",
      "args": [
        "run", "-i", "--rm",
        "-e", "MCP_TRANSPORT=stdio",
        "-e", "UNIFI_URL", "-e", "UNIFI_API_KEY", "-e", "UNIFI_TLS_FINGERPRINT",
        "ghcr.io/mattoddie/unifi-mcp:latest"
      ],
      "env": {
        "UNIFI_URL": "https://192.168.1.1",
        "UNIFI_API_KEY": "your-api-key",
        "UNIFI_TLS_FINGERPRINT": "AB:CD:…"
      }
    }
  }
}
```

Restart Claude Desktop. The tools appear under the tools (🔨) menu, and the prompts appear under **+ → Add from unifi**.

## VS Code (GitHub Copilot)

Create `.vscode/mcp.json` in your workspace, or run **MCP: Add Server** from the command palette:

```json
{
  "inputs": [
    { "id": "unifi-token", "type": "promptString", "description": "unifi-mcp token", "password": true }
  ],
  "servers": {
    "unifi": {
      "type": "http",
      "url": "http://<docker-host>:8080/mcp",
      "headers": { "Authorization": "Bearer ${input:unifi-token}" }
    }
  }
}
```

VS Code asks for the token once and stores it securely. Use the tools from Copilot Chat in **Agent** mode.

## Cursor

Add to `~/.cursor/mcp.json` for all projects, or `.cursor/mcp.json` for one project:

```json
{
  "mcpServers": {
    "unifi": {
      "url": "http://<docker-host>:8080/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

## OpenAI Codex CLI

Add to `~/.codex/config.toml`:

```toml
[mcp_servers.unifi]
url = "http://<docker-host>:8080/mcp"
bearer_token_env_var = "UNIFI_MCP_TOKEN"
```

Then `export UNIFI_MCP_TOKEN=<token>` before running `codex`.

## Other clients

Any client that supports MCP **Streamable HTTP** works. Point it at `http://<docker-host>:8080/mcp` and send the bearer token header.

Clients that install servers from the [MCP Registry](https://registry.modelcontextprotocol.io) can find unifi-mcp as `io.github.mattoddie/unifi-mcp`. The registry entry runs the image in [stdio mode](#stdio-mode) and asks for the same settings as the bundle, except the CA file: use `UNIFI_TLS_FINGERPRINT` for a self-signed certificate.

If your client only supports stdio, you have two options. You can use [stdio mode](#stdio-mode), or you can keep the shared HTTP server and connect through a stdio-to-HTTP bridge such as [`mcp-remote`](https://www.npmjs.com/package/mcp-remote):

```json
{
  "command": "npx",
  "args": ["-y", "mcp-remote", "http://<docker-host>:8080/mcp", "--header", "Authorization: Bearer <token>"]
}
```

## stdio mode

In stdio mode the MCP client runs the container itself and talks to it over stdin/stdout. No port is opened, and no `MCP_AUTH_TOKEN` is needed because only the client can reach the server.

The general shape is:

```sh
docker run -i --rm \
  -e MCP_TRANSPORT=stdio \
  -e UNIFI_URL=https://192.168.1.1 \
  -e UNIFI_API_KEY=your-api-key \
  -e UNIFI_TLS_FINGERPRINT=AB:CD:… \
  ghcr.io/mattoddie/unifi-mcp:latest
```

Tips:

- **Tell it how to trust the controller's certificate.** For an `https` controller the server won't start without `UNIFI_TLS_FINGERPRINT` (recommended), `UNIFI_CA_FILE` or an explicit `UNIFI_VERIFY_SSL` (`true`, or `false` to skip the check). See [Security](security.md#about-tls-to-the-controller).
- **Keep secrets out of the client config.** Pass `-e NAME` without a value so Docker copies the variable from the client's environment, as in the Claude Desktop example. You can also use `--env-file /path/to/unifi.env`.
- **Always pass `-i` and never `-t`.** A TTY corrupts the MCP message stream.
- **Docker on another machine** works too. Set `DOCKER_HOST` in the client's `env`, and note that `UNIFI_URL` must be reachable from that machine.
- **Pin a version** (for example `:1.2.3`) so a new `latest` doesn't surprise you. See [Deployment](deployment.md#image-tags).
