# Design: external-dns webhook provider

Status: proposal, not implemented. Roadmap items: external-dns webhook provider (`P1`), Kubernetes services on UniFi DNS (`P1` use case), Kubernetes CRD controller (`P2`, separate repository).

## Goal

Let external-dns publish Kubernetes `Service`/`Ingress` hostnames to UniFi through bollard's UniFi client, instead of a third-party UniFi webhook with its own client code.

## Shape

A new mode of the existing binary, `bollard webhook`, serving the external-dns [webhook provider protocol](https://kubernetes-sigs.github.io/external-dns/latest/docs/tutorials/webhook-provider/) over HTTP. It runs as the sidecar external-dns documents, on `127.0.0.1:8888` (the API port, which external-dns calls) and a separate health/metrics port. It reuses `internal/unifi` and `internal/dnsname`; it does not use the Docker watcher, the reconciler or the ownership store.

| Webhook call | Behaviour |
|---|---|
| `GET /` (negotiation) | Returns the configured `domainFilter` as external-dns expects |
| `GET /records` | Lists UniFi records (`IncludeAllRecords`), keeps A, AAAA, CNAME (and TXT, see below) inside the domain filter, and maps each to an external-dns endpoint (`dnsName`, `recordType`, `targets`, `recordTTL`) |
| `POST /records` | Applies the change set: `Create` → `CreateRecord`, `UpdateOld`/`UpdateNew` → `UpdateRecord`, `Delete` → `DeleteRecord`, resolving IDs from a fresh listing by `(name, type)` |
| `POST /adjustendpoints` | Normalises endpoints UniFi cannot hold (drops unsupported types and multi-target sets, folds names) so external-dns does not see perpetual diffs |

UniFi holds one value per record, so an endpoint with several targets becomes several records at one name only if UniFi allows it. Initial scope refuses multi-target endpoints in `/adjustendpoints` with a log line rather than guessing.

## Ownership: the part that needs a decision

external-dns tracks ownership itself with a **registry**. The default `txt` registry writes a companion `TXT` record next to every record it manages and refuses to touch records without one. That is a good fit here: bollard stays stateless, and the ownership evidence lives in UniFi next to the record.

Today `internal/unifi` supports A, AAAA and CNAME only. The modern API has `TXT_RECORD` (a `text` field; it rejects `ttlSeconds`, unlike A, AAAA and CNAME), the legacy API has `record_type: "TXT"`. So the options are:

1. **Add TXT support to `internal/unifi`** (recommended). Smallest surface, uses the registry as designed, and keeps the daemon's rule that bollard never deletes what it cannot prove it owns.
2. Run external-dns with `--registry=noop --policy=upsert-only`. No new record type, but external-dns then owns nothing and can overwrite records created by hand or by the daemon. Not acceptable as a default.
3. Keep ownership in the bollard store. Contradicts the stateless sidecar model and duplicates the registry.

Decision needed: option 1. It also benefits anyone who wants TXT records for other reasons, and the wire format is already verified against the same reference used for AAAA and CNAME.

## Interaction with the daemon

A zone must have one writer. Names managed through external-dns must not also be labelled on Docker containers; the daemon's ownership rows will not know about external-dns records and will refuse to create a name the webhook already holds (an occupied name), which is the correct failure. Document the split by domain filter.

## Configuration

`UNIFI_*` as the daemon, plus `WEBHOOK_ADDR` (default `127.0.0.1:8888`), `DOMAIN_FILTER` (required, comma-separated suffixes: refusing to run without one avoids managing the whole zone by accident), and the existing `STOP_DRAIN`-style options do not apply. Reserved names are enforced as in the daemon.

## Test plan

- Table tests for the endpoint ⇄ `DNSRecord` mapping, including trailing dots, case and unsupported types.
- A simulated UniFi server and a recorded external-dns call sequence (negotiate, list, apply create/update/delete, adjust).
- Domain filter: records outside it are invisible to `GET /records` and rejected by `POST /records`.
- Run external-dns against the sidecar in a kind cluster in CI, once, as the compatibility check.

## Out of scope

The CRD controller (`BollardDNSRecord`) is a different product: leader election, a controller-runtime dependency and its own repository. It should build on this webhook's mapping code, not duplicate it.
