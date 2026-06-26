# Configuration reference

## Environment variables

### `UNIFI_HOST`

- **Required:** yes
- **Type:** URL string
- **Example:** `https://192.168.1.1` or `https://unifi.home.arpa`

Base URL of the UniFi Network controller. Include scheme and port if non-standard. Do not include a trailing slash.

```bash
UNIFI_HOST=https://unifi.home.arpa
UNIFI_HOST=https://192.168.1.1:8443   # older controller versions
```

---

### `UNIFI_API_KEY`

- **Required:** yes
- **Type:** string

API key for authenticating with the UniFi controller. Must belong to a local account with the Network Admin role. Generate in UniFi Network → Settings → Admins & Users → (account) → API Keys.

---

### `UNIFI_SITE`

- **Required:** no
- **Default:** `default`
- **Type:** string

UniFi site identifier. For most home setups this is `default`. Find the site name in the controller URL: `https://unifi.home.arpa/network/<site-name>/dashboard`.

---

### `UNIFI_SKIP_TLS_VERIFY`

- **Required:** no
- **Default:** `true`
- **Type:** boolean (`true` / `false`)

When `true`, TLS certificate errors from the UniFi controller are ignored. Appropriate for controllers with self-signed certificates, which is the default for most self-hosted UniFi deployments.

**Interaction with `UNIFI_CA_CERT`:** When `UNIFI_CA_CERT` is set to a valid PEM file path, TLS verification is enabled regardless of this setting. Setting both `UNIFI_CA_CERT` and `UNIFI_SKIP_TLS_VERIFY=true` is a configuration error — bollard will use the CA cert and verify TLS.

```bash
# Skip verification (default, fine for home use)
UNIFI_SKIP_TLS_VERIFY=true

# Strict verification with system trust store
UNIFI_SKIP_TLS_VERIFY=false

# Strict verification with custom CA (UNIFI_SKIP_TLS_VERIFY is ignored)
UNIFI_CA_CERT=/etc/bollard/ca.pem
```

---

### `UNIFI_CA_CERT`

- **Required:** no
- **Default:** unset
- **Type:** filesystem path (string)

Absolute path to a PEM-encoded CA certificate file. Use when the UniFi controller presents a certificate signed by an internal or private CA.

When set, bollard loads this certificate and uses it to verify the controller's TLS certificate. This overrides `UNIFI_SKIP_TLS_VERIFY`.

The path must be accessible inside the container — mount the file via a bind mount or volume.

```bash
UNIFI_CA_CERT=/etc/bollard/ca.pem
```

---

### `DATABASE_URL`

- **Required:** no
- **Default:** `file:bollard.db`
- **Type:** DSN string

Connection string for the state database.

**SQLite formats:**

```bash
# Relative path (not recommended in production — relative to working dir)
DATABASE_URL=file:bollard.db

# Absolute path (recommended)
DATABASE_URL=file:/data/bollard.db

# In-memory (data lost on restart — for testing only)
DATABASE_URL=file::memory:?cache=shared
```

**Postgres format** (post-MVP, interface exists):

```bash
DATABASE_URL=postgres://user:password@host:5432/dbname?sslmode=require
DATABASE_URL=postgres://bollard:secret@postgres:5432/bollard
```

> Postgres backend is not active in current releases. The schema-compatible interface exists for future use. Use SQLite for production deployments.

**Production recommendation:** Use an absolute path inside a named Docker volume:

```yaml
volumes:
  - bollard-data:/data
environment:
  DATABASE_URL: file:/data/bollard.db
```

---

### `RECONCILE_INTERVAL`

- **Required:** no
- **Default:** `5m`
- **Type:** Go duration string

How often the reconcile loop runs. The loop compares running containers against owned records in the state store and cleans up orphans.

Valid units: `s` (seconds), `m` (minutes), `h` (hours).

```bash
RECONCILE_INTERVAL=5m    # default
RECONCILE_INTERVAL=30s   # faster cleanup, more UniFi API calls
RECONCILE_INTERVAL=1h    # slower, lower API traffic
```

**Interaction with missed events:** If a container exits without emitting a stop event (host crash, OOM kill, Docker daemon restart), the orphaned DNS record will persist until the next reconcile tick. With the default 5m interval, orphan records are cleaned up within 5 minutes of the next bollard startup.

Minimum effective value is approximately `10s` — below this the reconcile loop adds no meaningful benefit over event-driven updates.

---

### `LOG_FORMAT`

- **Required:** no
- **Default:** `logfmt`
- **Valid values:** `logfmt`, `json`

Log output format.

`logfmt` is human-readable:
```
level=info msg="dns record created" hostname=myapp.home.arpa ip=192.168.1.100
```

`json` is machine-readable for log aggregation pipelines:
```json
{"level":"info","msg":"dns record created","hostname":"myapp.home.arpa","ip":"192.168.1.100"}
```

---

### `LOG_LEVEL`

- **Required:** no
- **Default:** `info`
- **Valid values:** `debug`, `info`, `warn`, `error`

Minimum log level to emit. `debug` includes detailed reconcile loop output and UniFi API call traces.

---

## Container labels

Labels are set on individual containers to control DNS registration.

### `dns.bollard/hostname`

- **Required:** yes (to opt in)
- **Type:** FQDN string

The fully qualified domain name to register. This label opts the container into DNS management. Without it, bollard ignores the container entirely.

**Valid:** any syntactically valid FQDN with at least two labels.

```yaml
labels:
  dns.bollard/hostname: myapp.home.arpa
  dns.bollard/hostname: service.internal.example.com
```

**Invalid:**
- Single-label names: `myapp` (no dot)
- Trailing dots: `myapp.home.arpa.`
- Empty string

If the hostname is invalid, bollard logs an error and skips the container.

---

### `dns.bollard/record-type`

- **Required:** no
- **Default:** `A`
- **Valid values:** `A`

Record type to create. Only `A` records are supported in the current release. Setting any other value is logged as an error and the container is skipped.

---

### `dns.bollard/ttl`

- **Required:** no
- **Default:** `300`
- **Type:** integer (seconds)
- **Valid range:** 1–2147483647

TTL in seconds for the DNS record.

```yaml
labels:
  dns.bollard/ttl: "60"     # short TTL for frequently-changed services
  dns.bollard/ttl: "3600"   # 1 hour
```

**Invalid values:** non-integer strings, zero, negative numbers. bollard logs an error and falls back to the default `300`.

---

### `dns.bollard/ip-override`

- **Required:** no
- **Default:** inferred NAS host IP

Override the IP address used for the A record. By default bollard uses the host machine's primary IP (appropriate for host-networking containers on a NAS). Use this label when the container is reachable at a different IP — for example, on a macvlan network or when using a dedicated interface.

```yaml
labels:
  dns.bollard/ip-override: "192.168.10.50"
```

Must be a valid IPv4 address. Invalid values are logged as an error and the container is skipped.

---

### `dns.bollard/enabled`

- **Required:** no
- **Default:** `true`
- **Valid values:** `true`, `false`

Set to `false` to suppress DNS management for a container without removing the other labels. Useful for temporarily disabling registration during maintenance.

```yaml
labels:
  dns.bollard/hostname: myapp.home.arpa
  dns.bollard/enabled: "false"
```

When `enabled` transitions from `false` to `true` (container restart), bollard creates the record. When it transitions from `true` to `false` (container restart with updated label), any existing record is deleted.
