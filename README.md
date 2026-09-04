# broadcast

A Kubernetes CRD and proxy that fans one HTTP request out to *every* ready
endpoint of a Service, instead of load-balancing it to one.

```text
                    ┌──► Pod A
HTTP client ──► /broadcast/{name} ──┼──► Pod B
                    └──► Pod C
```

It exists for cache invalidation and similar "your local copy is stale" hints,
where core Kubernetes gives you no fan-out primitive and standing up
Kafka/NATS/RabbitMQ is a lot of infrastructure for a message that is allowed to
be lost. Delivery is deliberately UDP-like: no acknowledgement, no retry, no
ordering, no persistence. If you need any of those, use a real queue.

## Install

```bash
helm install broadcast oci://ghcr.io/goxang/charts/broadcast \
  --namespace goxang-broadcast-system --create-namespace
```

Or with raw manifests:

```bash
kubectl apply -f config/crd/
kubectl create namespace goxang-broadcast-system
kubectl apply -f config/rbac/ -f config/manager/ -n goxang-broadcast-system
```

v1alpha1 is single-namespace: the controller watches `Broadcast`s in the
namespace it runs in, and the proxy resolves `/broadcast/{name}` against that
same namespace. Cluster-wide routing is not supported yet.

## Usage

Point a `Broadcast` at an existing Service:

```yaml
apiVersion: networking.goxang.io/v1alpha1
kind: Broadcast
metadata:
  name: cache-invalidation
spec:
  service:
    name: my-service
    targetPort: 8080
  timeout: 50ms
```

Then send it a request:

```bash
# service is "broadcast" with the raw manifests, "<release>-broadcast" via Helm
curl -X POST http://broadcast:8080/broadcast/cache-invalidation/invalidate \
  -H 'Content-Type: application/json' -d '{"key":"user:42"}'
```

Every ready endpoint of `my-service` receives `POST /invalidate` on port 8080
with the same method, path suffix, query string, headers, and body. Hop-by-hop
headers (`Connection`, `Keep-Alive`, `Transfer-Encoding`, `Upgrade`, `Host`, …)
are stripped; each target sees its own `Host`.

The caller gets one summary, not N responses:

```json
{
  "broadcast": "cache-invalidation",
  "targets": 3,
  "dispatched": 3,
  "responses": 3,
  "errors": 0,
  "timed_out": false,
  "duration_ms": 4,
  "statuses": {"200": 3}
}
```

## Spec

| Field | Default | Meaning |
|---|---|---|
| `spec.service.name` | — | Existing Service in the same namespace |
| `spec.service.targetPort` | — | Endpoint (pod) port, not the Service port |
| `spec.protocol` | `HTTP` | Only `HTTP` in v1alpha1 |
| `spec.timeout` | `1s` | Budget for the entire fan-out |
| `spec.concurrency` | `16` | Max in-flight target requests, 1–1024 |

`targetPort` is matched against the EndpointSlice endpoint port, exactly like a
Service's integer `targetPort`, so it stays unambiguous when a Service maps its
`port` to a different `targetPort`. Named ports are not resolved. If the Service
sets no `targetPort`, use the Service port number.

Status reports `endpoints` (ready endpoints currently resolved) and a `Ready`
condition, true once at least one endpoint resolves. `kubectl get broadcasts`
prints service, target port, timeout, endpoint count, and age.

## Response semantics

| Code | Meaning |
|---|---|
| `202 Accepted` | Dispatched to ≥1 target. Body is the summary above. |
| `400 Bad Request` | Path is not `/broadcast/{name}[/{path}]`. |
| `404 Not Found` | No `Broadcast` by that name in the namespace. |
| `413 Request Entity Too Large` | Body over the limit (1 MiB by default). |
| `503 Service Unavailable` | Broadcast exists but has zero ready endpoints. |

`202`, never `200`, because the proxy can confirm that it dispatched and nothing
more. It does wait up to `spec.timeout` for target responses, but only to fill
in an honest summary — a target's `200` is its HTTP server accepting the
request, not an application-level ack. When the budget runs out, in-flight
requests are cancelled, targets not yet dispatched are skipped, `timed_out` is
set, and the caller still gets `202`. Individual target failures land in
`errors` and never fail the caller. Nothing is ever retried.

Your application's correctness must not depend on a broadcast arriving. A pod
that starts after the fan-out, or is unavailable during it, simply misses it.

## How it works

One binary, two halves, joined by an in-memory store:

- **`pkg/controller`** watches `Broadcast`, `Service`, and `EndpointSlice` via
  informers, resolves the ready non-terminating endpoints for each Broadcast,
  writes them to the resolver, and updates `Broadcast.status`.
- **`pkg/resolver`** is copy-on-write: writes build a fresh snapshot and publish
  it with one `atomic.Pointer` swap, so reads are lock- and allocation-free.
- **`pkg/proxy`** serves `/broadcast/`, reads the resolver, and fans out over a
  pooled `http.Transport` (`MaxIdleConnsPerHost=8`) with a semaphore bounded by
  `spec.concurrency`.

The request path makes **zero Kubernetes API calls** — endpoint churn is picked
up by informers, off the hot path. The two halves talk through the
`resolver.Resolver` interface, so they can be split into separate deployments
later without either side changing.

Replicas are fully independent: each pod runs its own informers, resolver, and
proxy, with no shared state and no leader election. Scale out by raising
`replicaCount`; the Service load-balances callers across replicas. Replica
endpoint views can differ briefly during watch propagation, which is consistent
with best-effort delivery. Status writes are compare-before-write and
idempotent, so concurrent reconcilers are safe.

See [docs/architecture.md](docs/architecture.md) for the longer version.

## Operating it

Flags: `--namespace` (defaults to `POD_NAMESPACE`), `--listen-addr` (`:8080`),
`--workers` (2), `--resync-period` (10m), `--max-body-bytes` (1 MiB),
`--kubeconfig`, `--log-json`.

The listener serves `/broadcast/`, `/healthz`, `/readyz` (ready once informer
caches sync), and `/metrics`. Prometheus metrics: `goxang_broadcast_requests_total`,
`goxang_broadcast_target_requests_total`, `goxang_broadcast_fanout_duration_seconds`,
`goxang_broadcast_targets_total`.

RBAC is a namespace-scoped Role: `get/list/watch` on broadcasts, services, and
endpointslices, plus `update/patch` on `broadcasts/status`. The pod runs
non-root with a read-only root filesystem, `RuntimeDefault` seccomp, and all
capabilities dropped. Chart defaults request 10m CPU / 32Mi memory and cap at
200m / 128Mi.

## Limitations

- HTTP/1.1 only; `spec.protocol` is reserved for HTTP/2 and gRPC later.
- Single namespace, no cross-namespace Service references.
- Request bodies are buffered in memory and capped at 1 MiB.
- No TLS to targets — pod-to-pod plaintext HTTP is assumed.
- No queue, ack, ordering, replay, or DLQ. This is not a messaging system.

## Development

```bash
make verify        # fmt, vet, test, test -race
make build         # static binary at bin/broadcast
make docker-build
make helm-lint
make e2e           # functional suite against a throwaway kind cluster
```

Unit tests cover endpoint resolution, the resolver, proxy fan-out, and the
timeout/concurrency bounds. `test/e2e/run.sh` builds the images, installs the
chart into a `kind` cluster, and exercises basic fan-out, scaling, pod removal,
slow and failing targets, endpoint churn, and Service independence.

## License

[Apache-2.0](LICENSE)
