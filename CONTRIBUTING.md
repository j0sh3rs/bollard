# Contributing

Bollard has two runtimes in one Go module, `github.com/j0sh3rs/bollard`: the standalone Docker daemon (root `main` package plus `internal/`) and the Traefik provider plugin (`traefik/`). Read the [architecture](docs/architecture.md) and the [plugin explanation](docs/traefik/explanation.md) before changing behavior.

## Layout

| Path | Role |
|---|---|
| `main.go` | Daemon entry point: startup, UniFi retry, metrics listener, shutdown |
| `internal/unifi` | UniFi client shared by the daemon and the plugin |
| `internal/dnsname` | Hostname validation and normalization shared by both |
| `internal/reconciler`, `internal/store`, `internal/docker`, `internal/dryrun`, `internal/resolver`, `internal/config`, `internal/log`, `internal/metrics` | Daemon only |
| `traefik/` | Plugin. Runs under Traefik's Yaegi interpreter |
| `docs/` | User documentation, organised by [Diátaxis](https://diataxis.fr/) type |
| `dev/` | Local Compose project with a labelled test container |

The plugin may import only the standard library, `internal/dnsname`, and `internal/unifi`. Do not make `internal/unifi` depend on daemon packages. Native tests do not prove Yaegi compatibility, so run the Traefik container test for any plugin or `internal/unifi` change.

## Checks

```sh
gofmt -l .                      # must print nothing
go vet ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
go test -race ./...
go build ./...
TRAEFIK_IMAGE=traefik:v3.7.19 go test -race ./traefik -run TestTraefikPlugin -v -count=1
docker build --build-arg VERSION=v0.0.0-smoke -t bollard:smoke .
BOLLARD_SMOKE_IMAGE=bollard:smoke go test -race . -run '^TestDaemonImageSmoke$' -count=1 -v
```

Set `TEST_POSTGRES_DSN` (for example `postgres://bollard:bollard@localhost:5432/bollard_test?sslmode=disable`) to include the Postgres store tests. The Docker-backed tests skip when their variables are unset; CI sets all three.

Tests must use simulated UniFi servers. Never point a test at a real controller or put a real API key in a fixture. A simulated list endpoint must return `[]` for an empty zone, not `null`: the client rejects a response with no record list.

## Pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/) (`fix:`, `feat:`, `docs:`, `chore:`). release-please builds the changelog and release PR from them. Do not edit released sections of `CHANGELOG.md` or release metadata by hand.
- Change code and its documentation together. Update the configuration, monitoring, operations, and plugin reference pages when a flag, label, metric, log message, or error string changes.
- Add a regression test for a bug fix. Test behavior an operator can observe, not wiring.
- Write for the reader's task: tutorials teach, how-to guides solve one problem, reference pages state facts, explanations give reasons. Keep each fact in one place and link to it.

## Releases

Merging to `main` does not publish anything. release-please opens a release PR; merging it creates the tag, binaries, signed multi-arch image, and SBOM. Plugin catalog indexing is a separate step; see [Publish a release and the plugin catalog](docs/traefik/how-to.md#publish-a-release-and-the-plugin-catalog).
