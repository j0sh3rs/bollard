# Bollard Release Hardening Implementation Plan

> **For agentic workers:** Use the executing-plans or subagent-driven-development workflow. Check each slice with its behavioral tests before publishing.

**Goal:** Ship plans 001–005 and their operational follow-ups in one PR to `main`.

**Architecture:** Keep Docker ownership in the store and keep the Traefik plugin create-only. Add checks at DNS and HTTP boundaries, and prove the shipped daemon image against an isolated Docker/UniFi fixture. This document is the release specification.

**Tech stack:** Go, SQLite/Postgres, Prometheus, Docker, GitHub Actions, release-please.

## Constraints and scope

- Base: GitHub `main`; branch from local `13ae92b`, which already contains 001–005. Do not duplicate those commits. Do not include staged dependency bumps or local plans. Open one PR; do not merge, tag, or deploy.
- Do not delete or adopt a DNS record without ownership evidence. Records with no ownership row cannot be safely distinguished from manual records. Keep their recovery operator-controlled and document the safe procedure.
- Keep the Traefik plugin dependency-light and create-only. Serialize discovery-to-write transactions for instances in one loaded runtime using the same controller hostname. Traefik gives distinct aliases separate interpreters. Across aliases and processes, require one writer per site/domain scope unless UniFi itself enforces uniqueness; no atomic create-if-absent is known.
- Maintain existing daemon TLS and metrics listen defaults for compatibility. Clearly distinguish insecure daemon TLS from secure plugin TLS and document the working loopback metrics configuration. Leave unrequested DNS record types and unrelated roadmap features out.
- Do not add a speculative legacy DNS response cache. Provider selection is cached for the process lifetime; document restart on controller API upgrades and retain the existing bounded transport lifecycle.

## Review focus

- Occupied mixed-case or trailing-dot names, disabled A and non-A policies; skip creation, and never adopt unless the existing record is a unique eligible match.
- Controller temporarily unreachable versus bad credentials or certificate; retry only the former and applicable status codes, and honor SIGTERM during backoff.
- Health probes on wildcard IPv4/IPv6 and explicit binds; no proxy or redirect, short timeout, response body closed.
- Concurrent replicas and unowned records: no overwrite or guessed delete; cap guarantees to the scope actually coordinated.
- Image smoke uses only isolated fixtures and labels, cleans up by ID, and tests failure during lifecycle as well as normal registration/removal where practical.

## Tasks

### 1. Daemon probe and startup

- [x] Implement and exercise `checkHealth(addr string) error` against actual IPv4, IPv6, and wildcard listeners. Reject bad addresses, redirects, and non-200; use a three-second client without a proxy. Keep `:9090` when unset and document HTTP liveness semantics.
- [x] Retry transient `unifi.New` failures with cancelable, capped backoff. Retry network timeout/refusal/reset, temporary DNS, HTTP 429 and applicable 5xx; do not retry configuration, site, authentication, or TLS verification failures. Test cancellation and 403. Close the server and idle UniFi connections and reset `bollard_up` on shutdown.

### 2. Metrics

- [x] Report method, status, and duration per UniFi HTTP attempt, including retries, through optional callbacks. Record provider selection on success. Verify HTTP counters, duration, and active-provider metrics from the built image.
- [x] Assert `HostnameConflictsTotal` increments exactly once on the `Adopt` conflict path without the global registry. Update monitoring reference.

### 3. DNS and ownership

- [x] Share ASCII DNS syntax and canonicalization between Docker labels and Traefik rules. Compare historical store rows and UniFi records canonically without a migration.
- [x] List all named policies for occupancy. Refuse a normal start at an occupied name and adopt only a single matching enabled A record with a nonempty ID. Reconcile uses one listing per tick and keeps names reserved after uncertain mutations.
- [x] Preserve records without a store row, reject missing IDs at the provider boundary, and retain 003 retry and 004 one-owner regressions. Document verified manual cleanup after state loss.

### 4. Plugin concurrency

- [x] Serialize writes within one loaded plugin runtime and controller hostname, including cancellation. Verify concurrent providers issue one POST and preserve simulated remote-server boundary tests. Document one registered writer and stop-before-start rollouts.
- [x] Verify site `internalReference`, occupied-name handling, and reconcile coverage already shipped with v0.7.1. Correct stale release notes.

### 5. Delivery and docs

- [x] Build the Docker image in PR CI and exercise it against an isolated Docker daemon and fake UniFi. Verify probe recovery, health, create, stop cleanup, and telemetry. Run the real Traefik container smoke for Yaegi compatibility.
- [x] Set `-version` from the release tag in binaries and images. Verify exact image output and leave publishing to release-please.
- [x] Display literal `***` only for redacted Postgres credentials. Test authority, query credentials, encoded keys, repeated keys, and preservation of unrelated fields.
- [x] Update public docs, the changelog, and the local TLS plan note. Document restarting after a UniFi controller API upgrade; do not introduce a legacy response cache.

### 6. Ship

Run `go test -race ./...` with a real Postgres service, `go build ./...`, the Docker image and Traefik smoke tests, and workflow syntax checks. Review the complete diff against remote `main` and confirm it includes the ten existing commits.

Commit only the release work, push the isolated branch, and open one PR with verification results and ownership/concurrency limits.
