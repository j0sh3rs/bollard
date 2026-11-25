# Configuration reference

Reference for the **standalone Docker daemon**: environment variables and container labels. Values are read from the code on `main`. The Traefik plugin has its own settings, see [Traefik provider plugin](traefik-plugin.md).

Invalid configuration exits non-zero at startup.

## Environment variables

| Variable | Required | Default | Validation |
|---|---|---|---|
| `UNIFI_HOST` | yes | — | non-empty |
| `UNIFI_API_KEY` | yes | — | non-empty |
| `UNIFI_SITE` | no | `default` | must match a site on the controller |
| `UNIFI_SKIP_TLS_VERIFY` | no | `true` | boolean |
| `UNIFI_CA_CERT` | no | unset | readable PEM file with at least one certificate |
| `DATABASE_URL` | no | `file:bollard.db` | `postgres://`/`postgresql://` selects Postgres, anything else SQLite |
| `RECONCILE_INTERVAL` | no | `5m` | Go duration, at least `1s` |
| `LOG_FORMAT` | no | `logfmt` | `logfmt` or `json` |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `METRICS_ADDR` | no | `:9090` | listen address |

### `UNIFI_HOST`

Base URL of the UniFi Network controller, with scheme and port if non-standard, no trailing slash. bollard appends `/proxy/network/...` paths.

```bash
UNIFI_HOST=https://unifi.home.arpa
UNIFI_HOST=https://192.168.1.1:8443
```

### `UNIFI_API_KEY`

UniFi Network API key, sent as `X-API-KEY`. Create it under the controller's admin/API settings for a dedicated local account. Never log or commit it; the startup log omits it.

### `UNIFI_SITE`

Site selector. Matched against the controller's site **name**, **ID**, or **internal reference** (since v0.7.1). No match is a fatal startup error (`site "..." not found`).

### `UNIFI_SKIP_TLS_VERIFY`

Default **`true`**: TLS certificate errors from the controller are ignored. That default exists so self-signed controllers work out of the box, but it lets anyone on the path impersonate the controller and read the API key. Set `false` for anything beyond a trusted home LAN, and use `UNIFI_CA_CERT` for a private CA.

```bash
UNIFI_SKIP_TLS_VERIFY=false                 # verify with the system trust store
UNIFI_CA_CERT=/etc/bollard/ca.pem           # verify with a private CA
```

### `UNIFI_CA_CERT`

Path, inside the container, to a PEM file. When set, bollard verifies the controller against **only** that pool, and `UNIFI_SKIP_TLS_VERIFY` is ignored without a warning. An unreadable file or a file without a valid certificate is a fatal startup error. Mount the file read-only; see [`examples/custom-ca.yml`](examples/custom-ca.yml).

### `DATABASE_URL`

State store DSN. Both backends are implemented and use embedded migrations.

```bash
# SQLite (default). Use an absolute path on a persistent volume.
DATABASE_URL=file:/data/bollard.db

# Postgres
DATABASE_URL=postgres://bollard:secret@postgres:5432/bollard?sslmode=require
```

- The default `file:bollard.db` is relative to the working directory. The image is `FROM scratch` with no volume, so that file is lost when the container is recreated. Always set an absolute path on a volume (as in the examples) unless you accept losing ownership state.
- `file::memory:?cache=shared` keeps state in memory only; use it for testing.
- For Postgres, prefer `sslmode=require` or stricter unless the database is on the same trusted host. The startup log masks the password, including `password` and `sslpassword` query parameters.
- Switching backends does not copy data. Follow [Migrate the state store](operations.md#switch-the-state-store).
- Run one bollard per store and per UniFi site.

### `RECONCILE_INTERVAL`

Interval between reconcile ticks (Go duration such as `30s`, `5m`, `1h`), minimum `1s`. The first tick runs one interval after startup. Each tick lists all UniFi records, so very short intervals increase controller load. See [Architecture](architecture.md#periodic-reconcile) for what a tick does and does not do.

### `LOG_FORMAT` / `LOG_LEVEL`

`logfmt` (human-oriented coloured text from the `tint` handler, not strict logfmt; use `json` for log pipelines) or `json`; `debug`, `info`, `warn`, `error`. Messages emitted today include `bollard starting`, `configuration`, `state store ready`, `bollard started`, `created DNS record`, `deleted DNS record`, plus warnings and errors for failed operations. The `debug` level currently adds little beyond `info`.

### `METRICS_ADDR`

Listen address for `/metrics` and `/healthz`. Neither endpoint is authenticated, and the default `:9090` listens on all interfaces (every interface when using `network_mode: host`). Bind to loopback or restrict with a firewall.

The image runs `/bollard -healthcheck` against the configured host and port. For example, `METRICS_ADDR=127.0.0.1:9090` restricts access to IPv4 loopback and keeps the healthcheck working. Use `[::1]:9090` for IPv6 loopback. Wildcard binds are probed over loopback.

## Command-line flags

| Flag | Meaning |
|---|---|
| `-adopt` / `--adopt` | Run the adopt phase before normal operation; see [Operations](operations.md#adopt-existing-records) |
| `-healthcheck` | Request `/healthz` and exit 0 or 1; used by the image healthcheck |
| `-version` | Print the installed build version and exit |

## Container labels

| Label | Required | Default | Accepted values |
|---|---|---|---|
| `dns.bollard/hostname` | yes (opt-in) | — | valid DNS hostname |
| `dns.bollard/record-type` | no | `A` | `A` only |
| `dns.bollard/ttl` | no | `300` | integer ≥ 0 |
| `dns.bollard/ip-override` | no | inferred host IP | IPv4 address, or empty |
| `dns.bollard/enabled` | no | `true` | `true`/`1`/`yes`, `false`/`0`/`no` |

A container with an invalid label is **skipped** with a warning (`invalid labels on container`). Nothing falls back to a default except an absent label.

### `dns.bollard/hostname`

Hostname to register, such as `myapp.home.arpa`. A missing or empty label means the container is not managed. Names are folded to lowercase and one trailing dot is removed. Use ASCII letters, digits, and interior hyphens, with at most 63 bytes per label and 253 bytes total. IP literals are not hostnames. A name already present in the store or UniFi blocks creation.

### `dns.bollard/record-type`

Only `A`. Any other value skips the container.

### `dns.bollard/ttl`

TTL in seconds. Non-integer or negative values skip the container (they do **not** fall back to 300). `0` is accepted and passed to UniFi as is; whether UniFi honours it is a controller matter.

### `dns.bollard/ip-override`

IPv4 address for the A record. IPv6 or malformed values skip the container. When absent, the host IP is inferred from host interfaces and must be unambiguous; see [IP resolution](architecture.md#ip-resolution). Use the label for macvlan containers, multi-NIC hosts, or a proxy IP; see [`examples/ip-override.yml`](examples/ip-override.yml).

### `dns.bollard/enabled`

`false`, `0`, or `no` disables management for the container; anything outside the accepted list is an invalid label. Labels are read at container start. Disabling a running container requires recreating it; the record is deleted when the old container stops.
