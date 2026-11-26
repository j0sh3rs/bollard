# Getting started

Tutorial: run the **standalone bollard daemon** on a Docker host (written for a Synology NAS), register one container in UniFi DNS, and confirm it. About 15 minutes.

Not sure this is the right mode? bollard has two:

| | Standalone daemon (this tutorial) | [Traefik provider plugin](traefik-plugin.md) |
|---|---|---|
| Use when | You label plain Docker containers | You already run Traefik and want DNS from router `Host()` rules |
| Needs Docker socket | yes, read-only | no |
| Keeps state | yes (SQLite/Postgres) | no |
| Deletes DNS records | yes, ones it owns | only ones it created, at shutdown |

## Prerequisites

- Docker Engine with Compose on the host.
- A UniFi Network controller reachable from the host, with API-key support.
- A UniFi account that may create API keys and manage DNS.
- The host has **one** routable IPv4 address on a non-Docker interface. If it has several (multiple NICs, VLANs), you will need `dns.bollard/ip-override` in step 4; see [IP resolution](architecture.md#ip-resolution).

## 1. Create a UniFi API key

Use a dedicated local account rather than your primary or SSO admin, so a leaked key has limited reach.

1. In UniFi Network, open **Settings → Admins & Users** and add a local-access admin (for example `bollard`) with a role that can manage DNS.
2. Open that account and generate an API key. It is shown once; copy it.

## 2. Create the `.env` file

```bash
# /volume1/docker/bollard/.env
UNIFI_HOST=https://unifi.home.arpa
UNIFI_API_KEY=your-api-key-here
DATABASE_URL=file:/data/bollard.db
```

```bash
chmod 600 /volume1/docker/bollard/.env
```

`UNIFI_SKIP_TLS_VERIFY` defaults to **`true`**, which disables certificate checks. That is convenient for the first run on a self-signed controller but exposes the API key to anyone who can intercept the connection. Once it works, switch to verification: set `UNIFI_SKIP_TLS_VERIFY=false`, or mount your CA and set `UNIFI_CA_CERT` (see [`examples/custom-ca.yml`](examples/custom-ca.yml)).

If your site is not the default, set `UNIFI_SITE` to its display name or UUID. All settings: [Configuration reference](configuration.md).

## 3. Start bollard

```yaml
# /volume1/docker/bollard/docker-compose.yml
services:
  bollard:
    image: ghcr.io/j0sh3rs/bollard:latest
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - bollard-data:/data
    env_file: .env

volumes:
  bollard-data:
```

Pin a version tag instead of `latest` once you are happy.

```bash
cd /volume1/docker/bollard
docker compose up -d
docker compose logs bollard
```

Expected log lines (the `logfmt` format is a colourised text format; timestamps, levels, and fields abridged here):

```
INF msg="bollard starting" version=... go=... os=linux arch=...
INF msg=configuration unifi_host=https://unifi.home.arpa unifi_site=default unifi_tls_verify=false database=file:/data/bollard.db ...
INF msg="state store ready" owned_records=0
INF msg="bollard started" reconcile_interval=5m0s
```

`unifi_tls_verify=false` here means verification is off (the default). If bollard exits instead, see [Troubleshooting](#troubleshooting).

## 4. Label a container

```yaml
services:
  myapp:
    image: nginx:alpine
    labels:
      dns.bollard/hostname: myapp.home.arpa
```

Labels are read when a container starts, so recreate it:

```bash
docker compose up -d --force-recreate myapp
```

If the host has several IPv4 addresses, add `dns.bollard/ip-override: "192.168.1.100"` (your host IP).

## 5. Verify

Logs should show:

```
INF msg="created DNS record" hostname=myapp.home.arpa ip=192.168.1.100 container=myapp(0123456789ab)
```

Then check UniFi (**Settings → DNS**, or Policy Table depending on version) for an A record, and resolve it:

```bash
dig myapp.home.arpa @192.168.1.1
```

Stop the container and the log shows `deleted DNS record`; the record leaves UniFi.

## Troubleshooting

bollard exits non-zero on configuration, store, permanent UniFi (credentials, certificate, site), or Docker socket errors; `restart: unless-stopped` retries. If the controller is only unreachable, bollard logs `UniFi unavailable; retrying` and keeps trying without exiting.

| Symptom | Likely cause | Fix |
|---|---|---|
| `config error` mentioning `UNIFI_HOST`/`UNIFI_API_KEY` | Variable missing | Check `.env` is loaded (`env_file`) |
| `site "..." not found` | `UNIFI_SITE` does not match a site name or ID | Use the display name or UUID |
| `site probe failed (status=401...` | Bad API key or insufficient rights | Regenerate the key |
| TLS error mentioning unknown authority | `UNIFI_SKIP_TLS_VERIFY=false` without a trusted CA | Set `UNIFI_CA_CERT` |
| `state store unavailable` | `DATABASE_URL` path not writable or database unreachable | Check the volume mount |
| `docker socket unavailable` | Socket not mounted or permission denied | Check the mount and socket permissions |
| `invalid labels on container` | Bad TTL, record type, IP, or enabled value | See [labels](configuration.md#container-labels) |
| `multiple routable IPv4 addresses found` | Ambiguous host IP | Set `dns.bollard/ip-override` |
| `hostname ... already owned by container` | Two containers claim one hostname | Make hostnames unique |
| `UniFi unavailable; retrying` repeats | Controller unreachable, wrong `UNIFI_HOST`, or a blocked port | Not fatal. Check the URL and network path from the host |
| `metrics listen failed` | `METRICS_ADDR` is already in use or not bindable | Pick another port or address |
| `response has no data` or `accepted but not completed` | UniFi answered unexpectedly | See [UniFi error messages](operations.md#unifi-error-messages) |

Set `LOG_LEVEL=debug` for extra detail. For day-two tasks, see [Operations](operations.md).
