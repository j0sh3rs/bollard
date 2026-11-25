# Explanation: how and why the Traefik plugin behaves this way

## What the plugin is

A Traefik provider normally supplies routing configuration. This one supplies none: it hands Traefik an empty configuration and uses its goroutine to watch Traefik's own router API. Your existing providers (file, Docker, Kubernetes, others) stay the source of truth for routes. Bollard turns the hostnames those routes already declare into DNS.

That design explains most of its limits. It sees only what Traefik's API reports (names, not traffic), and it runs inside Traefik (so it has no database, no Docker socket, and no lifecycle events).

## Why it never updates or deletes

The standalone controller can delete safely because it keeps a state database that records which UniFi record belongs to which container. The plugin keeps nothing. Without a record of what it created, it cannot tell its own records from ones you made by hand, from another tool's, or from another Traefik's. Deleting on router removal would risk removing a record someone else relies on; "updating" a record to the current `address` would silently overwrite a deliberate manual choice.

So the rule is simple: an existing name is never touched. Consequences operators feel:

- Removing a route, stopping Traefik, or changing `address` leaves DNS alone. Stale records are cleaned up by you.
- A record pointing at the wrong IP stays wrong until you fix it. The plugin logs a conflict each poll to remind you.
- A restart never deletes records. Use a [stop-before-start rollout](how-to.md#run-one-dns-writer-with-multiple-replicas) when replacing the writer.

## Why every named record reserves the name

A hostname can belong to a disabled A record, a CNAME, or another type. Since `v0.7.1`, every named record prevents the plugin from creating another record at that name. `v0.7.0` lists only enabled A records. See [release differences](reference.md#release-differences).

## What "concurrent" means here

Each provider polls sequentially. Provider instances sharing one loaded plugin runtime and the same normalized controller hostname also take turns from listing through creating. The guard is not keyed by site, so different names for the same site cannot bypass it. Stopping a provider waiting on the guard cancels the wait.

Traefik loads separately registered plugin aliases in separate interpreters. They do not share the guard, even in one process. Different controller hostnames, separate Traefik processes, and other tools also bypass it. Two writers outside the guard can see the same name as free and both try to create it. What happens next depends on UniFi:

- If UniFi rejects the duplicate (for example with a 409), one record wins and the loser logs a failed create. The plugin never overwrites or retries blindly.
- If UniFi accepts duplicates, both records exist, and the plugin will not delete either.

Both outcomes are covered by tests with a simulated remote writer. Matching target IPs do not prevent duplicates.

Run [one dedicated writer per site and domain scope](how-to.md#run-one-dns-writer-with-multiple-replicas). Any other tool that creates the same names counts as a second writer. The plugin has no cross-process lock or leader election.

## Why migration order matters

The standalone controller deletes a record when a labeled container dies, and re-creates records for running labeled containers during reconciliation. If you remove labeled containers while it runs, it deletes their records. If you stop it first, nothing is deleted: its shutdown is not a delete trigger. Hence the [migration order](how-to.md#migrate-from-the-standalone-controller): stop the controller, deploy the plugin, fix stale records by hand, then remove the old service.

## Where Traefik's API fits

The plugin needs the router list, so Traefik's API must be enabled. The API has no authentication and the plugin's HTTP client sends none, so isolation is the control: the example binds a dedicated entrypoint to `127.0.0.1:8081` inside the container and never publishes that port. Anything else in that network namespace can read router names and rules. Keep the namespace trusted and never move that router to a public entrypoint.

Anyone able to define an eligible Traefik route can cause a DNS record to be created, because discovery follows configured rules, not traffic. Restrict who can write route configuration accordingly.

## Published releases, catalog indexing, and local plugins

Three different things can be "the plugin":

| Source | What it means | Failure mode |
|---|---|---|
| Git tag (`v0.7.0`) | Code exists in the repository history. | None at runtime by itself. A tag without `.traefik.yml` cannot load as a plugin. |
| Plugin catalog | Traefik downloads the tag via the catalog at startup. Indexing is a separate step with its own validation. | The tag is not indexed, the catalog is unreachable, or the manifest is rejected: Traefik cannot load the plugin and fails at startup. |
| Local plugin | Traefik loads source mounted into `/plugins-local/src/...`. | A wrong mount path or a link to the `traefik` subdirectory instead of the repository root: the module is not found at startup. |

A tag does not prove catalog acceptance. This is why the example offers a local overlay: it lets you run unreleased fixes, or test against a mock controller, without trusting the catalog. See the [publishing steps](how-to.md#publish-a-release-and-the-plugin-catalog).

Plugin code runs inside Traefik's Go interpreter, so compatibility is tested with real Traefik (`TestTraefikPlugin`), not only native tests.

## Why configuration errors surface at different times

`New` validates syntax and reads the key file without touching the network, so mistakes fail Traefik's startup immediately. The CA file, the site, and credentials are checked on first use, which happens only when a route with a qualifying `Host()` exists. A deployment with no routes looks healthy even with a wrong CA path. After adding the first route, expect any such errors in the next poll. Use [troubleshooting](how-to.md#troubleshoot) to map each message.

## What this does not do

It does not authenticate or configure DNS clients, replace authoritative DNS, bootstrap DNS for the controller's own address, issue TLS certificates for applications, or manage AAAA/CNAME records.
