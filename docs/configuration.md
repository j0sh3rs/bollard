# Configuration reference

Reference for the **standalone Docker daemon**: environment variables and container labels. Values are read from the code on `main`. The Traefik plugin has its own settings, see [Traefik provider plugin](traefik-plugin.md).

Invalid configuration exits non-zero at startup.

## Environment variables

| Variable | Required | Default | Validation |
|---|---|---|---|
| `UNIFI_HOST` | yes | — | non-empty |
| `UNIFI_API_KEY` | one of `UNIFI_API_KEY`/`UNIFI_API_KEY_FILE` | — | non-empty, single line |
| `UNIFI_SITE` | no | `default` | must match a site on the controller |
| `UNIFI_SKIP_TLS_VERIFY` | no | `true` | boolean |
| `UNIFI_CA_CERT` | no | unset | readable PEM file with at least one certificate |
| `DATABASE_URL` | no | `file:bollard.db` | `postgres://`/`postgresql://` selects Postgres, anything else SQLite |
| `RECONCILE_INTERVAL` | no | `5m` | Go duration, at least `1s` |
| `LOG_FORMAT` | no | `logfmt` | `logfmt` or `json` |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `UNIFI_API_KEY_FILE` | one of the two | — | readable file, single line |
| `BOLLARD_INSTANCE_TAG` | no | empty | up to 64 of `A-Z a-z 0-9 . _ -` |
| `METRICS_ADDR` | no | `127.0.0.1:9090` | listen address |
| `DRY_RUN` | no | `false` | boolean |
| `STOP_DRAIN` | no | `0s` | `0`, or a Go duration of at least `1s` |
| `INFER_TRAEFIK_HOSTS` | no | `false` | boolean |

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

Default **`true`**: TLS certificate errors from the controller are ignored. That default exists so self-signed controllers work out of the box, but it lets anyone on the path impersonate the controller and read the API key. bollard logs a startup warning while this is in effect without `UNIFI_CA_CERT`. Set `false` for anything beyond a trusted home LAN, and use `UNIFI_CA_CERT` for a private CA.

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

Listen address for `/metrics` and `/healthz`. Neither endpoint is authenticated, so the default is the **loopback** address `127.0.0.1:9090`: only processes in the container's own network namespace can reach it (the image healthcheck does). To let Prometheus scrape bollard, set a reachable address such as `METRICS_ADDR=:9090` and restrict access with a firewall, a private Docker network or a reverse proxy; see [Monitoring](monitoring.md#scraping). A wildcard bind listens on every interface (every interface when using `network_mode: host`).

The image runs `/bollard -healthcheck` against the configured host and port, so any bind keeps the healthcheck working. `METRICS_ADDR=[::1]:9090` selects IPv6 loopback. Wildcard binds are probed over loopback.

### `UNIFI_API_KEY_FILE`

Path to a file holding the API key, for Docker secrets (`/run/secrets/unifi_api_key`) or any mounted file. Surrounding whitespace is trimmed and the key must be one line. Set exactly one of `UNIFI_API_KEY` and `UNIFI_API_KEY_FILE`; setting both, neither, or an unreadable file is a fatal startup error. The key never appears in the process environment when only the file is used.

### `BOLLARD_INSTANCE_TAG`

