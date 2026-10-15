# Deployment

- [Docker Compose](#docker-compose)
- [Image tags](#image-tags)
- [Upgrading](#upgrading)
- [Building from source](#building-from-source)
- [TLS with a reverse proxy](#tls-with-a-reverse-proxy)
- [Hardening](#hardening)
- [Running without Docker](#running-without-docker)

## Docker Compose

The repository's [`compose.yaml`](../compose.yaml) is a good starting point:

```yaml
services:
  unifi-mcp:
    image: ghcr.io/mattoddie/unifi-mcp:latest
    container_name: unifi-mcp
    restart: unless-stopped
    env_file: .env
    ports:
      - "8080:8080"
```

```sh
docker compose up -d          # start
docker compose logs -f        # follow logs
docker compose ps             # shows "healthy" once the health check passes
docker compose down           # stop
```

The image has a built-in health check that calls `/health` every 30 seconds.

To listen on one interface only, such as a LAN address or localhost when you use a reverse proxy, change the port mapping to `"192.168.1.10:8080:8080"` or `"127.0.0.1:8080:8080"`.

## Image tags

Images are published to the GitHub Container Registry at **`ghcr.io/mattoddie/unifi-mcp`** for `linux/amd64` and `linux/arm64`. Each [GitHub release](https://github.com/mattoddie/unifi-mcp/releases) publishes these tags:

| Tag | Example | Moves? |
|---|---|---|
| `X.Y.Z` | `1.4.2` | Never. Pin this for full reproducibility. |
| `X.Y` | `1.4` | Moves to the latest patch release (bug fixes). |
| `X` | `1` | Moves to the latest minor release (new features, no breaking changes). Not published for `0.x`. |
| `latest` | | Moves to the newest stable release. |

Pre-releases, such as `1.5.0-rc.1`, are only published under their exact version, never as `latest`.

Every image includes an SBOM and build provenance attestation. To verify that an image was built by this repository's release workflow:

```sh
gh attestation verify oci://ghcr.io/mattoddie/unifi-mcp:1.4.2 --owner mattoddie
```

## Upgrading

```sh
docker compose pull
docker compose up -d
```

Check the [changelog](../CHANGELOG.md) or the release notes before upgrading across a major version.

## Building from source

```sh
git clone https://github.com/mattoddie/unifi-mcp.git
cd unifi-mcp
docker build -t unifi-mcp .
```

The tests run as part of the image build, so a broken build never produces an image. To use a local build with Compose, replace `image:` in `compose.yaml` with `build: .` and run `docker compose up -d --build`.

## TLS with a reverse proxy

The server speaks plain HTTP. If it's reachable from outside a trusted network, put a TLS-terminating reverse proxy in front of it and keep `MCP_AUTH_TOKEN` set.

**Caddy**, which gets certificates automatically:

```caddyfile
unifi-mcp.example.com {
    reverse_proxy unifi-mcp:8080
}
```

**nginx:**

```nginx
server {
    listen 443 ssl;
    server_name unifi-mcp.example.com;
    ssl_certificate     /etc/ssl/certs/unifi-mcp.pem;
    ssl_certificate_key /etc/ssl/private/unifi-mcp.key;

    location /mcp {
        proxy_pass http://unifi-mcp:8080;
        proxy_set_header Host $host;
        proxy_read_timeout 120s;   # some tools (backups, RF scans) take a while
    }
}
```

**Traefik** labels:

```yaml
services:
  unifi-mcp:
    image: ghcr.io/mattoddie/unifi-mcp:latest
    env_file: .env
    labels:
      - traefik.enable=true
      - traefik.http.routers.unifi-mcp.rule=Host(`unifi-mcp.example.com`)
      - traefik.http.routers.unifi-mcp.tls.certresolver=letsencrypt
      - traefik.http.services.unifi-mcp.loadbalancer.server.port=8080
```

When the server sits behind a proxy, don't publish its port. Set `MCP_ALLOWED_HOSTS=unifi-mcp.example.com` so it only accepts requests for that hostname.

> [!WARNING]
> Don't expose unifi-mcp directly to the internet. Anyone with the token can control your network with the configured account's permissions. For remote access, a VPN (WireGuard, Tailscale, UniFi Teleport) is safer than a public endpoint.

## Hardening

The image already runs as the unprivileged `node` user and doesn't need a writable filesystem. You can lock it down further:

```yaml
services:
  unifi-mcp:
    image: ghcr.io/mattoddie/unifi-mcp:1.4.2
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    mem_limit: 256m
    env_file: .env
    ports: ["127.0.0.1:8080:8080"]
```

See [Security](security.md) for choosing permissions and toolsets.

## Running without Docker

Docker is the supported way to run unifi-mcp, but the server is an ordinary Node.js 22+ program:

```sh
git clone https://github.com/mattoddie/unifi-mcp.git
cd unifi-mcp
npm ci && npm run build
UNIFI_URL=https://192.168.1.1 UNIFI_API_KEY=... node dist/index.js   # stdio by default
MCP_TRANSPORT=http MCP_AUTH_TOKEN=... UNIFI_URL=... UNIFI_API_KEY=... node dist/index.js
```

In HTTP mode, `MCP_AUTH_TOKEN` is required, and outside Docker the server listens on `127.0.0.1` only. Set `MCP_HTTP_HOST=0.0.0.0` to accept connections from other machines.
