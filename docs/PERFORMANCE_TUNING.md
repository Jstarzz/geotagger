# GeoTagger Performance Tuning Guide

## Summary

The first deployed load test showed that GeoTagger's current bottleneck is **CPU contention on the single 9-vCPU VM**, not the GeoLite2 lookup and not HDD throughput.

Measured observations from the first public-path test:

```text
local MMDB lookup for verified request: 56 microseconds
HDD utilization:                     below roughly 3%
comfortable current range:           roughly 500-1,000 RPS
higher burst achieved:               roughly 1,373 RPS
```

The 500-1,000 RPS figure is an operational estimate for the tested topology, not a hard application ceiling. The load generator ran on the same VM as the target and traffic traversed the public Cloudflare route, so the test consumed target CPU and included WAN/edge latency.

## Priority order

For this deployment, performance work should be done in this order:

1. **Measure the origin correctly** with an external load generator.
2. **Tune HPA/CPU scheduling** so replica count follows real CPU pressure.
3. **Separate public-network latency from application latency** by benchmarking both the internal Service path and public Cloudflare path.
4. **Add capacity or newer hardware** if the CPU ceiling is still below the required SLO.
5. Split stateful services across nodes only when measurements show shared CPU is limiting them.
6. Consider code micro-optimizations only after the above, because the MMDB lookup itself is already very fast.

## Applied optimization: HPA CPU-request tuning

The original API resource request was:

```yaml
requests:
  cpu: 250m
```

with:

```yaml
averageUtilization: 65
```

Kubernetes calculates HPA CPU utilization against the **CPU request**, not the CPU limit. Therefore a 65% target against `250m` corresponds to only:

```text
250m * 0.65 = 162.5m CPU per Pod
```

At steady state, the HPA could request eight replicas when aggregate API CPU was only around:

```text
8 * 162.5m = 1.3 CPU cores
```

On a 9-vCPU single-node system, that can create unnecessary Pod/process scheduling and context-switch overhead long before the VM is truly CPU-constrained.

The tuned manifest uses:

```yaml
replicas: 2
resources:
  requests:
    cpu: 750m
  limits:
    cpu: "2"

hpa:
  minReplicas: 2
  maxReplicas: 8
  averageUtilization: 65
```

The new HPA target is approximately:

```text
750m * 0.65 = 487.5m CPU per Pod
```

and eight replicas correspond to roughly:

```text
8 * 487.5m = 3.9 aggregate API CPU cores at target
```

This leaves more room for NATS, ClickHouse, the audit worker, cloudflared and K3s while avoiding the previous very-early scale-out signal.

Two minimum API replicas also reduce sensitivity to sudden bursts and rolling updates without materially changing the stateful architecture.

**This tuning still requires a fresh external-generator benchmark.** It is based on Kubernetes HPA mechanics and the known 9-vCPU node budget, not a claim that the new values are globally optimal.

## Applied optimization: better latency instrumentation

The API now exports three separate latency histograms:

```text
geotagger_lookup_latency_microseconds
geotagger_audit_publish_latency_microseconds
geotagger_request_latency_microseconds
```

These answer different questions:

```text
lookup latency
    = local MMDB work only

audit publish latency
    = synchronous JetStream durable-ACK wait

request latency
    = origin-side handler time, including auth, validation, lookup and audit publish
```

Comparing them lets operators determine whether time is being spent in MMDB, JetStream, or elsewhere in the handler before making changes.

Public k6 latency also contains network/Cloudflare time, so it should not be treated as the same metric as origin request latency.

## Applied optimization: load-test correctness

The original k6 script counted only HTTP 200 as success. Three of twelve valid public fixture addresses returned a legitimate `404 country not found` for the installed GeoLite2 snapshot, creating an artificial ~25% failure baseline.

The updated load script treats both 200 and 404 as expected application responses while still failing on unexpected 4xx/5xx responses.

This does not make the API faster; it makes the performance data usable.

## Applied optimization: audit-worker allocation reduction

The ClickHouse worker now:

