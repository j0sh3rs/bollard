# How-to guides: Traefik provider plugin

Task recipes for operators who already run the [tutorial stack](tutorial.md) or an equivalent. Exact field values live in the [reference](reference.md).

Contents: [file routes](#discover-file-defined-routes) · [Docker routes](#discover-docker-defined-routes) · [private CA](#verify-the-controller-with-a-private-ca) · [application HTTPS](#serve-application-https) · [one DNS writer](#run-one-dns-writer-with-multiple-replicas) · [rotate the key](#rotate-the-api-key) · [migrate](#migrate-from-the-standalone-controller) · [troubleshoot](#troubleshoot) · [local development](#develop-with-a-local-plugin) · [publish](#publish-a-release-and-the-plugin-catalog)

## Discover file-defined routes

Prerequisite: the plugin is running with the private API router from [`templates/dynamic.yml.tmpl`](../examples/traefik-plugin/templates/dynamic.yml.tmpl) (entrypoint `bollard-api` on `127.0.0.1:8081`, rule ``PathPrefix(`/api`)``, service `api@internal`).

Add any file-provider router with a positive `Host()` rule. In the example project, merge it into `templates/dynamic.yml.tmpl` (the `http`, `routers`, and `services` keys already exist there) and run `docker compose up -d --force-recreate`; the `render` job writes that file into the directory Traefik watches. In your own deployment, add it to any file in your `providers.file.directory`:

```yaml
http:
  routers:
    wiki:
      entryPoints: [web]
      rule: Host(`wiki.home.arpa`)
      service: wiki
  services:
    wiki:
      loadBalancer:
        servers:
          - url: http://wiki:8080
```

Traefik's file provider has `watch: true`, so the router appears without a restart. Within one `pollInterval`, Bollard creates `wiki.home.arpa → <address>`. The backend URL never becomes the record value.

Use either `providers.file.directory` or `providers.file.filename`, not both.

## Discover Docker-defined routes

The plugin does not talk to Docker. It sees Docker routers because Traefik's own Docker provider publishes them to the same router API. This is an **integration recipe** for an existing, already-secured Traefik Docker setup, not a standalone Compose file.

1. In Traefik's static configuration, add the provider next to `file` and `plugin`. Keep `exposedByDefault: false`:

   ```yaml
   providers:
     docker:
       exposedByDefault: false
   ```

2. Give the Traefik container read access to the Docker API the way your environment already does (a socket mount, or a socket proxy). Do not copy a socket mount from an unrelated example; choose the access model you already trust. The tutorial stack does not include it.

3. Put Traefik and the app on a shared Docker network, then label the app container:

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

4. Keep the file provider (or any other provider) that defines the private `bollard-api` router.

Bollard sees `app@docker` exactly like a file router. No `dns.bollard/*` labels are read, so they have no effect here. Stopping the container removes the route but **leaves the DNS record in place**.

## Verify the controller with a private CA

TLS verification is on by default. For a controller certificate signed by a private CA:

1. Save the CA certificate as a PEM file on the Docker host, for example `./secrets/unifi_ca.pem`.
2. Start the example with the CA overlay. Compose refuses to start if `UNIFI_CA_FILE` is unset, so no placeholder path is ever used:

   ```sh
   UNIFI_CA_FILE=./secrets/unifi_ca.pem \
     docker compose -f compose.yaml -f compose.custom-ca.yaml up -d
   ```

   The overlay mounts the file at `/run/secrets/unifi_ca.pem` and sets `unifiCACert` to that path.

In your own deployment, mount the PEM read-only and set `unifiCACert` to its in-container path. Note:

- The CA is read when Bollard first connects to UniFi, **not at Traefik startup**. A missing or malformed CA file is logged on each poll as `unifi: read CA cert` or `unifi: no valid certs in <path>`, and only after Traefik reports at least one eligible router.
- When `unifiCACert` is set, only that CA is trusted (system roots are not added) and `unifiSkipTLSVerify` is ignored.
- If `unifiHost` is an IP URL, the certificate must list that IP as a subject alternative name. A name URL must match the certificate name and resolve without help from Bollard.
- `unifiSkipTLSVerify: true` disables verification entirely. Use it for a throwaway lab only.

## Serve application HTTPS

The plugin only reads `Host()` rules; certificates are entirely your Traefik configuration. The tutorial stack is HTTP-only on purpose. To add HTTPS:

1. Add an entrypoint in static config and publish its port:

   ```yaml
   entryPoints:
     websecure:
       address: ":443"
   ```

   ```yaml
   # compose.yaml, traefik service
   ports:
     - "${HTTP_BIND:-127.0.0.1}:8443:443"
   ```

2. Add a TLS router for the same host. Both routers yield one DNS record because names are deduplicated:

   ```yaml
   http:
     routers:
       app-secure:
         entryPoints: [websecure]
         rule: Host(`app.home.arpa`)
         service: app
         tls: {}
   ```

3. Supply certificates. Pick one, and own it:

   - **Your own certificate files.** Mount them read-only and register them in dynamic configuration:

     ```yaml
     tls:
       certificates:
         - certFile: /certs/app.home.arpa.crt
           keyFile: /certs/app.home.arpa.key
     ```

   - **An ACME resolver.** Configure `certificatesResolvers` in static config for your CA and challenge type. A private name such as `app.home.arpa` cannot be validated by a public CA; that is a property of the resolver you choose, not of Bollard. Resolver credentials, renewals, and storage are operator-owned.

Bollard issues no certificates and does not require TLS on the router. TLS settings are not part of its eligibility rules.

## Run one DNS writer with multiple replicas

1. Configure a dedicated Traefik instance with one replica as the DNS writer. It must see all routes for the UniFi site and domain scope it manages.
2. Enable the Bollard provider and mount its UniFi key only on the writer. Keep the ordinary routing providers enabled on the serving replicas, but omit Bollard from their `providers.plugin` configuration. Do not run another DNS writer for the same names.
3. Set `address` to the stable IPv4 address clients use to reach Traefik, such as a load balancer. Do not use the writer's instance address unless clients should route there.
4. When replacing or upgrading the writer, stop the old process and confirm it has exited before starting the new one. Use a stop-before-start strategy, not a rolling update that can run both writers at once. The other replicas keep serving traffic, and stopping the writer leaves existing DNS records in place.

Use only one registered Bollard writer per site/domain scope. The plugin's [coordination](explanation.md#what-concurrent-means-here) does not extend across plugin aliases or across processes.

## Rotate the API key

The key file is read once, when Traefik initializes the plugin. To rotate:

1. Create the new key in UniFi and write it to the secret file (`secrets/unifi_api_key` in the example).
2. Restart Traefik so the plugin re-reads it:

   ```sh
   docker compose restart traefik
   ```

3. Check the logs for `bollard[` errors, then revoke the old key in UniFi.

For an inline `unifiAPIKey`, edit the Traefik static configuration and restart (in the example, change the template and run `docker compose up -d --force-recreate`). Prefer the key file: Traefik debug logs may print inline plugin settings. Restarting Traefik interrupts routing briefly.

## Migrate from the standalone controller

Both programs must never manage the same names. The standalone controller deletes a record when it sees its labeled container die, and its reconcile loop re-creates records for running labeled containers. **Stop the standalone controller first. Do not remove labeled containers while it runs**, or it deletes their records.

1. List what the standalone controller owns (see [Inspect and clean up stale records](../operations.md#clean-up-stale-records)) and note each hostname and IP.
2. Stop only the controller. Your application containers stay up:

   ```sh
   docker compose stop bollard
   docker compose ps bollard    # must show it exited
   ```

   Stopping it deletes nothing. Do not run `down -v` on it yet; its database is your rollback path.
3. Deploy the Traefik plugin and a route for each hostname. Existing records now block creation: the plugin leaves them alone and logs `DNS conflict for <host>: existing record preserved` when the IP differs from `address`.
4. Records created by the standalone controller point at a container IP or `ip-override`, not necessarily at Traefik. The plugin never edits them. In UniFi, delete or retarget each stale record by hand. After a deletion, the plugin recreates the name within one `pollInterval`, pointing at `address`.
5. Verify each name with `dig @controller` and `curl --resolve` as in the [tutorial](tutorial.md#7-prove-routing-with-dns-out-of-the-picture).
6. Only now remove the standalone service and its `dns.bollard/*` labels, and archive its data volume. Labels are inert to the plugin, but leaving them invites an accidental restart.

Rollback: remove the plugin provider, then restart the standalone controller, optionally with `--adopt` (see the [operations guide](../operations.md#adopt-existing-records)). Records the plugin created are not owned by the standalone controller until adopted.

## Troubleshoot

Read `docker compose logs traefik`. Bollard lines start with `bollard[<name>]:`. Errors repeat every poll until fixed, and the API key is redacted.

| Symptom | Cause | Fix |
|---|---|---|
| Traefik exits at startup with a plugin download or load error | Catalog unreachable, or the tag is not indexed by the catalog | Check outbound access and the tag. Use the [local plugin overlay](#develop-with-a-local-plugin) to rule out the catalog. |
| Traefik reports an error initializing the plugin (`bollard: ...`) | Invalid configuration. See [validation](reference.md#validation). | Fix the field named in the message. These fail fast, before any network I/O. |
| `DNS discovery failed: read Traefik API: ...` | `traefikAPI` wrong, or the `bollard-api` entrypoint/router missing | Confirm entrypoint address `127.0.0.1:8081` and router `bollard-api`. |
| `Traefik API returned HTTP 404` | Router not attached to the entrypoint, or rule does not match | Check the router rule ``PathPrefix(`/api`)`` and `entryPoints`. |
| `HTTP 3xx` from the Traefik API | Redirects are never followed | Point `traefikAPI` at the final URL. |
| No `created` line and no errors | No enabled router with a positive literal `Host()`, so UniFi was never contacted | Inspect the router list (below). Note `HostRegexp`, negated hosts, and IPs do not qualify. |
| `initialize UniFi: unifi: site "..." not found` | Wrong `unifiSite`. With `v0.7.0` only the display name or UUID matches. | Use `Default` or the UUID. Since `v0.7.1`, you can also use the internal reference `default`. |
| `unifi: site probe failed (status=401/403...)` or `unifi api error` | Wrong key or insufficient permission | Recreate the key for an account allowed to manage DNS. |
| `x509: ...` | Controller certificate not trusted, or IP missing from its SANs | [Private CA](#verify-the-controller-with-a-private-ca). |
| `unifi: read CA cert` or `no valid certs` | CA path missing or not PEM. Found only on first connection. | Fix the mount and path. |
| `create DNS record for <host>: ...` | UniFi rejected the create (for example a duplicate) | Read the UniFi message. The create is retried every poll. |
| `DNS conflict for <host>: existing record preserved` | A record with a different IP holds the name | Edit or delete it in UniFi. The plugin never will. Repeats every poll. |
| Controller URL by name fails to resolve | Bollard cannot bootstrap DNS for its own controller | Use a reachable IP or a name resolved elsewhere. |

Inspect the router list as the plugin sees it. The Traefik image has no shell, so join its network namespace from a throwaway container:

```sh
docker run --rm --network "container:$(docker compose ps -q traefik)" \
  busybox:stable wget -qO- 'http://127.0.0.1:8081/api/http/routers?per_page=100'
```

## Develop with a local plugin

Use a local plugin to test unreleased code, or to run against a mock UniFi controller without depending on the plugin catalog.

1. Run a controller or mock reachable from Docker. For a mock on the Docker host at port 18080 set in `.env`:

   ```sh
   UNIFI_HOST=http://host.docker.internal:18080
   UNIFI_SITE=Default
   ```

   The mock must implement `GET /proxy/network/integration/v1/sites`, and `GET`/`POST` on `.../sites/<id>/dns/policies` (see [reference](reference.md#unifi-api-calls)). `traefik/smoke_test.go` contains a working mock. For plain HTTP the URL scheme is accepted; use a dummy key file.

2. Start with the overlay. `BOLLARD_SOURCE` is the repository **root**, defaulting to `../../..` from the example directory:

   ```sh
   docker compose -f compose.yaml -f compose.local-plugin.yaml up -d --force-recreate
   ```

   The overlay mounts the source at `/plugins-local/src/github.com/j0sh3rs/bollard`, adds `host.docker.internal`, and renders `experimental.localPlugins` instead of `experimental.plugins`. It never reads `BOLLARD_VERSION`.

3. Do not register the same plugin name as remote and local together.

Native tests and the Docker-backed runtime smoke test:

```sh
go test -race . ./internal/unifi ./traefik
TRAEFIK_IMAGE=traefik:v3.7.19 go test -race ./traefik -run TestTraefikPlugin -v -count=1
```

The runtime test starts real Traefik with a local plugin and a mock controller, verifies DNS creation and backend routing, then removes its container. It never contacts a real controller. `.github/workflows/traefik-plugin.yml` runs it in CI.

## Publish a release and the plugin catalog

Maintainer steps. Run them in the repository; documentation changes do not publish anything.

1. Keep `github.com/j0sh3rs/bollard` public and non-forked, with `.traefik.yml`, `go.mod`, and the MIT `LICENSE` at the root.
2. Keep manifest `type: provider` and `import: github.com/j0sh3rs/bollard/traefik`. The module stays `github.com/j0sh3rs/bollard`; the root `main` package is the separate standalone controller.
3. Add the GitHub topic `traefik-plugin`.
4. Run the native tests and the local-plugin smoke test above against the target Traefik version. Native tests alone do not prove Yaegi interpreter compatibility.
5. Merge through release-please and publish the tag with `.github/workflows/release-please.yml`. Never reuse an older tag, and never hand-edit release metadata to claim a release.
6. Check the catalog's indexing and validation result, then pin deployments to that tag.

A merged change is **not** a release, and a release is **not** catalog acceptance. Until step 6, run unreleased code only through the local overlay. The plugin imports only the standard library, `internal/dnsname`, and `internal/unifi`; the Docker controller's dependencies stay in the module but are not imported by the plugin.

Manifest `testData` uses unreachable loopback port 1 and a dummy key. Initialization does no network I/O, so the catalog check passes while a running worker would log the unavailable endpoint.
