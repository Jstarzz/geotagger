# GeoTagger Performance and Capacity Report

## Scope

This document records the first end-to-end load test of the deployed GeoTagger stack on the on-premises server. It is a deployment-capacity measurement, not a theoretical benchmark of the Go lookup handler in isolation.

The tested path was:

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

and asynchronously:

JetStream -> audit worker -> ClickHouse
```

The same 9-vCPU VM hosted both the k6 load generator and the complete K3s stack, so the generator and server competed for the same CPU resources.

## Test results

| Target tier | Achieved throughput | Reported failure rate | p95 | p99 | HDD utilization |
|---:|---:|---:|---:|---:|---:|
| 100 RPS | ~100 req/s | 25.05%* | 190 ms | 203 ms | <1% |
| 1,000 RPS | ~668 req/s, VU-limited | 24.9%* | 2.08 s | 3.81 s | <1% |
| 5,000 RPS | ~1,373 req/s | 75.6% | 14.3 s | 20.3 s | <3% |
| 10,000 RPS | ~322-643 req/s | 26-37%+ | 6.2-8.2 s | much higher | <1% |

`*` The approximately 25% baseline failure rate at the lower tiers was not an infrastructure failure. Three of the twelve fixture IPs used by `test/load/k6.js` returned the API's valid `404 country not found` result for the installed GeoLite2 snapshot, while the k6 script currently considers only HTTP 200 a successful check. The affected fixtures observed during this run were `1.1.1.1`, `9.9.9.9`, and `2606:4700:4700::1111`.

A future load harness should distinguish a valid `404 country not found` application result from transport/server failure before comparing failure percentages.

## Primary bottleneck

The observed bottleneck was CPU, not the HDD.

During the test:

- HDD utilization remained below 3%;
- NATS durable publishing did not become the limiting resource;
- ClickHouse writes did not saturate the disk;
- queued audit work drained after the tests; and
- durability settings were not relaxed to improve the benchmark.

The VM was simultaneously running:

- k6;
- K3s/control-plane components;
- one or more Go API pods;
- NATS JetStream;
- the audit worker;
- ClickHouse; and
- cloudflared.

At higher offered load, CPU contention caused request latency to rise sharply before storage became a constraint.

## Practical capacity statement

For this specific deployment and test topology, a reasonable operational estimate is:

```text
~500-1,000 requests/second for sustained service before latency degrades sharply
```

This is an operational estimate, not a clean hardware ceiling. The 1,000-RPS test was already load-generator/VU constrained and showed multi-second tail latency, so workloads requiring consistently low latency should be sized more conservatively until an external-generator test is complete.

The service did demonstrate burst throughput above 1,000 requests/second, reaching approximately 1,373 requests/second at the 5,000-RPS tier, but latency at that point was not suitable for a low-latency production SLO.

## Why this does not prove a 10k-RPS ceiling

The original design target was 10,000 requests/second, but this run does not fairly establish whether the server-side stack alone can or cannot reach that figure because:

1. the load generator shared the same CPU as the target;
2. traffic left the VM and traversed the public Cloudflare path before returning;
3. k6 virtual-user availability limited at least one tier;
4. the fixture set counted valid `404` responses as failed checks; and
5. the test exercised the full durable audit contract rather than a lookup-only microbenchmark.

The results therefore represent the real deployed path under a deliberately difficult test arrangement, not an isolated maximum of the handler/MMDB implementation.

## Recommended clean ceiling test

Run k6 from a separate machine with sufficient CPU and network capacity. Preserve the production durability settings.

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

At each stage collect:

- offered and achieved RPS;
- HTTP/application result distribution;
- p50/p95/p99 latency;
- API replica count;
- API CPU and RAM;
- host/VM CPU saturation;
- JetStream pending messages and bytes;
- worker throughput;
- ClickHouse insert rate;
- disk `await`, utilization, queue depth, and throughput; and
- Cloudflare/public round-trip latency.

Also run an internal benchmark directly against the Kubernetes Service or a local port-forward. Comparing internal and public tests separates application capacity from Cloudflare/WAN round-trip effects.

## Lookup-path evidence

A production lookup of `8.8.8.8` completed successfully through the public endpoint and produced request ID:

```text
c75bb0a033c44dd87a9969df059dd6b4
```

The corresponding ClickHouse audit row recorded:

```text
caller_id:         azure-prod
country_code:      US
country:           United States
outcome:           ok
status_code:       200
lookup_latency_us: 56
ip_mode:           hmac
```

The 56-microsecond MMDB lookup illustrates that local GeoIP resolution itself is not the dominant cost in the end-to-end public request. Authentication, durable audit acknowledgement, scheduling/CPU contention, and network round trip dominate at load.

## Durability under load

The benchmark did not disable or weaken auditing. JetStream remained configured for work-queue retention with no age-based expiry, an 8-GiB byte limit, and `DiscardNew`. The backlog drained after each test.

This matters when comparing GeoTagger to a lookup-only benchmark: the measured service is intentionally paying the cost of a durable audit acknowledgement before returning a successful response.
