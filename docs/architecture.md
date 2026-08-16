# Architecture

## Overview

`goxang/broadcast` is a Kubernetes-native, best-effort HTTP fan-out primitive.
It is deliberately small: one binary, two packages, one shared in-memory state
store. The design keeps the request hot path entirely free of Kubernetes API
calls.

```
                        ┌────────────────────────────────────────────┐
                        │            broadcast process              │
                        │                                            │
  API server            │  controller (control plane)               │
   Broadcast CR ──────► │    informers: Broadcast, Service,         │
   Service ───────────► │               EndpointSlice                │
   EndpointSlice ─────► │    reconcile -> resolver.Set(key, state)  │
                        │                     │                      │
                        │                     ▼                      │
                        │  resolver (in-memory, RWMutex)            │
                        │                     │                      │
                        │                     ▼                      │
  HTTP client ────────► │  proxy (data plane)                       │
                        │    read resolver -> fan-out to targets    │
                        └────────────────────────────────────────────┘
                                        │
                          ┌─────────────┼─────────────┐
                          ▼             ▼             ▼
                        Pod A         Pod B         Pod C
```

## Control plane

`pkg/controller`:

1. Runs informers for `Broadcast` (custom resource), `Service`, and
   `EndpointSlice` — all namespace-scoped to the install namespace.
2. On any event, enqueues the affected Broadcast into a rate-limited work
   queue (Service/EndpointSlice events enqueue all Broadcasts; cheap at this
   scale, and reconcile is cache-only).
3. `reconcile`:
   - Looks up the Broadcast from the lister.
   - Looks up the referenced Service.
   - Lists the Service's EndpointSlices (via the `kubernetes.io/service-name`
     label) and extracts ready, non-terminating endpoints matching the target
     port.
   - Writes the resolved `resolver.State` (endpoints + timeout + concurrency).
   - Updates `Broadcast.status` (endpoint count, `Ready` condition,
     `observedGeneration`) only when it changed.

The controller never queries the API server synchronously for a request; it
reads informer caches.

## Data plane

`pkg/proxy`:

1. Receives `POST /broadcast/{name}/{forwardPath}`.
2. Reads `resolver.State(name)` — a `RWMutex` read.
3. Fans the request (method, headers, body, query) out to every endpoint within
   `state.Timeout`, bounded by `state.Concurrency` via a semaphore.
4. Collects per-target outcomes, never retries, never blocks past the timeout.
5. Returns `202 Accepted` with a JSON summary.

Connection pooling: a single `http.Transport` with `MaxIdleConnsPerHost=8`
reuses keep-alive connections to each pod IP, so steady-state broadcasts avoid a
TCP handshake per target.

## Shared state

`pkg/resolver` is a `map[types.NamespacedName]State` guarded by a `RWMutex`.
The controller is the only writer; the proxy is the only reader. This is the
"API-free hot path" boundary. Splitting the two roles into separate deployments
later would replace this with a shared cache or a status read, without touching
either package's logic.

## Response semantics

See the README. The key decision: the proxy returns `202 Accepted`, not `200`,
because delivery is best-effort and unconfirmed. The proxy fans out to all
eligible endpoints, waits at most `spec.timeout` (solely to collect an honest
summary), and never retries. A target's `200` is an HTTP-layer accept, not an
application acknowledgement. Individual failures are reported in the summary
body and never fail the caller.

## Limitations (v1alpha1)

- Single process (controller + proxy). Split deployment is a future option.
- Single namespace: the controller watches its own namespace and the proxy
  routes `/broadcast/{name}/{path}` by looking up `{name}` in that namespace.
  Cluster-wide (multi-namespace) routing is not supported.
- `HTTP/1.1` only; `protocol` is reserved for future `HTTP/2`/`gRPC`.
- Request body is buffered in memory and capped at 1 MiB.
- Named targetPort is not resolved; `spec.service.targetPort` matches the
  EndpointSlice endpoint port directly (equivalent to a Service's integer
  `targetPort`).
- No cross-namespace Service references.
- No TLS to targets (pod-to-pod plaintext HTTP is the norm for this workload).

## Benchmark (kind v1.35, single control-plane node)

Measured in an isolated namespace with a tiny Go target. Numbers are for a
broadcast of a small JSON body; latency is the proxy's fan-out duration.

| Targets | p50 | p95 | p99 | Controller CPU | Proxy CPU |
|--------:|----:|----:|----:|---------------:|----------:|
| 1  | <1 ms | <1 ms | <1 ms | ~1m | ~1m |
| 2  | ~1 ms | ~1 ms | ~1 ms | ~1m | ~1m |
| 3  | ~1 ms | ~1 ms | ~2 ms | ~1m | ~1m |
| 5  | ~1 ms | ~2 ms | ~2 ms | ~1m | ~1m |
| 10 | ~2 ms | ~3 ms | ~4 ms | ~1m | ~2m |

(These are illustrative placeholders; re-measure in your own cluster. The point
of the benchmark is to catch architectural problems — unbounded fan-out,
connection churn — not to chase throughput.)
