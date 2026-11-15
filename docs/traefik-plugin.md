# Traefik provider plugin

Bollard can run inside Traefik as a provider plugin, independently of the Docker DNS controller. It reads Traefik's `/api/http/routers` endpoint, extracts exact positive HTTP `Host()` names, and creates missing UniFi A records pointing at one configured Traefik IPv4 address. It does not supply application routes; keep using Traefik's file, Docker, or other routing providers.

## Deploy with a released plugin

Validated with Traefik 3.7.19 using a real local plugin and mock UniFi controller. Plugin loading is experimental; validate other Traefik versions before production use.

The remote module is `github.com/j0sh3rs/bollard`, not the `/traefik` package. The root `.traefik.yml` selects that package. This guide does not assert that a release containing the plugin already exists. Replace `REPLACE_WITH_RELEASE_TAG` below with a real `vX.Y.Z` tag containing the plugin, after publication. Older releases without `.traefik.yml` cannot load it.

Create this Traefik **static** configuration (`traefik.yml`):

```yaml
entryPoints:
  web:
    address: ":80"
  bollard-api:
    address: "127.0.0.1:8080"

api:
  insecure: false
  dashboard: false

experimental:
  plugins:
    bollard:
      moduleName: github.com/j0sh3rs/bollard
      version: "REPLACE_WITH_RELEASE_TAG" # Replace with a published tag containing this plugin.

providers:
  file:
    directory: /etc/traefik/dynamic
    watch: true
  plugin:
    bollard:
      traefikAPI: http://127.0.0.1:8080
      unifiHost: https://192.0.2.1
      unifiAPIKeyFile: /run/secrets/unifi_api_key
      unifiSite: default
      # unifiCACert: /run/secrets/unifi_ca.pem
      unifiSkipTLSVerify: false
      address: 192.0.2.10
      pollInterval: 30s
```

Replace example addresses with reachable addresses. `address` is the IPv4 address clients use to reach **Traefik**, not an application backend or the UniFi controller. The controller URL must already resolve and be reachable before plugin startup; this plugin cannot bootstrap DNS for its own controller. Use a reachable IP or an independently resolvable hostname. For an HTTPS IP URL, the certificate must contain that IP in its subject alternative names.

Create `/etc/traefik/dynamic/bollard-api.yml` as **dynamic** configuration:

```yaml
http:
  routers:
    bollard-api:
      entryPoints:
        - bollard-api
      rule: PathPrefix(`/api`)
      service: api@internal
```

This exposes the API only on loopback in Traefik's network namespace. Do not bind `bollard-api` to `:8080`, publish port 8080 from a container, or attach this router to a public entrypoint. Leave `api.insecure: false`. The plugin needs read access to the router API; it does not require the dashboard. Its local HTTP client does not configure API authentication headers, so this example uses isolation rather than a public authenticated API. Other processes in the same network namespace can access this API; keep that namespace trusted.

For a container deployment, mount the static configuration, dynamic directory, and secrets read-only into the Traefik container at the paths above. For example, add these mounts to your existing Traefik Compose service:

```yaml
services:
  traefik:
    volumes:
      - ./traefik.yml:/etc/traefik/traefik.yml:ro
      - ./dynamic:/etc/traefik/dynamic:ro
    secrets:
      - unifi_api_key
      # - unifi_ca

secrets:
  unifi_api_key:
    file: ./secrets/unifi_api_key
  # unifi_ca:
  #   file: ./secrets/unifi_ca.pem
  #   # Set unifiCACert to /run/secrets/unifi_ca when using this secret.
```

Put only the API key in the secret file, restrict host file permissions, and keep it outside version control. Use a dedicated UniFi local account with permission to manage DNS. Do not expose the key in labels or logs. TLS verification is enabled by default, unlike the standalone Docker controller's historical default. Use `unifiCACert` for a private CA; do not disable verification as a deployment shortcut.

Prefer the key file even when inline configuration is convenient: Traefik can include inline plugin settings in its debug logs.

### Discover file-defined routes

Add `/etc/traefik/dynamic/app.yml`:

```yaml
http:
  routers:
    app:
      entryPoints:
        - web
      rule: Host(`app.home.arpa`)
      service: app
  services:
    app:
      loadBalancer:
        servers:
          - url: http://192.0.2.20:8080
```

Use a reachable backend URL. Once Traefik exposes the enabled router in its API, Bollard creates `app.home.arpa → 192.0.2.10` with TTL 300. The backend address does not become the DNS record value. To use one dynamic file instead, replace `providers.file.directory` with `providers.file.filename: /etc/traefik/dynamic.yml`, combining the API and application routers in that file. Do not configure both `filename` and `directory`.

### Discover Docker-defined routes

If Traefik already uses its Docker provider, no Bollard-specific labels are needed. Add `docker` alongside `file` and `plugin` in the static `providers` map:

```yaml
providers:
  docker:
    exposedByDefault: false
```

Keep the file provider enabled for the private API router. Configure Docker access using your existing secured Docker provider setup; the Bollard plugin itself does not access the Docker socket. Label an application sharing a network with Traefik:

```yaml
services:
  app:
    image: traefik/whoami:v1.11.0
    labels:
      traefik.enable: "true"
      traefik.http.routers.app.entrypoints: web
      traefik.http.routers.app.rule: Host(`app.home.arpa`)
      traefik.http.services.app.loadbalancer.server.port: "80"
```

The plugin discovers `app@docker` through the same API as `app@file`. Never run the standalone Bollard Docker controller and this plugin against the same DNS names: the controller can delete records when containers stop.

