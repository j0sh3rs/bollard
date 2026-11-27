# Reference: Traefik provider plugin

Exact behavior of the plugin, taken from `traefik/provider.go`, `internal/dnsname/rules.go`, and `internal/unifi`. For recipes see the [how-to guides](how-to.md); for rationale see the [explanation](explanation.md).

## Release differences

| Behavior | v0.7.0 | v0.7.1 | Current source |
|---|---|---|---|
| `unifiSite` match (modern API) | Display name or UUID | Also matches internal references such as `default` | Same as v0.7.1 |
| Existing-record check | Enabled A records only | All named records | All named records |
| Write coordination | None | None | Serialized within a shared plugin runtime for the same controller hostname; not across aliases or replicas |
| Shutdown | Leaves all records | Leaves all records | Deletes the records this instance created; failures are logged and do not stop the rest. Concurrent `Stop` calls clean up once |
| Failed create | Not tracked | Not tracked | After an ambiguous failure, tracks the one matching record so shutdown deletes it; never after a 4xx |
| Reserved names | Not checked | Not checked | Skips the UniFi host, the Traefik API host, `localhost` and Docker host aliases |

Release tags do not guarantee catalog availability. Use the [local plugin overlay](how-to.md#develop-with-a-local-plugin) if the version you need is not served.

## Registration

The module is `github.com/j0sh3rs/bollard`; the root `.traefik.yml` selects the `traefik` package (`type: provider`, `import: github.com/j0sh3rs/bollard/traefik`).

<!-- x-release-please-start-version -->
```yaml
experimental:
  plugins:               # or localPlugins (no version) for a local checkout
    bollard:
      moduleName: github.com/j0sh3rs/bollard
      version: "v0.8.0"
providers:
  plugin:
    bollard:             # must match the registration name
      # fields below
```
<!-- x-release-please-end -->

## Configuration fields

All fields sit under `providers.plugin.<name>` in **static** configuration.

| Field | Default | Required | Notes |
|---|---|---|---|
| `traefikAPI` | `http://127.0.0.1:8080` | no | Base URL; `/api/http/routers` is appended. |
| `unifiHost` | none | yes | UniFi controller base URL. |
| `unifiAPIKeyFile` | none | one of | File containing the API key. Recommended. |
| `unifiAPIKey` | none | one of | Inline API key. Exactly one key source. |
| `unifiSite` | `default` | no | Site name, UUID, or internal reference (since v0.7.1). |
| `unifiCACert` | none (empty = unset) | no | PEM CA file path inside the Traefik container. |
| `unifiSkipTLSVerify` | `false` | no | Disables TLS verification. Ignored when `unifiCACert` is set. |
| `address` | none | yes | IPv4 address written to every created record. |
| `pollInterval` | `30s` | no | Time between discovery polls. |

An explicitly empty `traefikAPI`, `unifiHost`, `address`, or `pollInterval` replaces the default and fails validation. An empty `unifiSite` becomes `default`.

### Validation

`New` validates in this order and fails Traefik's plugin initialization on the first error. It performs no network I/O and does not read the CA file.

| Check | Error |
|---|---|
| Config present | `bollard: configuration is required` |
| `traefikAPI` and `unifiHost`: scheme `http` or `https`, nonempty hostname, no credentials, query, or fragment. A path is allowed; trailing `/` is trimmed. | `bollard: invalid traefikAPI: ...` / `invalid unifiHost: ...` |
| `address`: parses as IP, has an IPv4 form, contains no `:` (IPv6 and IPv4-mapped IPv6 rejected) | `bollard: address must be an IPv4 address` |
| `pollInterval`: Go duration, greater than zero (`30s`, `1m`; bare `30` fails) | `bollard: pollInterval must be a positive duration` |
| `unifiSite`: not `.` or `..`, and none of `/ \ ? # %` | `bollard: unifiSite must be a site name or ID` |
| Key sources: not both set | `bollard: set only one of unifiAPIKey and unifiAPIKeyFile` |
| `unifiAPIKeyFile` readable; contents are whitespace-trimmed | `bollard: read unifiAPIKeyFile: ...` |
| Key nonempty after trimming and contains no CR or LF | `bollard: a nonempty UniFi API key is required` |

A trailing newline in a key file is trimmed. An inline key is not trimmed, so leading or trailing whitespace there is kept (only CR/LF are rejected).

### Deferred validation

These checks run in the worker, not in `New`:

- **CA certificate.** `unifiCACert` is read and parsed when the UniFi client is first built, which happens only after discovery finds at least one hostname. Failures: `unifi: read CA cert: ...` and `unifi: no valid certs in <path>`. They are logged each poll and retried; Traefik keeps running.
- **Site resolution, TLS, credentials.** Same first-use timing. A successfully built client is cached for the life of the plugin instance, including the resolved site. Changing sites or CAs needs a Traefik restart.
- When `unifiCACert` is set, the CA pool replaces system roots. With it unset, system roots apply (or no verification if `unifiSkipTLSVerify: true`). Minimum TLS version is 1.2.

## Discovery

The worker calls `GET <traefikAPI>/api/http/routers?page=N&per_page=100`, following `X-Next-Page` until it is empty, `0`, or `1`. A non-increasing or non-numeric next page, or an empty page that claims another, is an error (`invalid Traefik API pagination`).

Request limits: 15 s timeout, redirects not followed (any 3xx is an error), 4 MiB per page, HTTP 200 required. TCP and UDP routers are never read.

A router contributes names only if **all** hold:

- `status` is `enabled`;
- `provider` is not `internal` and the name does not end in `@internal`;
- its rule parses as a boolean expression (see below).

Entrypoints, TLS, middlewares, and services are not considered. Any provider except internal counts (`file`, `docker`, and others).

### Rule parsing

Rules are parsed with Go's expression parser, which accepts `&&`, `||`, `!`, and parentheses. Only calls named exactly `Host` are read; every other matcher and its arguments are ignored. Argument literals may use backticks or double quotes.

| Rule | Result |
|---|---|
| ``Host(`App.Home.Arpa.`)`` | `app.home.arpa` (lowercased, one trailing dot removed) |
| ``Host(`a.home.arpa`) && PathPrefix(`/x`)`` | `a.home.arpa` |
| ``Host(`a.home.arpa`) \|\| Host(`b.home.arpa`)`` | both |
| ``Host(`a.home.arpa`, `b.home.arpa`)`` | both (legacy multi-argument form) |
| ``!Host(`x.home.arpa`)`` / ``!(Host(`x`) \|\| Path(`/`))`` | none (negative) |
| ``!!Host(`x.home.arpa`)`` | `x.home.arpa` (double negation is positive) |
| ``HostRegexp(`...`)``, `HostSNI`, wildcards, IP literals | none |
| Rule the parser rejects | none, silently |

A name is valid when it is at most 253 characters, not an IP, and every dot-separated label is 1–63 characters of `a-z`, `0-9`, or `-`, not starting or ending with `-`. Underscores, `*`, and `/` are invalid. Names are deduplicated across routers and pages.

## Reconciliation

The worker sends an empty configuration `{}` to Traefik once (the plugin supplies no routes), then loops:

1. Discover hostnames. On error, log `DNS discovery failed: ...` and wait.
2. Drop reserved names: the `unifiHost` host, the `traefikAPI` host, `localhost`, and Docker's host aliases (`host.docker.internal` and the legacy `docker.for.*` names). Each is logged as `hostname <host> is reserved, not creating it`. If no hostnames remain, stop the iteration. UniFi is **not contacted**, so UniFi, CA, and site errors stay invisible until a route exists.
3. Build the UniFi client if not cached (see [deferred validation](#deferred-validation)).
4. List UniFi records. For each record, compare `hostname` lowercased with one trailing dot removed. A discovered name that matches is removed from the work list; if the record's IP differs from `address`, log `DNS conflict for <host>: existing record preserved`. (This prints every poll while the conflict stands.)
5. For each remaining name in alphabetical order, create one A record. A failed create is logged as `create DNS record for <host>: ...` and the loop continues; later polls retry. Success logs `created A record <host> -> <address>`. After a failure that is not a 4xx answer (timeout, lost response, 5xx, no ID), the plugin lists once more. If exactly one A record has that name and `address`, it is tracked so shutdown deletes it, and the plugin logs `recovered A record <host> after a failed create`. One failed lookup disables further lookups for that poll. A 4xx (for example 409) means another writer's record won, so nothing is tracked.
6. Wait `pollInterval`, or exit on shutdown.

Polls on one instance never overlap.

### Record shape

Every create sends TTL **300**, enabled, type A, IPv4 `address`. The plugin makes no AAAA, CNAME, or update calls. In releases after v0.7.2, its only delete calls run at shutdown, one per record this instance created, using the UniFi ID returned by the create. It does not re-check the record first, so a record you edited after creation is still deleted. Older releases and plain reconfiguration leave all records.

### Logs

Lines go to stderr with prefix `bollard[<provider name>]: ` and standard timestamps. The configured API key is replaced by `[redacted]` in error text. With inline configuration, Traefik debug logging may still print the key; use `unifiAPIKeyFile`.

## UniFi API calls

All calls send `X-API-KEY`. Per attempt: 15 s timeout, redirects not followed, 3 attempts, exponential backoff from 500 ms (jittered, capped at 10 s). HTTP 429 is retried for every method; 5xx is retried for `GET` only in this plugin (create is a `POST` and is not retried within a poll).

**Modern API** (selected when the sites endpoint exists):

| Call | Purpose |
|---|---|
| `GET /proxy/network/integration/v1/sites?offset=N&limit=200` | Resolve `unifiSite` to a site UUID. Paginated by `totalCount`. |
| `GET /proxy/network/integration/v1/sites/<uuid>/dns/policies?offset=N&limit=200` | List records. |
| `POST /proxy/network/integration/v1/sites/<uuid>/dns/policies` | Create `{"type":"A_RECORD","enabled":true,"domain":H,"ipv4Address":IP,"ttlSeconds":300}`. |

If the first sites request returns 404, 405, or 501, the client falls back to the **legacy API**, using `unifiSite` literally as the site name in the path (the UUID and display name do not apply):

| Call | Purpose |
|---|---|
| `GET /proxy/network/v2/api/site/<unifiSite>/static-dns` | List. |
| `POST /proxy/network/v2/api/site/<unifiSite>/static-dns` | Create `key`, `value`, `record_type: "A"`, `ttl: 300`, `enabled: true`. |

Any other site-probe failure is an error and does not fall back.

### Response handling

The plugin lists and creates during polling, and deletes only at shutdown (see [Record shape](#record-shape)). In current source, a list response that has no record list (`data` missing or `null`) is an error (`unifi: list records: response has no data`) rather than an empty zone. The poll creates nothing and logs the error, then retries on the next poll. A truly empty zone must return an empty list. A create response without an ID is also an error (`unifi: create returned no record ID`). Each listing has a 60-second deadline, and pagination that is still unfinished after crossing 100,000 entries stops with an error instead of returning a truncated list. The check runs after each page, so the final page can carry a listing past 100,000.

## Limits

| Property | Value |
|---|---|
| Uniqueness across instances | None. Check-then-create is not atomic. |
| Persistent state | None. |
| Record changes after creation | None. Different IP, router removal, and shutdown all leave the record. |
| Router API response | 4 MiB per page |
| UniFi response | 25 MiB |
| Supported record type | A, IPv4 only |

## Example project knobs

[`docs/examples/traefik-plugin/`](../examples/traefik-plugin/) renders Traefik config from `.env`:

| Variable | Default | Effect |
|---|---|---|
| `UNIFI_HOST` | required | `unifiHost` |
| `UNIFI_SITE` | `Default` | `unifiSite` |
| `UNIFI_SKIP_TLS_VERIFY` | `false` | `unifiSkipTLSVerify` (`true` or `false`) |
| `UNIFI_CA_CERT` | empty | `unifiCACert`; set by `compose.custom-ca.yaml` |
| `TRAEFIK_ADDRESS` | required | `address` |
| `APP_HOST` | `app.home.arpa` | nginx router host |
| `POLL_INTERVAL` | `30s` | `pollInterval` |
| `BOLLARD_VERSION` | `v0.8.0` | Plugin tag (remote mode only) | <!-- x-release-please-version -->
| `HTTP_BIND`, `HTTP_PORT` | `127.0.0.1`, `8000` | Host side of the published port 80 |
| `TRAEFIK_IMAGE`, `RENDER_IMAGE` | `traefik:v3.7.19`, `busybox:stable` | Images |
| `UNIFI_API_KEY_FILE` | `./secrets/unifi_api_key` | Host path of the key file secret |
| `BOLLARD_SOURCE` | `../../..` | Repository root for `compose.local-plugin.yaml` |
| `UNIFI_CA_FILE` | required by overlay | Host path of the CA PEM for `compose.custom-ca.yaml` |

Values must be single-line and contain none of `| & \ "` or a backtick.

| File | Role |
|---|---|
| `compose.yaml` | `render`, `traefik` 3.7.19, `app` (`nginx:stable-alpine`); publishes only port 80 |
| `compose.local-plugin.yaml` | Overlay: local source plugin, `host.docker.internal` |
| `compose.custom-ca.yaml` | Overlay: private CA secret and `unifiCACert` |
| `templates/` | Static, dynamic, and plugin-block templates plus `render.sh` |
| `env.example` | Template for `.env` |

Traefik's API is on `127.0.0.1:8081` inside the container. It has no authentication, so every process in that network namespace can read it. The Traefik dashboard is disabled and `api.insecure` is `false`.
