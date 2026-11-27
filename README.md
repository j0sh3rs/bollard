# bollard

[![CI](https://github.com/j0sh3rs/bollard/actions/workflows/ci.yml/badge.svg)](https://github.com/j0sh3rs/bollard/actions/workflows/ci.yml)
[![Traefik plugin](https://github.com/j0sh3rs/bollard/actions/workflows/traefik-plugin.yml/badge.svg)](https://github.com/j0sh3rs/bollard/actions/workflows/traefik-plugin.yml)
[![Release](https://github.com/j0sh3rs/bollard/actions/workflows/release-please.yml/badge.svg)](https://github.com/j0sh3rs/bollard/actions/workflows/release-please.yml)

Automatically create UniFi DNS records (A, and AAAA and CNAME in the standalone controller) for self-hosted services. Choose the Traefik provider plugin for router-driven DNS, or the standalone controller for Docker-label-driven DNS with ownership tracking and cleanup.

## Choose a mode

| | Traefik provider plugin | Standalone Docker controller |
|---|---|---|
| Discovery | Enabled HTTP router `Host()` rules through Traefik's API | Opted-in Docker container labels and events |
| DNS target | Explicit client-reachable Traefik IPv4 address | The container's macvlan or chosen-network address, else the inferred host address, or a per-container override |
| State | Rebuilt from Traefik and UniFi each poll; no database | Ownership database in SQLite or PostgreSQL |
| Creates records | A | A, AAAA and CNAME |
| Deletes records | Only the ones it created, at Traefik shutdown (releases after v0.7.2) | Deletes owned records on stop; reconciliation catches missed stops and retries failed deletes |
| Docker socket required by Bollard | No | Yes |
| Runs as | A Traefik provider plugin | A separate service |

**Do not manage the same names with both modes.** Stop the standalone controller before replacing it with the plugin. Removing a labeled container while the controller is running can delete its owned DNS record.

## Start with the Traefik plugin

The first published plugin release is **v0.7.0**. Register it in Traefik's static configuration:

<!-- x-release-please-start-version -->
```yaml
experimental:
  plugins:
    bollard:
      moduleName: github.com/j0sh3rs/bollard
      version: v0.7.2
```
<!-- x-release-please-end -->

Registration alone does not enable discovery. Configure `providers.plugin.bollard`, a private loopback API route, and a UniFi API key file. The [plugin tutorial](docs/traefik/tutorial.md) supplies a complete nginx Compose deployment and DNS/routing checks. Existing installations can use the [integration how-to guide](docs/traefik/how-to.md).

Use the IPv4 address clients use to reach **Traefik**, not the nginx container address. Clients must use UniFi DNS, or a resolver forwarding the relevant zone to UniFi.

### Released behavior versus current source

v0.7.1 added two changes to the first plugin release:

| Behavior | v0.7.0 | Since v0.7.1 |
|---|---|---|
| Modern UniFi site selection | Display name or site UUID | Also accepts `internalReference`, such as `default` for a site named `Default` |
| Names protected against creation | Existing enabled A records | Every named record reported by UniFi, including CNAME and disabled records |

For v0.7.0, configure the actual site display name or UUID. For unreleased changes or an unindexed tag, follow the [local plugin instructions](docs/traefik/how-to.md).

The plugin never updates a record. It creates missing records. Releases after v0.7.2 also delete, when Traefik stops the plugin, only the records that instance created; v0.7.2 and earlier leave all records in place. Records from earlier runs, other writers, or created by hand are never deleted. Router removal and target-IP changes do not modify existing records. Use one DNS writer per site/domain scope across Traefik replicas. See [plugin concurrency and ownership](docs/traefik/explanation.md).

## Start with the standalone controller

Create a private `.env` file, excluded from version control:

```dotenv
UNIFI_HOST=https://unifi.example.net
UNIFI_API_KEY=replace-with-dedicated-account-api-key
UNIFI_SITE=Default
UNIFI_SKIP_TLS_VERIFY=false
DATABASE_URL=file:/data/bollard.db
```

Use your actual site display name or UUID on the modern API; legacy controllers use the site reference. The controller certificate must be trusted. For a private CA, mount its PEM file and configure `UNIFI_CA_CERT`; see the [configuration reference](docs/configuration.md).

```yaml
services:
  bollard:
    image: ghcr.io/j0sh3rs/bollard:v0.7.1
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - bollard-data:/data
    env_file: .env

volumes:
  bollard-data:
```

Opt an existing application into DNS management:

```yaml
labels:
  dns.bollard/hostname: app.home.arpa, www.home.arpa # one or more names
  dns.bollard/ip-override: "192.0.2.10"              # Replace with the address clients should use.
```

The explicit IP avoids host-interface ambiguity. Replace the documentation address with a reachable address. A container on a macvlan network publishes its own address automatically, `dns.bollard/network` picks another network's address, and `dns.bollard/record-type` can be `AAAA` or `CNAME` (with `dns.bollard/target`). Persist and back up the ownership database; recover lost ownership with `--adopt`, not by deleting records. Try new labels with `DRY_RUN=true` first. See the [standalone tutorial](docs/getting-started.md), the [label reference](docs/configuration.md#container-labels) and the [operations guide](docs/operations.md).

The standalone controller historically defaults to skipping UniFi TLS verification; set `UNIFI_SKIP_TLS_VERIFY=false` explicitly. The plugin verifies TLS by default. A read-only Docker socket bind still grants access to a powerful API; protect the socket and the host.

## Documentation

| Need | Document |
|---|---|
| Learn by deploying nginx and registering DNS | [Traefik tutorial](docs/traefik/tutorial.md) |
| Integrate, migrate, troubleshoot, or develop a plugin | [Traefik how-to guides](docs/traefik/how-to.md) |
| Look up plugin configuration and supported rules | [Traefik reference](docs/traefik/reference.md) |
| Understand polling, ownership, and concurrency | [Traefik explanation](docs/traefik/explanation.md) |
| Browse plugin documentation and examples | [Plugin documentation hub](docs/traefik-plugin.md) |
| Set up the standalone controller | [Standalone tutorial](docs/getting-started.md) |
| Look up environment variables and labels | [Standalone configuration](docs/configuration.md) |
| Recover ownership, back up state, or upgrade | [Standalone operations](docs/operations.md) |
| Understand the controller's internals | [Architecture](docs/architecture.md) |
| Inspect standalone metrics and endpoint limits | [Monitoring](docs/monitoring.md) |
| Adapt a deployment example | [Examples](docs/examples/) |
| Check release history or future work | [Changelog](CHANGELOG.md), [roadmap](docs/ROADMAP.md) |
| Contribute or report a security issue | [Contributing](CONTRIBUTING.md), [Security](SECURITY.md) |

## Development checks

With the Go toolchain required by `go.mod`:

```sh
gofmt -l .                      # must print nothing
go vet ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
go test -race ./...
TRAEFIK_IMAGE=traefik:v3.7.19 go test -race ./traefik -run TestTraefikPlugin -v -count=1
docker build --build-arg VERSION=v0.0.0-smoke -t bollard:smoke .
BOLLARD_SMOKE_IMAGE=bollard:smoke go test -race . -run '^TestDaemonImageSmoke$' -count=1 -v
```

The Docker-backed tests skip when their environment variable is unset. The Traefik test loads the plugin in real Traefik against a mock UniFi controller. The image smoke test runs the built image against an isolated Docker daemon and a simulated UniFi API. Set `TEST_POSTGRES_DSN` to include the PostgreSQL store tests. Neither test contacts your real controller. See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow and [SECURITY.md](SECURITY.md) to report a vulnerability.

Bollard does not configure client resolvers, replace authoritative DNS, or issue application TLS certificates. Those remain operator responsibilities.