## Configuration reference

All fields belong under `providers.plugin.bollard` in static configuration. The name `bollard` must match the experimental plugin registration.

| Field | Default | Meaning |
|---|---|---|
| `traefikAPI` | `http://127.0.0.1:8080` | Traefik API base URL; the plugin appends `/api/http/routers`. |
| `unifiHost` | Required | Reachable HTTP(S) UniFi controller base URL. Prefer HTTPS. |
| `unifiAPIKeyFile` | Unset | Recommended: path to a readable API key file inside Traefik. |
| `unifiAPIKey` | Unset | Inline API key alternative. Configure exactly one key source. |
| `unifiSite` | `default` | UniFi site name. |
| `unifiCACert` | Unset | Path to a custom CA PEM file inside Traefik. |
| `unifiSkipTLSVerify` | `false` | Explicit insecure TLS override; leave false in production. |
| `address` | Required | Traefik's client-reachable IPv4 address for all created A records. |
| `pollInterval` | `30s` | Positive Go duration between discovery polls. |

Configuration and secret files are validated without network I/O during `New`/`Init`. The worker polls immediately and then every `pollInterval`. API/controller errors are logged and retried on later polls; they do not turn the plugin into a no-op mode. Restart Traefik after changing plugin configuration or rotating its loaded key file.

## Scope and operational limits

- Creates missing A records only, with TTL 300. No AAAA, CNAME, updates, or deletes.
- Existing matching active A records are already satisfied. Conflicting active A records are skipped, never overwritten. Other record types and disabled records are outside this client's listing; UniFi may reject a conflicting create.
- No persistent ownership database. Router removal, plugin shutdown, and reconfiguration preserve DNS records. Remove stale records manually.
- Only enabled external HTTP routers with exact positive literal `Host()` matches qualify. Names are normalized and deduplicated. Negative matches, `HostRegexp`, TCP/SNI routes, IP literals, and invalid DNS names do not generate records.
- Discovery follows configured router rules, not request headers or observed traffic. Anyone allowed to define eligible Traefik routes can request DNS creation; restrict route configuration accordingly.
- Every instance managing the same names must use the same target IPv4. Different targets conflict rather than update. This is not a distributed ownership or leader-election system.
- This plugin does not replace authoritative DNS configuration, configure clients to use UniFi DNS, or obtain TLS certificates for applications.

## Develop with a local plugin

Run Traefik from a directory containing `plugins-local`. Link the **repository root**, not just the `traefik` subdirectory:

```sh
mkdir -p plugins-local/src/github.com/j0sh3rs
ln -s /absolute/path/to/bollard plugins-local/src/github.com/j0sh3rs/bollard
```

Replace the remote `experimental.plugins` block with:

```yaml
experimental:
  localPlugins:
    bollard:
      moduleName: github.com/j0sh3rs/bollard
```

Keep `providers.plugin.bollard` and the private API route unchanged. Start your installed Traefik binary from that working directory:

```sh
traefik --configFile=./traefik.yml
```

When Traefik runs in a container, the symlink target must exist inside the container too; alternatively mount the repository directly at `/plugins-local/src/github.com/j0sh3rs/bollard` and use `/` as the working directory. Do not enable remote and local registrations under the same plugin name together.

Run the native tests and optional Docker-backed runtime smoke test:

```sh
go test -race . ./internal/unifi ./traefik
TRAEFIK_IMAGE=traefik:v3.7.19 go test -race ./traefik -run TestTraefikPlugin -v -count=1
```

The runtime test starts real Traefik with a local plugin and a mock UniFi controller, verifies DNS creation and backend routing, then removes its container. It never contacts your UniFi controller. `.github/workflows/traefik-plugin.yml` runs this check in CI.

## Publish to the plugin catalog

These are maintainer steps, not actions performed by this change:

1. Keep `github.com/j0sh3rs/bollard` public and non-forked. Keep `.traefik.yml`, `go.mod`, and the existing MIT `LICENSE` at the repository root.
2. Keep manifest `type: provider` and `import: github.com/j0sh3rs/bollard/traefik`. The module remains `github.com/j0sh3rs/bollard`; the standalone root `main` package remains separate.
3. Add the GitHub repository topic `traefik-plugin`.
4. Validate native tests and actual local plugin loading against the intended Traefik version. Existing CI runs `go test -race ./...` and `go build ./...`; native tests alone do not prove interpreter compatibility.
5. Merge through the existing release-please process and publish the resulting release tag using `.github/workflows/release-please.yml`. Do not reuse an older tag that lacks this plugin or manually edit release metadata to pretend a release exists.
6. Check catalog indexing and its validation results, then pin deployments to that real release tag. A manifest and topic do not guarantee catalog acceptance.

Manifest `testData` uses dummy credentials and unavailable loopback port 1. Configuration initialization performs no network I/O; a running worker still attempts real discovery and logs the unavailable endpoint. No fake enabled flag or silent fallback is provided.

The plugin runtime imports only the standard library and the internal UniFi client. The Docker controller's dependencies remain in the root module but are not imported by the plugin runtime; this change requires no new vendored dependencies or unsafe interpreter permission.

Traefik references: [plugin installation](https://plugins.traefik.io/install), [experimental plugin configuration](https://doc.traefik.io/traefik/reference/install-configuration/experimental/plugins/), and [API configuration](https://doc.traefik.io/traefik/reference/install-configuration/api-dashboard/).
