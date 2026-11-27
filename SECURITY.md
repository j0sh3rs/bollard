# Security policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: the repository's **Security** tab, then **Report a vulnerability**. Do not open a public issue with exploit details, API keys, or controller addresses. If private reporting is unavailable, open an issue that asks for a private contact and contains no details.

Supported versions and response times are not yet defined; the maintainer will confirm a fix path after triage.

## What Bollard handles

| Asset | Where it lives | Guidance |
|---|---|---|
| UniFi API key | `UNIFI_API_KEY` or `UNIFI_API_KEY_FILE` (daemon); `unifiAPIKeyFile` or `unifiAPIKey` (plugin) | Use a dedicated local account. Prefer a key file (Docker secret): it keeps the key out of the process environment, and Traefik debug logs can print inline plugin settings. Logs replace the key with `[redacted]`. |
| Docker socket (daemon) | Read-only bind mount | A read-only socket bind still grants a powerful API. Protect the host and the socket. |
| Ownership database | SQLite file or Postgres | Contains hostnames, IPs, and record IDs, not credentials. Postgres DSN passwords, including `password` and `sslpassword` query parameters, are masked in logs. Use `sslmode=require` or stricter off-host. |
| `/metrics` and `/healthz` | `METRICS_ADDR`, default `127.0.0.1:9090` | Unauthenticated, so the default is loopback. A wildcard bind exposes them on every interface; restrict by firewall or private network. |
| Traefik router API (plugin) | Private loopback entrypoint | Unauthenticated. Anything in that network namespace can read router names and rules. |

## Defaults to review

- The **daemon** defaults `UNIFI_SKIP_TLS_VERIFY` to `true` so self-signed controllers work. That lets a network attacker impersonate the controller and read the API key. Set it to `false`, or set `UNIFI_CA_CERT`, outside a trusted LAN. The **plugin** verifies TLS by default.
- Anyone who can define an eligible Traefik route can cause the plugin to create a DNS record. Restrict who can write route configuration.
- The **daemon** will not create or adopt records for names it depends on. Reserved names are derived once at startup from `UNIFI_HOST`, every host in a `postgres://` `DATABASE_URL` (including `?host=` and multi-host lists) and a TCP `DOCKER_HOST`, plus a fixed list: `host.docker.internal`, `gateway.docker.internal`, `kubernetes.docker.internal`, `vm.docker.internal`, `docker.internal`, `host.containers.internal`, the legacy `docker.for.mac.*` and `docker.for.win.*` names, and `localhost`. Matching is exact: a subdomain of a reserved name is not covered, and names resolved only by other software (registries, other databases) are not reserved. A refused start event logs `handle event failed`; reconcile and `--adopt` log a `hostname is reserved` warning. Each refusal increments `bollard_record_hostname_conflicts_total`. The **Traefik plugin applies a smaller guard**: it skips `unifiHost`, the `traefikAPI` host and the same fixed list (logging `hostname ... is reserved, not creating it`), but has no Postgres or `DOCKER_HOST` setting to derive names from. The guard does not replace TLS verification: keep `UNIFI_SKIP_TLS_VERIFY=false` or set `UNIFI_CA_CERT` so an impersonated controller name cannot capture the API key.
- The Docker build context excludes `.git`, `.env`, `dev/.env`, and the plugin example's `.env` and `secrets/`. Keep credentials out of the repository regardless.

See the [configuration reference](docs/configuration.md), [monitoring reference](docs/monitoring.md#endpoints), and [plugin explanation](docs/traefik/explanation.md#where-traefiks-api-fits) for details. Verify release artifacts with the steps in [Verify a release](docs/operations.md#verify-a-release): images and binaries are signed with keyless Cosign, and CI plus the release workflow run `go vet`, `golangci-lint`, `govulncheck` and a Trivy scan of the image.
