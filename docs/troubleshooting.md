# Troubleshooting

Start with the logs: `docker compose logs unifi-mcp`. Configuration errors stop the server at startup with a clear message. Controller errors show up in the tool results the assistant sees, and you can usually ask it *"what error did you get?"*.

- [The container keeps restarting](#the-container-keeps-restarting)
- [The client can't connect to the server](#the-client-cant-connect-to-the-server)
- [Login and permission errors](#login-and-permission-errors)
- [Certificate and connection errors](#certificate-and-connection-errors)
- ["Redirected" errors](#redirected-errors)
- ["Endpoint not found"](#endpoint-not-found)
- [Site errors](#site-errors)
- [A change didn't take effect](#a-change-didnt-take-effect)
- [Tools are missing](#tools-are-missing)
- [Slow or timing-out tools](#slow-or-timing-out-tools)
- [Still stuck?](#still-stuck)

## The container keeps restarting

Run `docker compose logs unifi-mcp`. The last line names the problem, for example:

| Message | Fix |
|---|---|
| `UNIFI_URL is required` | Set `UNIFI_URL` in `.env`, and check that `env_file: .env` is in `compose.yaml`. |
| `Set either UNIFI_API_KEY or both UNIFI_USERNAME and UNIFI_PASSWORD` | Add credentials. |
| `API keys are only supported by UniFi OS consoles` | You set `UNIFI_CONTROLLER_TYPE=legacy` with an API key. Use a local account instead. |
| `UNIFI_ALLOW_DELETES=true also requires UNIFI_ALLOW_WRITES=true` | Turn on writes too, or turn deletes off. |
| `Unknown toolset "x" in UNIFI_TOOLSETS` | Fix the typo. The message lists the valid names. |
| `Could not read UNIFI_API_KEY_FILE (…)` | The secret file isn't mounted where the variable points. |
| `MCP_AUTH_TOKEN is required for the http transport …` | Set `MCP_AUTH_TOKEN` (generate one with `openssl rand -hex 32`) and send it from your client. Only if something else in front of the server authenticates requests, set `MCP_ALLOW_UNAUTHENTICATED=true` instead. |
| `MCP_AUTH_TOKEN must be at least 16 characters` | Use a longer token, for example `openssl rand -hex 32`. |
| `MCP_ALLOW_UNAUTHENTICATED=true also requires MCP_ALLOWED_HOSTS …` | List the host names your clients use to reach the server in `MCP_ALLOWED_HOSTS`, or set `MCP_AUTH_TOKEN` instead. |
| `UNIFI_TLS_FINGERPRINT: … is not a SHA-256 certificate fingerprint` | Use the 64-hex-digit SHA-256 fingerprint, with or without colons. SHA-1 fingerprints are too short. |
| `UNIFI_URL is https, but nothing says how to check the controller's certificate …` | Set `UNIFI_TLS_FINGERPRINT` to the console's certificate fingerprint (recommended), `UNIFI_CA_FILE` to your CA, or `UNIFI_VERIFY_SSL=true` for a trusted certificate. `UNIFI_VERIFY_SSL=false` skips the check, as older versions did by default. See [Security](security.md#about-tls-to-the-controller). |
| `UNIFI_URL must not contain a username or password` | Remove `user:pass@` from the URL and use `UNIFI_API_KEY`, or `UNIFI_USERNAME` and `UNIFI_PASSWORD`. |
| `Could not read UNIFI_CA_FILE (…)` | The CA file isn't mounted where the variable points. |

## The client can't connect to the server

1. **Is it running?** `curl http://<docker-host>:8080/health` should return `{"status":"ok"}`.
2. **Right host?** The port is published on the machine running Docker. If Docker runs on another machine (for example with `DOCKER_HOST`), `localhost` won't work.
3. **Right path?** The endpoint is `/mcp`, so the URL is `http://<docker-host>:8080/mcp`.
4. **`401 Unauthorized`:** the client isn't sending `Authorization: Bearer <MCP_AUTH_TOKEN>`, or the token doesn't match. Watch for stray quotes or spaces in `.env`.
5. **`403 Host … is not allowed` or `403 Origin … is not allowed`:** add the hostname the client uses to `MCP_ALLOWED_HOSTS`, or unset it. Browser-based clients send an `Origin` header, which must also name an allowed host; for a page on a non-default port, list it as `host:port`.
6. **`405 Method not allowed`:** the client tried to open an SSE stream with `GET`. The server is stateless and only accepts `POST`. Make sure the client is set to *Streamable HTTP* (often called `http`), not `sse`.
7. **Connection refused without Docker:** run outside Docker, the server listens on `127.0.0.1` only. Set `MCP_HTTP_HOST=0.0.0.0` (and keep `MCP_AUTH_TOKEN` set) to accept connections from other machines.

## Login and permission errors

**`Login to UniFi controller failed (HTTP 401)` / `api.err.Invalid`**

- Check the username and password by logging in to the UniFi web UI with them.
- The account must be **local**. Ubiquiti cloud and SSO accounts, especially with MFA, can't log in through the API.
- On UniFi OS, too many failed logins temporarily lock the account. Wait a few minutes.

**`… failed with HTTP 401/403 … (check credentials and that the account/API key has sufficient permissions)`**

- With an API key: check it was copied completely and hasn't been deleted in **Settings → Control Plane → Integrations**.
- A *View Only* account can read but not change anything. A write fails with this error even when `UNIFI_ALLOW_WRITES=true`.

## Certificate and connection errors

**`Could not reach UniFi controller at https://…: ECONNREFUSED / ETIMEDOUT / ENOTFOUND`**

- Test from the Docker host: `curl -k https://192.168.1.1`. The container uses the Docker host's network path.
- Check the port: UniFi OS uses `443`, legacy controllers usually `8443`, and UniFi OS Server `11443`.
- Hostnames must resolve **inside the container**. If local DNS names don't resolve there, use an IP address.

**`… self-signed certificate … (the controller's certificate isn't trusted …; pin it with UNIFI_TLS_FINGERPRINT …)`**

Certificate verification is on (`UNIFI_VERIFY_SSL=true` or `UNIFI_CA_FILE`), but the controller uses its default self-signed certificate. Turn verification off and pin the certificate instead with `UNIFI_TLS_FINGERPRINT`, as described in [Security: TLS to the controller](security.md#about-tls-to-the-controller). Or install a trusted certificate on the controller. If the error mentions `ALTNAME`, the certificate doesn't cover the host name or IP in `UNIFI_URL`; a pin works around that too.

**`TLS certificate fingerprint mismatch: the controller presented SHA-256 …`**

The controller's certificate isn't the one in `UNIFI_TLS_FINGERPRINT`. That's expected after the console renews or regenerates its certificate, for example after an update or a reset. Check the new fingerprint against the console itself (see the `openssl` command in [Security](security.md#about-tls-to-the-controller)) before you update the variable. If you didn't expect a change, treat it as a possible interception.

## "Redirected" errors

**`… was redirected (HTTP 302) to …; check UNIFI_URL and UNIFI_CONTROLLER_TYPE`**

- `UNIFI_URL` should be the bare console URL, such as `https://192.168.1.1`, without `/network` or `/manage`.
- If the controller type was detected wrongly, for example behind a reverse proxy, set `UNIFI_CONTROLLER_TYPE=unifi-os` or `legacy`.

## "Endpoint not found"

**`… failed with HTTP 404 … (endpoint not found; it may not exist on this Network application version)`**

The feature isn't available on your Network version or hardware, or your site uses the other firewall model. For example, legacy firewall rule tools don't apply after migrating to zone-based firewall, and the other way round. See [Compatibility](compatibility.md).

## Site errors

**`api.err.NoSiteContext` or `api.err.NoPermission` for a site**

Tools need the site's **API name**, not its display name. Ask *"list my UniFi sites"* and use the `name` field (for example `default` or `x7k2pq9d`) for `UNIFI_SITE` or in your questions.

## A change didn't take effect

- The tool returns as soon as the controller accepts a change. Devices then need a few seconds to provision, and clients may reconnect.
- Some field names vary between Network versions. Ask the assistant to fetch the object with `raw: true`, compare it with what you expected, and set the right field via `extra`. See [Compatibility → fields that couldn't be verified](compatibility.md#fields-that-couldnt-be-verified).
- If the controller rejected the change, the tool result contains the controller's message, such as `api.err.InvalidPayload`.

## Tools are missing

Tools are only registered when they're allowed:

- Write tools need `UNIFI_ALLOW_WRITES=true`, and delete tools also need `UNIFI_ALLOW_DELETES=true`.
- Each tool's toolset must be listed in `UNIFI_TOOLSETS`.
- Restart the container after changing `.env` with `docker compose up -d`, and reconnect or restart your MCP client so it fetches the new tool list.
- Version 0.5.0 listed only three meta-tools by default (`UNIFI_TOOL_MODE=lazy`). That mode was removed in 0.6.0, and every enabled tool is listed again. If the server now refuses to start with `UNIFI_TOOL_MODE was removed in 0.6.0`, delete `UNIFI_TOOL_MODE` from your configuration and use `UNIFI_TOOLSETS` to keep the list small (see [Keeping the tool list small](configuration.md#keeping-the-tool-list-small)).

## Slow or timing-out tools

- Some operations are slow by nature: backups, RF scans, large statistics reports and big client lists. Raise `UNIFI_TIMEOUT_MS`, for example to `120000`.
- Behind a reverse proxy, raise the proxy's read timeout too.
- Use `search`, `fields`, `limit` and toolset filtering to keep responses small (see [Narrowing list results](usage.md#narrowing-list-results)).

## Still stuck?

Search the [existing issues](https://github.com/mattoddie/unifi-mcp/issues) or [open a bug report](https://github.com/mattoddie/unifi-mcp/issues/new?template=bug_report.yml). Include your controller model, Network version, unifi-mcp version (the first log line) and the exact error. **Remove any credentials, tokens and public IPs first.**
