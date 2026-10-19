# Connecting MCP clients

unifi-mcp supports both standard MCP transports:

| Transport | How it runs | Best for |
|---|---|---|
| **Streamable HTTP** (the default in the image) | One long-running container. Clients connect to `http://<host>:8080/mcp`. | Most setups. One server can be shared by several clients and machines. |
| **stdio** | The client starts a fresh container for each session with `docker run -i`. | A single machine, no network service, or clients that only support stdio. |

The HTTP endpoint is **stateless**: each request is handled on its own, so you can restart the container at any time. Every request must send `Authorization: Bearer <token>` with your `MCP_AUTH_TOKEN`, which the HTTP transport requires.

In the examples below, replace `<docker-host>` with the machine running the container and `<token>` with your `MCP_AUTH_TOKEN`.

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

## Claude Desktop

Claude Desktop starts local MCP servers over stdio, so use [stdio mode](#stdio-mode). Open **Settings → Developer → Edit Config** and add:

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