- reuses the `[][]byte` row slice across flushes; and
- pre-sizes the NDJSON buffer before writing a batch.

This reduces allocation/copy churn in the asynchronous worker. It is a small optimization and is not expected to change the API's primary throughput ceiling by itself.

## Redis: not recommended

GeoTagger does **not** need Redis for the current lookup path.

The local MaxMind MMDB is already memory-mapped and the verified lookup took only 56 microseconds. Adding Redis would introduce:

```text
API
 -> network/socket hop
 -> Redis command parsing
 -> key serialization
 -> Redis lookup
 -> response parsing
```

for data that is already available through a local memory-mapped radix-tree lookup.

Redis would also introduce:

- another stateful service;
- another credential/security boundary;
- cache invalidation on MMDB refresh;
- additional memory usage;
- another availability dependency; and
- potential retention of queried IPs or derived keys.

For GeoTagger, Redis is likely slower than the lookup it would replace.

### In-process result cache

An in-process LRU cache is also not recommended by default.

Reasons:

1. MMDB lookup cost is already tiny.
2. Real production IP traffic may have low reuse compared with the 12-address benchmark fixture.
3. A cache adds invalidation requirements when the MMDB is replaced.
4. Storing raw queried IPs in cache expands the privacy/data-handling surface.
5. Per-Pod caches duplicate memory and produce inconsistent warm/cold behavior.

A cache should be considered only if an external benchmark with realistic traffic proves MMDB lookup CPU is material. Current evidence says it is not.

## Do not weaken audit durability for speed

These changes would make benchmark numbers look better but violate the service's stated audit guarantee:

```text
DO NOT switch JetStream to memory-only storage for benchmark speed
DO NOT remove the publish ACK wait
DO NOT change MaxAge=0 to age-based expiry
DO NOT use DiscardOld to hide queue pressure
DO NOT return success before required audit acceptance
```

The durability cost is intentional. If the product requirement changes, that is an architecture/policy decision, not a performance tweak.

## Major optimization: external load generator

The original k6 process competed with the application stack for the same nine vCPUs.

A clean benchmark should run k6 on another machine with enough CPU and network capacity.

Test both:

```text
A. public path
load generator -> Cloudflare -> cloudflared -> Service -> API

B. internal/origin path
load generator on trusted LAN -> internal endpoint -> Service/API
```

The difference between A and B estimates public-edge/network overhead.

Recommended stages:

```text
100 RPS
250 RPS
500 RPS
750 RPS
1,000 RPS
1,500 RPS
2,000 RPS
then increase only while the SLO remains acceptable
```

Capture at each stage:

- offered RPS;
- achieved RPS;
- 200/404/4xx/5xx distribution;
- p50/p95/p99 public latency;
- origin `geotagger_request_latency_microseconds`;
- `geotagger_audit_publish_latency_microseconds`;
- MMDB latency;
- HPA desired/current replicas;
- per-Pod CPU/RAM;
- VM CPU steal/load/saturation;
- NATS stream pending/bytes/redelivery;
- worker lag/batch behavior;
- ClickHouse insert latency/errors;
- disk `await`, queue depth and utilization; and
- network throughput/RTT.

## Major optimization: avoid the public edge for internal callers

If the primary callers are services on the same trusted NDHIS/on-prem network, the lowest-latency architecture is not to send every internal request to the public Internet and back through Cloudflare.

A future design can provide two ingress paths:

```text
external/remote caller
    -> Cloudflare Tunnel
    -> API Service

trusted internal caller
    -> internal TLS endpoint
    -> API Service
```

The internal path must still use authentication, TLS, access controls and appropriate audit controls. It should not be implemented as an unauthenticated NodePort shortcut.

This can remove WAN/Cloudflare round-trip latency for internal clients without changing the core API.

## Major optimization: newer CPU or additional nodes

The current host uses Westmere-generation Xeons. The first test already showed CPU pressure before disk pressure.

A modern server CPU would provide substantially higher per-core performance, better memory bandwidth and newer instruction-set support. It would also remove the ClickHouse x86-64-v2 compatibility constraint.

