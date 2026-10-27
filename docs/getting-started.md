# Getting started

This guide takes you from nothing to asking your first question about your network. It takes about ten minutes.

- [What you need](#what-you-need)
- [1. Find your controller URL](#1-find-your-controller-url)
- [2. Create credentials](#2-create-credentials)
- [3. Run the server](#3-run-the-server)
- [4. Connect your assistant](#4-connect-your-assistant)
- [5. Ask something](#5-ask-something)
- [Next steps](#next-steps)

## What you need

- **A UniFi Network controller.** This can be a UniFi OS console (Dream Machine, Cloud Gateway, Dream Router, Express, Cloud Key Gen2+, UniFi OS Server) or a self-hosted UniFi Network application. See [Compatibility](compatibility.md) for the full list.
- **A machine that runs Docker** and can reach the controller over the network. This could be a NAS, a home server, a Raspberry Pi 4/5 or your laptop. The image supports both `amd64` and `arm64`.
- **An MCP client**, such as [Claude Code](https://claude.com/claude-code), Claude Desktop, VS Code, Cursor or Codex.

> [!TIP]
> **Using Claude Desktop on a Mac or Windows PC?** You don't need Docker. Do steps 1 and 2, then install the [extension bundle](clients.md#claude-desktop) from the releases page and enter the URL and credentials in its settings, instead of steps 3 and 4.

## 1. Find your controller URL

This is the address you use to open the UniFi web interface, without any path:

| Controller | Typical URL |
|---|---|
| UniFi OS console (UDM, UCG, UDR, UX, Cloud Key Gen2+) | `https://192.168.1.1` (the console's LAN IP) |
| UniFi OS Server | `https://<server>:11443` |
| Legacy self-hosted Network application | `https://<server>:8443` |

Use the **local** address, not `unifi.ui.com`. The server talks to the controller directly over your LAN.

## 2. Create credentials

There are two ways to authenticate. Use an API key if you can.

### Option A: API key (UniFi OS, recommended)

1. Open **UniFi Network → Settings → Control Plane → Integrations**.
2. Click **Create API Key**, give it a name like `unifi-mcp`, and copy the key. You can't see it again later.

API keys need UniFi Network 9.0 or later on a UniFi OS console. The key has the same permissions as the admin who created it.

### Option B: Local account (any controller)

1. On UniFi OS, open **UniFi OS → Admins & Users → Add Admin**. On a legacy controller, open **Settings → Admins**.
2. Tick **Restrict to local access only**, then set a username and password.
3. Choose a role:
   - **View Only** if you only want the assistant to read.
   - **Site Admin** or a custom role with Network admin rights if you want to turn on writes.

> [!IMPORTANT]
> Ubiquiti cloud (SSO) accounts, especially those with MFA, can't log in through the API. Use a local account.

## 3. Run the server

Create a folder and download the compose file and the example settings:

```sh
mkdir unifi-mcp && cd unifi-mcp
curl -fsSLO https://raw.githubusercontent.com/mattoddie/unifi-mcp/main/compose.yaml
curl -fsSL https://raw.githubusercontent.com/mattoddie/unifi-mcp/main/example.env -o .env
```

Edit `.env` and set at least:

```sh
UNIFI_URL=https://192.168.1.1
UNIFI_API_KEY=your-api-key          # or UNIFI_USERNAME + UNIFI_PASSWORD
MCP_AUTH_TOKEN=a-long-random-string # required; generate one with: openssl rand -hex 32
UNIFI_TLS_FINGERPRINT=AB:CD:…       # the console's certificate (see below)
```

The server won't start without `MCP_AUTH_TOKEN` (at least 16 characters). For an `https` controller it also won't start until you say how to check the controller's certificate. Pin the console's self-signed certificate with `UNIFI_TLS_FINGERPRINT`, so nobody on the network can impersonate the controller; [Security](security.md#about-tls-to-the-controller) shows how to get the fingerprint. Alternatively, trust its CA with `UNIFI_CA_FILE`, or set `UNIFI_VERIFY_SSL=true` for a publicly trusted certificate. `example.env` sets `UNIFI_VERIFY_SSL=false`, which skips the check: it works, but anyone on the network path could impersonate the controller.

Start it:

```sh
docker compose up -d
docker compose logs -f unifi-mcp
```

You should see lines like these:

```text
unifi-mcp 1.0.0: controller https://192.168.1.1, site "default", auth API key, writes disabled, secrets redacted, audit log off, transport http
unifi-mcp listening on http://0.0.0.0:8080/mcp (bearer auth enabled)
```

A `WARNING: the controller's TLS certificate is not verified … and not pinned` line means `UNIFI_VERIFY_SSL=false` is set and `UNIFI_TLS_FINGERPRINT` isn't. Everything works, but pinning is recommended.

Check that it's up from another machine:

```sh
curl http://<docker-host>:8080/health
# {"status":"ok"}
```

> [!TIP]
> `/health` only shows that the server is running. It doesn't contact the controller. Wrong credentials show up as an error the first time a tool is used, so step 5 is the real test.

## 4. Connect your assistant

For **Claude Code**:

```sh
claude mcp add --transport http unifi http://<docker-host>:8080/mcp \
  --header "Authorization: Bearer <MCP_AUTH_TOKEN>"
```

Run `claude mcp list` to check that `unifi` shows as connected. For other clients, see [Connecting MCP clients](clients.md).

## 5. Ask something

Start a new conversation and try:

- *"List my UniFi sites and give me a quick health summary."*
- *"Which access points have the most clients?"*
- *"Show me any alarms from the last day."*

The assistant calls tools like `unifi_get_site_health` and `unifi_list_devices` and summarises the results. In Claude Code you can also run the built-in prompts as slash commands, for example `/mcp__unifi__network_health_check`.

If something goes wrong, the tool's error message usually says what to fix. [Troubleshooting](troubleshooting.md) covers the common errors.

## Next steps

- **Let it make changes.** Set `UNIFI_ALLOW_WRITES=true`, and read [Security](security.md) first.
- **Expose fewer tools** with `UNIFI_TOOLSETS` for a faster, more focused assistant. See [Configuration](configuration.md#keeping-the-tool-list-small).
- **Put it behind HTTPS** if it's reachable outside your LAN. See [Deployment](deployment.md#tls-with-a-reverse-proxy).
- **Learn what it can do** in [Using unifi-mcp](usage.md) and the [tool reference](tools.md).