Scopes this instance's ownership rows. Every store query filters on the tag, so several bollard instances can share one database, and one UniFi site, without seeing or deleting each other's records. Up to 64 characters from `A-Z a-z 0-9 . _ -`; default empty. Existing rows belong to the empty tag, so upgrading needs no change. Two instances must still not manage the same hostnames: tags separate ownership, they do not arbitrate names (the second instance sees the first one's record as an occupied name and refuses to create it).

### `DRY_RUN`

Boolean, default `false`. With `true`, bollard reads UniFi and the store as usual but never changes them. Each create, update and delete is logged as `dry-run: would create record` (or update, delete) and applied to an in-memory overlay, so later ticks see the intended state and do not repeat the same line. Ownership rows are not written. Use it to check labels before pointing bollard at a real zone. `-adopt` still reads but does not write.

### `STOP_DRAIN`

Go duration, default `0s` (delete on stop). Above zero, stopping a container keeps its records for that long with their TTL lowered to at most 10 seconds, and deletes them when the period ends; see [Architecture](architecture.md#stop-drain). If a container asks for the same hostname meanwhile (a restart, a recreate), it takes the record over: UniFi sees an update, not a delete and create, so name resolution has no gap. Must be at least `1s` when set. Needs UniFi record updates, which both the modern and legacy APIs provide.

### `INFER_TRAEFIK_HOSTS`

Boolean, default `false`. Enables hostname inference from Traefik router rules; see [`dns.bollard/hostname`](#dnsbollardhostname).

## Command-line flags

| Flag | Meaning |
|---|---|
| `-adopt` / `--adopt` | Run the adopt phase before normal operation; see [Operations](operations.md#adopt-existing-records) |
| `-healthcheck` | Request `/healthz` and exit 0 or 1; used by the image healthcheck |
| `-version` | Print the installed build version and exit |

## Container labels

| Label | Required | Default | Accepted values |
|---|---|---|---|
| `dns.bollard/hostname` | yes (opt-in)¹ | — | one valid DNS hostname, or a comma-separated list |
| `dns.bollard/record-type` | no | `A` | `A`, `AAAA`, `CNAME` |
| `dns.bollard/target` | for `CNAME` | — | valid DNS hostname |
| `dns.bollard/ttl` | no | `300` | integer ≥ 0 |
| `dns.bollard/ip-override` | no | inferred | IPv4 (`A`) or IPv6 (`AAAA`) address, or empty |
| `dns.bollard/network` | no | inferred | name of a Docker network the container is on |
| `dns.bollard/enabled` | no | `true` | `true`/`1`/`yes`, `false`/`0`/`no` |

¹ With `INFER_TRAEFIK_HOSTS=true`, a container without this label gets its hostnames from its Traefik router rules; see [`dns.bollard/hostname`](#dnsbollardhostname).

A container with an invalid label is **skipped** with a warning (`invalid labels on container`). Nothing falls back to a default except an absent label.

### `dns.bollard/hostname`

Hostname to register, such as `myapp.home.arpa`, or several separated by commas (`app.home.arpa, www.home.arpa`, at most 32). Every hostname gets its own record, sharing the record type, TTL, address and target labels, and its own ownership row. A missing or empty label means the container is not managed. Names are folded to lowercase and one trailing dot is removed. Use ASCII letters, digits, and interior hyphens, with at most 63 bytes per label and 253 bytes total. IP literals are not hostnames, and a duplicate name in the list is an invalid label. A name already present in the store or UniFi blocks creation of that name only; the container's other names are still created and the conflict is reported. The daemon also refuses reserved names, whether or not a record exists. They are derived once at startup from `UNIFI_HOST`, `DATABASE_URL` and `DOCKER_HOST`, plus a fixed list of Docker host aliases and `localhost`. Matching is exact, not zone-wide. See [SECURITY.md](../SECURITY.md#defaults-to-review).

**Hostname inference.** With `INFER_TRAEFIK_HOSTS=true`, a container that has **no** `dns.bollard/hostname` label gets one record for every name in its positive ``Host(`...`)`` matchers on `traefik.http.routers.<router>.rule` labels, parsed with the same rules as the [Traefik plugin](traefik/reference.md#rule-parsing). A container with `traefik.enable=false` gets none. An explicit `dns.bollard/hostname` always wins. The other labels (`record-type`, `ttl`, `ip-override`, `network`, `enabled`) apply to inferred names too.

### `dns.bollard/record-type`

`A` (default), `AAAA` or `CNAME`; any other value skips the container. A mixed set is not possible on one container: the type applies to every hostname in the label. Use two containers, or a sidecar, to publish both `A` and `AAAA` for one name set.

### `dns.bollard/target`

Required for `CNAME` and rejected on other types. A valid DNS hostname, folded like `hostname`. A hostname cannot point at itself. The target does not have to exist in UniFi.

### `dns.bollard/ttl`

TTL in seconds. Non-integer or negative values skip the container (they do **not** fall back to 300). `0` is accepted and passed to UniFi as is; whether UniFi honours it is a controller matter.

### `dns.bollard/ip-override`

Address for the record: an IPv4 address for `A`, an IPv6 address for `AAAA` (link-local and loopback are rejected). Rejected on `CNAME` and together with `dns.bollard/network`. Malformed values skip the container. Without it, the address is chosen as described in [IP resolution](architecture.md#ip-resolution). Use the label for multi-NIC hosts or a proxy IP; see [`examples/ip-override.yml`](examples/ip-override.yml).

### `dns.bollard/network`

Name of a Docker network the container is attached to. The record publishes the container's own address on that network (IPv4 for `A`, global IPv6 for `AAAA`) instead of the host's. It is an error, and nothing is created, when the container is not on that network or has no address of the needed family there. Rejected on `CNAME` and together with `ip-override`.

### `dns.bollard/enabled`

`false`, `0`, or `no` disables management for the container; anything outside the accepted list is an invalid label. Labels are read at container start. Disabling a running container requires recreating it; the record is deleted when the old container stops.