If the workload requirement exceeds one VM:

```text
Node A: API replicas
Node B: API replicas + worker
Node C: NATS / ClickHouse or additional API capacity
```

is more useful than adding Redis to the existing node.

Do not split services simply because Kubernetes permits it. Split them when metrics show contention or when availability requirements require separate failure domains.

## Admission control / load shedding

The high-load tests showed latency collapse at offered rates far above sustainable throughput. A future hardening step is explicit admission control so overload returns a fast `429` or `503` rather than allowing unbounded in-flight work to create multi-second tail latency.

Possible implementation layers:

- Cloudflare rate limiting for untrusted/public callers;
- per-caller quotas at the API layer;
- bounded origin concurrency with `Retry-After`; and
- capacity-aware SLO alerts.

Do not choose the concurrency ceiling by guesswork. Use the new origin/audit latency metrics and external load test to find the knee of the latency curve first.

## Cloudflare connector scaling

One cloudflared Pod is sufficient unless metrics show the connector is CPU-bound or tunnel throughput is limiting. A second connector on the same node improves process redundancy but does not add hardware capacity.

Do not add connector replicas just to increase a replica count.

## NATS tuning

NATS was not the observed bottleneck. Keep the durable configuration unchanged.

Only tune NATS if the new metrics show audit publish latency increasing materially while API CPU remains available. Candidate investigation points:

- JetStream fsync/storage latency;
- message size;
- NATS CPU;
- publish ACK latency;
- reconnects/timeouts; and
- PVC/storage behavior.

Moving JetStream to faster storage can help if durable fsync becomes limiting, but the first benchmark did not show that condition.

## ClickHouse tuning

ClickHouse is asynchronous and was not the observed request-path bottleneck. Tune it when worker backlog/insert latency grows.

Potential future changes:

- larger or adaptive batch sizes;
- native ClickHouse protocol and compression;
- dedicated CPU/storage;
- schema/order-key review against actual audit queries; and
- partition/TTL maintenance monitoring.

Do not put ClickHouse directly into the synchronous request path.

## Go/API micro-optimizations

The following are lower priority than CPU topology, HPA behavior and network path:

- reducing JSON allocations;
- pooling HMAC state;
- specialized response encoding;
- removing tiny lock operations around MMDB metadata; and
- request-ID allocation tuning.

These changes should require a benchmark showing a measurable benefit. At a 56-microsecond MMDB lookup and much larger public/request latency, premature micro-optimization is unlikely to move the system-level result significantly.

## Performance decision table

| Idea | Recommendation | Reason |
|---|---|---|
| Redis lookup cache | No | Local MMDB is already faster/simpler |
| In-process IP cache | Not now | No evidence lookup CPU is material; privacy/invalidation cost |
| Remove NATS durable ACK | No | Breaks audit guarantee |
| NATS memory storage | No | Weakens durability |
| Bigger API HPA CPU request | Applied | Corrects overly aggressive scaling signal |
| Two minimum API Pods | Applied | Better burst/rollout behavior |
| External k6 generator | High priority | Current benchmark consumes target CPU |
| Internal trusted ingress | High value for internal clients | Removes public-network round trip |
| Modern CPU / more nodes | Highest scale lever | CPU is the observed bottleneck |
| Split NATS/ClickHouse immediately | Not yet | They were not the measured bottleneck |
| Load shedding | Next after clean benchmark | Prevents latency collapse under overload |
| SSD for ClickHouse/NATS | Only if storage metrics justify it | HDD stayed below ~3% utilization in first test |

## Retest requirement

No optimization should be called successful solely because it looks correct in configuration or code.

After deploying these changes, repeat the load test from an external generator and compare:

```text
before / after achieved RPS
before / after p95 and p99
origin request latency
JetStream publish latency
API replica count over time
VM CPU utilization/context switching
NATS/worker/ClickHouse backlog
```

Record the new result in `PERFORMANCE.md` and `CHANGELOG.md`.
