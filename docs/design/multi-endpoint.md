# Design: multiple controllers, per-container sites and zones

Status: proposal, not implemented. Roadmap items: multiple UniFi controllers, per-container site override, multiple domains/zones (all `P1`), cross-endpoint collision policy (`P2`).

## Where the code is single-endpoint today

- `main.go` builds one `unifi.Config` from `UNIFI_HOST`/`UNIFI_API_KEY`/`UNIFI_SITE` and one `DNSProvider`. `unifi.New` resolves the site **once** at startup and the provider keeps the site ID.
- `reconciler.Reconciler` holds that one provider. Occupancy checks, orphan cleanup, the empty-list guard counters (`emptyUnifiTicks`) and adoption all assume one zone.
- A store row is `(instance_tag, container_id, hostname)`. Nothing says which controller or site owns the UniFi record behind `unifi_record_id`.
- Reserved names come from the single `UNIFI_HOST`; UniFi metrics carry no endpoint label.

A container's FQDN already works for any domain inside one site: `app.office.arpa` and `app.garage.arpa` are different names. What does not exist is *routing* a record to a different controller or site, and short names expanded per zone.

## Proposal

### Named endpoints

An endpoint is `{name, host, apiKeyFile, site, caCert, skipTLSVerify}`. Environment variables cannot express a list, so endpoints come from a file (`BOLLARD_ENDPOINTS_FILE`, YAML). The existing `UNIFI_*` variables keep working and define an endpoint named `default`; the file adds more. Setting both `UNIFI_HOST` and a file is allowed (the file's `default` entry then wins) so a single-controller deployment never has to change.

```yaml
endpoints:
  - name: house
    host: https://unifi.house.arpa
    apiKeyFile: /run/secrets/house
    site: default
  - name: cabin
    host: https://unifi.cabin.arpa
    apiKeyFile: /run/secrets/cabin
zones:
  - suffix: house.arpa
    endpoint: house
  - suffix: office.arpa
    endpoint: house
    site: office        # same controller, different site
  - suffix: cabin.arpa
    endpoint: cabin
```

### Routing

1. **By zone.** A hostname is routed to the zone with the longest matching suffix. No match falls to the `default` endpoint, which keeps today's behaviour.
2. **By label.** `dns.bollard/endpoint: cabin` overrides the zone for a container; `dns.bollard/site: office` selects a site on that endpoint (the per-container site override). An unknown endpoint or site is an invalid label.
3. **Short names.** With `dns.bollard/zone: office` and a hostname without a dot (`app`), the FQDN is `app.office.arpa`. Hostnames that already contain a dot are used as written, so existing labels are unaffected.

### Providers

The reconciler holds `map[target]DNSProvider` keyed by `(endpoint, site)`. Providers are built lazily on first use and cached; `unifi.New` already takes a site, so the change is the cache, not the client. A controller that is down fails only the containers routed to it, which differs from today's retry-until-up startup: startup must not block on endpoints it may never use. Decision needed: probe all endpoints at startup and warn, or stay lazy.

### Store

Add `endpoint TEXT NOT NULL DEFAULT 'default'` and `site TEXT NOT NULL DEFAULT ''` to `records`, and widen the unique key to `(instance_tag, endpoint, site, container_id, hostname)`. Existing rows take the defaults, so the migration is additive and reversible. Drain, instance tags and recovery already work per row and need no change. `unifi_record_id` is only meaningful together with `(endpoint, site)`; every delete or update must resolve the provider from the row, never from the container's current labels.

### Reconcile

Each tick runs the three passes **per target** with that target's own record list. Two state items become per-target: the empty-list hysteresis counters, and the `names` occupancy index. A failure listing one target skips that target and logs; it must not abort the others or count as a failed tick for the rest. Pass 3 groups specs by target before it lists.

### Collisions across endpoints

Occupancy is per target, so the same name on two endpoints is allowed by default, which is the correct reading for separate networks. A configurable `onCrossEndpointDuplicate: warn|refuse` covers deployments where two controllers serve one split-horizon zone. This is the `P2` collision policy and can ship after routing.

### Observability

`bollard_unifi_*` metrics gain an `endpoint` label (cardinality is the number of endpoints, small and fixed). Add `bollard_endpoint_up{endpoint}` and per-endpoint `bollard_records_active`. Existing dashboards keep working for the `default` endpoint because the label is additive.

## Security

Each key is read from its own file. Reserved names are the union of every endpoint host, so a label cannot capture any controller's name. Logs redact every endpoint key, not just one.

## Test plan

- Fake provider map with two targets: containers route by zone, by `endpoint`, by `site`; unknown route is an invalid label.
- One target down: the other still reconciles, and the rows of the down target are untouched (no orphan cleanup from a failed list).
- Migration upgrade preserves rows and gives them `default`.
- Extend the daemon smoke test with a second simulated controller.

## Rollout

Ship routing and the store columns in one release behind the new file; a deployment without the file behaves exactly as before. Zone short names, the collision policy and metrics labels can follow in separate releases. Roughly: store + providers cache + reconcile per target (large), labels and zones (medium), metrics (small).
