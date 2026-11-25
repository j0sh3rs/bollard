# Security policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: the repository's **Security** tab, then **Report a vulnerability**. Do not open a public issue with exploit details, API keys, or controller addresses. If private reporting is unavailable, open an issue that asks for a private contact and contains no details.

Supported versions and response times are not yet defined; the maintainer will confirm a fix path after triage.

## What Bollard handles

| Asset | Where it lives | Guidance |
|---|---|---|
| UniFi API key | `UNIFI_API_KEY` (daemon); `unifiAPIKeyFile` or `unifiAPIKey` (plugin) | Use a dedicated local account. Prefer the plugin's key file: Traefik debug logs can print inline settings. Logs replace the key with `[redacted]`. |
| Docker socket (daemon) | Read-only bind mount | A read-only socket bind still grants a powerful API. Protect the host and the socket. |
| Ownership database | SQLite file or Postgres | Contains hostnames, IPs, and record IDs, not credentials. Postgres DSN passwords, including `password` and `sslpassword` query parameters, are masked in logs. Use `sslmode=require` or stricter off-host. |
| `/metrics` and `/healthz` | `METRICS_ADDR`, default `:9090` | Unauthenticated. Bind to loopback or restrict by firewall. |
| Traefik router API (plugin) | Private loopback entrypoint | Unauthenticated. Anything in that network namespace can read router names and rules. |

## Defaults to review

- The **daemon** defaults `UNIFI_SKIP_TLS_VERIFY` to `true` so self-signed controllers work. That lets a network attacker impersonate the controller and read the API key. Set it to `false`, or set `UNIFI_CA_CERT`, outside a trusted LAN. The **plugin** verifies TLS by default.
- Anyone who can define an eligible Traefik route can cause the plugin to create a DNS record. Restrict who can write route configuration.
- The Docker build context excludes `.git`, `.env`, `dev/.env`, and the plugin example's `.env` and `secrets/`. Keep credentials out of the repository regardless.

See the [configuration reference](docs/configuration.md), [monitoring reference](docs/monitoring.md#endpoints), and [plugin explanation](docs/traefik/explanation.md#where-traefiks-api-fits) for details. Verify release artifacts with the steps in [Verify a release](docs/operations.md#verify-a-release).
