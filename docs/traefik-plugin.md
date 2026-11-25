# Traefik provider plugin

Bollard can run inside Traefik as a provider plugin. It polls Traefik's own router API, finds exact positive `Host()` names on enabled HTTP routers, and creates missing UniFi **A** records that point at one configured Traefik IPv4 address. It never supplies routes, never updates a record, and never deletes one.

Choose the plugin when Traefik already fronts your services and you want no Docker socket and no state database. Choose the [standalone Docker controller](../README.md) when records must follow container labels and be removed when containers stop. Never run both for the same names; see [migration](traefik/how-to.md#migrate-from-the-standalone-controller).

## Pick a document

| You want to... | Read |
|---|---|
| Learn by running a complete nginx + Traefik + Bollard stack | [Tutorial](traefik/tutorial.md) |
| Integrate file or Docker routes, secure TLS, migrate, troubleshoot, develop, or publish | [How-to guides](traefik/how-to.md) |
| Look up an exact field, default, validation rule, log line, or release difference | [Reference](traefik/reference.md) |
| Understand why it is stateless and create-only, and what concurrency it does not solve | [Explanation](traefik/explanation.md) |
| Copy a runnable Compose project | [`examples/traefik-plugin/`](examples/traefik-plugin/) |

## Release status

- Tag `v0.7.0` introduced the plugin. Set `unifiSite` to the display name (for example `Default`) or UUID when using that version.
- Tag `v0.7.1` can match the site's `internalReference` and protects every named UniFi record from a duplicate plugin create, regardless of type or enabled state.
- Catalog indexing is separate from a Git tag. A tag existing does not prove Traefik's plugin catalog serves it; use the [local plugin overlay](traefik/how-to.md#develop-with-a-local-plugin) when you cannot depend on the catalog.
- Validated against Traefik `3.7.19`. Traefik plugin loading is experimental; revalidate before changing versions.
- Unreleased source adds serialized writes within one plugin runtime and stricter UniFi response checks. Run it only through the local overlay until a tag includes it; see [release differences](traefik/reference.md#release-differences).

See [release differences](traefik/reference.md#release-differences) for behavior added after each tag.
