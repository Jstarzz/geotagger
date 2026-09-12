# GeoTagger Performance and Capacity Report

## Scope

This report records the first end-to-end load test of the deployed GeoTagger stack on the on-premises server. It measures the complete public request path, not the Go lookup handler in isolation.

Tested path:

```text
k6 on the GeoTagger VM
  -> public HTTPS hostname
  -> Cloudflare edge
  -> Cloudflare Tunnel
  -> K3s Service
  -> GeoTagger API
  -> local MMDB
  -> durable NATS JetStream ACK
  -> response

asynchronous audit persistence:

JetStream -> audit worker -> ClickHouse
```

The same 9-vCPU VM hosted both k6 and the complete K3s stack, so the load generator competed with the target for CPU.

## Historical test result

| Target tier | Achieved throughput | Reported failure rate | p95 | p99 | HDD utilization |
|---:|---:|---:|---:|---:|---:|
| 100 RPS | ~100 req/s | 25.05%* | 190 ms | 203 ms | <1% |
| 1,000 RPS | ~668 req/s, VU-limited | 24.9%* | 2.08 s | 3.81 s | <1% |
| 5,000 RPS | ~1,373 req/s | 75.6% | 14.3 s | 20.3 s | <3% |
| 10,000 RPS | ~322-643 req/s | 26-37%+ | 6.2-8.2 s | much higher | <1% |

`*` The approximately 25% low-tier failure rate was a test-harness artifact, not an infrastructure failure. Three of the twelve valid public fixture IPs returned the legitimate application result `404 country not found` for the installed GeoLite2 snapshot. The historical k6 script counted only HTTP 200 as a passing check.

The affected fixtures observed in that run were:

```text
1.1.1.1
9.9.9.9
2606:4700:4700::1111
```

The load script has since been fixed to treat both 200 and 404 as expected lookup outcomes. Unexpected 4xx/5xx responses still fail the test.

## Observed bottleneck

CPU contention appeared before storage contention.

During the test:

- HDD utilization remained below 3%;
- NATS durable publishing did not appear to be the limiting resource;
- ClickHouse writes did not saturate the disk;
- queued audit work drained after every tier; and
- audit durability settings were not weakened for the benchmark.

The VM was running all of the following at once:

- k6;
- K3s/control-plane processes;
- API Pods;
- NATS JetStream;
- the audit worker;
- ClickHouse; and
- cloudflared.

At offered load above the sustainable range, CPU contention drove large tail-latency increases.

## Current capacity statement

For the exact topology used in the historical test, the current defensible operational estimate is:

```text
roughly 500-1,000 requests/second before tail latency becomes poor
```

This is not a clean server-side ceiling. The 1,000-RPS tier was already VU/load-generator constrained and showed multi-second tail latency. The 5,000-RPS tier briefly achieved about 1,373 requests/second, but with p95 around 14.3 seconds; that is not suitable for a low-latency SLO.

A new benchmark is required after the HPA/observability changes described below.

## Post-test performance changes

The following changes were made after the historical test:

### HPA tuning

Original API CPU request:

```text
250m
```

With a 65% HPA target, that meant the scaling target was only 162.5m CPU per Pod. Eight Pods corresponded to roughly 1.3 aggregate API cores at target, so the HPA could scale out far earlier than the 9-vCPU node required.

The tuned deployment uses:

```text
minimum replicas: 2
maximum replicas: 8
CPU request:      750m per API Pod
CPU limit:        2 per API Pod
HPA target:       65%
```

This raises the per-Pod target to about 487.5m CPU and reduces unnecessary process/Pod proliferation at modest load while retaining scale-out capacity.

### Additional origin metrics

The API now exposes:

```text
geotagger_lookup_latency_microseconds
geotagger_audit_publish_latency_microseconds
geotagger_request_latency_microseconds
```

These separate local MMDB cost, synchronous JetStream durable-ACK cost, and total origin handler cost.

### Load-harness correction

HTTP 404 `country not found` for a valid public IP is now treated as an expected application result. This removes the historical false ~25% failure baseline.

### Worker allocation reduction

The audit worker now reuses the batch row slice and pre-sizes ClickHouse NDJSON buffers. This is a small asynchronous-path optimization rather than a primary request-throughput change.

## Why the first run does not establish a 10k-RPS limit

The first run cannot cleanly prove whether the server-side stack can or cannot reach 10,000 RPS because:

1. k6 consumed the same VM CPU as the target;
2. traffic traversed the public Cloudflare/WAN path;
3. VU availability limited at least one tier;
4. the historical fixture check counted valid 404 outcomes as failures; and
5. the benchmark preserved the full durable-audit contract rather than testing lookup-only code.

The result is useful as a deployed-path stress test, but not as an isolated maximum throughput figure.

## Clean benchmark plan

Run k6 from a separate machine with enough CPU and network capacity. Preserve production durability settings.

Suggested stages:

```text
100 RPS
250 RPS
500 RPS
750 RPS
1,000 RPS
1,500 RPS
2,000 RPS
then increase only while latency/error SLOs remain acceptable
```

Collect at each stage:

- offered and achieved RPS;
- HTTP result distribution;
- public p50/p95/p99 latency;
- origin request-latency histogram;
- JetStream publish-ACK latency histogram;
- MMDB lookup latency;
- HPA desired/current replicas;
- per-Pod CPU and RAM;
- VM CPU saturation/load/context switching;
- JetStream pending messages/bytes/redelivery;
- worker throughput/backlog;
- ClickHouse insert rate/errors;
- disk `await`, queue depth, utilization and throughput; and
- public versus internal network RTT.

Run two paths:

```text
A. public path
external generator -> Cloudflare -> tunnel -> Service -> API

B. internal/origin path
trusted generator -> internal endpoint -> Service/API
```

The difference between A and B helps separate public-network latency from application latency.

## Verified lookup evidence

A production lookup for `8.8.8.8` returned request ID:

```text
c75bb0a033c44dd87a9969df059dd6b4
```

The matching ClickHouse row contained:

```text
caller_id:         azure-prod
country_code:      US
country:           United States
outcome:           ok
status_code:       200
lookup_latency_us: 56
ip_mode:           hmac
```

The 56-microsecond lookup shows that local GeoLite2 resolution is a small part of the public request cost.

## Redis/cache decision

Redis is not recommended for the current lookup path. The local MMDB is already memory-mapped and much cheaper than adding a network cache hop, cache invalidation, credentials, another stateful service and additional data-retention surface.

An in-process IP cache is also deferred because there is no evidence that MMDB lookup CPU is material under realistic traffic.

See `PERFORMANCE_TUNING.md` for the full decision record and scaling priorities.

## Durability under load

JetStream remains configured with:

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

The benchmark result must be interpreted with this contract intact: the API waits for a durable JetStream publish acknowledgement before returning a normal lookup result. Performance changes must not remove that guarantee merely to improve benchmark numbers.
