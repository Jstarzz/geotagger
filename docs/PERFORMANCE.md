# GeoTagger Performance and Capacity Report

## Scope

This report records the first end-to-end load test of the deployed GeoTagger stack and defines how the new rich City + ASN endpoint must be benchmarked.

The historical numbers below were measured on the earlier country-only request path. They are useful as a baseline for the same hardware and durable-audit architecture, but they are **not measured capacity figures for `/v1/lookup`**.

Historical tested path:

```text
k6 on the GeoTagger VM
  -> public HTTPS hostname
  -> Cloudflare edge
  -> Cloudflare Tunnel
  -> K3s Service
  -> GeoTagger API
  -> local country MMDB lookup
  -> durable NATS JetStream ACK
  -> response

asynchronous audit persistence:

JetStream -> audit worker -> ClickHouse
```

The same 9-vCPU VM hosted both k6 and the complete K3s stack, so the load generator competed with the target for CPU.

## Historical country-only result

| Target tier | Achieved throughput | Reported failure rate | p95 | p99 | HDD utilization |
|---:|---:|---:|---:|---:|---:|
| 100 RPS | ~100 req/s | 25.05%* | 190 ms | 203 ms | <1% |
| 1,000 RPS | ~668 req/s, VU-limited | 24.9%* | 2.08 s | 3.81 s | <1% |
| 5,000 RPS | ~1,373 req/s | 75.6% | 14.3 s | 20.3 s | <3% |
| 10,000 RPS | ~322-643 req/s | 26-37%+ | 6.2-8.2 s | much higher | <1% |

`*` The approximately 25% low-tier failure rate was a test-harness artifact, not an infrastructure failure. Three of the twelve valid public fixture IPs returned the legitimate application result `404 country not found` for the installed GeoLite2 snapshot. The historical k6 script counted only HTTP 200 as a passing check.

The load scripts now treat expected `200`/`404` lookup outcomes separately from unexpected transport/service failures.

## Observed historical bottleneck

CPU contention appeared before storage contention.

During the test:

- HDD utilization remained below 3%;
- NATS durable publishing did not appear to be the limiting resource;
- ClickHouse writes did not saturate the disk;
- queued audit work drained after every tier; and
- audit durability settings were not weakened for the benchmark.

The VM was running all of the following at once:

```text
k6
K3s/control-plane processes
API Pods
NATS JetStream
audit worker
ClickHouse
cloudflared
```

At offered load above the sustainable range, CPU contention drove large tail-latency increases.

## Historical capacity statement

For the exact old country-only topology used in that test, the defensible operational estimate was:

```text
roughly 500-1,000 requests/second before tail latency became poor
```

That was never a clean server-side ceiling because the load generator shared CPU with the target and traffic traversed the public Cloudflare path.

Do **not** copy that number onto the new full intelligence endpoint.

## Rich endpoint changes the cost model

`/v1/lookup` now performs:

```text
City MMDB decode
+
ASN MMDB decode
+
richer response construction
+
larger JSON serialization
+
the same durable JetStream publish/ACK requirement
```

The new data path remains local and memory-mapped, but it does more work than `/v1/country`.

A rich response may include:

```text
network/CIDR
continent
country
region
city
postal code
estimated coordinates
accuracy radius
timezone
ASN
ASN organization
source database metadata
classification
request ID
lookup latency
```

Therefore the old single-MMDB `56 microseconds` observation must not be presented as measured rich-lookup latency.

## Post-test performance changes

### HPA tuning

Current API settings:

```text
minimum replicas: 2
maximum replicas: 8
CPU request:      750m per API Pod
CPU limit:        2 per API Pod
HPA target:       65%
```

This avoids the previous overly sensitive scaling signal created by a 250m request.

### Additional origin metrics

The API exposes:

```text
geotagger_lookup_latency_microseconds
geotagger_audit_publish_latency_microseconds
geotagger_request_latency_microseconds
```

For the rich endpoint, `geotagger_lookup_latency_microseconds` includes both local City and ASN lookup/decode work because those operations are performed inside the one lookup implementation.

### Load-harness correction

The historical false 404 failure baseline was removed from the country load harness.

A separate rich benchmark now exists:

```text
test/load/k6-rich.js
```

It validates expected application status, `X-Request-ID`, and rich response source metadata when a lookup is found.

### Worker allocation reduction

The asynchronous audit worker reuses the batch row slice and pre-sizes ClickHouse NDJSON buffers. This is a small optimization and not expected to dominate rich request throughput.

## Redis/cache decision after City + ASN

Redis is still not recommended by default.

The current lookup path is:

```text
Go API
-> memory-mapped City MMDB
-> memory-mapped ASN MMDB
```

Adding Redis would add:

```text
socket/network hop
protocol parsing
key serialization
cache invalidation
another stateful service
another credential boundary
additional memory/data-retention surface
```

Two local MMDB decodes may cost more than the former single country lookup, but caching should be introduced only if the new rich benchmark shows MMDB decode CPU is materially contributing to the bottleneck.

An in-process LRU remains deferred for the same reason and because it would add per-Pod cache inconsistency and MMDB-refresh invalidation complexity.

## Clean benchmark plan

Run load generation from a machine other than the GeoTagger VM.

Benchmark the two products separately:

```text
A. compatibility path
POST /v1/country
script: test/load/k6.js

B. full intelligence path
POST /v1/lookup
script: test/load/k6-rich.js
```

Optional convenience-path tests should also cover:

```text
GET /v1/lookup?ip=...
GET /v1/me
```

Suggested rich stages:

```text
100 RPS
250 RPS
500 RPS
750 RPS
1,000 RPS
1,500 RPS
2,000 RPS
then increase only while SLOs remain acceptable
```

Example:

```bash
RPS=100 DURATION=60s \
BASE_URL=https://geo.itsjosiahdavis.dev \
API_TOKEN="$API_TOKEN" \
k6 run test/load/k6-rich.js
```

Collect at every stage:

- offered and achieved RPS;
- HTTP result distribution;
- p50/p95/p99 public latency;
- origin request latency;
- combined City+ASN lookup latency;
- JetStream publish-ACK latency;
- response bytes;
- HPA desired/current replicas;
- per-Pod CPU/RAM;
- VM CPU saturation/load/context switches;
- JetStream pending messages/bytes/redelivery;
- worker throughput/backlog;
- ClickHouse insert rate/errors;
- disk `await`, queue depth, utilization and throughput; and
- public versus internal network RTT.

Run two network paths where practical:

```text
public:
external generator -> Cloudflare -> Tunnel -> Service -> API

trusted internal:
external generator -> internal origin/service path -> API
```

The difference helps isolate Cloudflare/WAN cost from application cost.

## Verified historical lookup evidence

The original end-to-end production test for `8.8.8.8` returned request ID:

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

That row proves the older country path completed API -> JetStream -> worker -> ClickHouse with HMAC-mode IP storage. It does not measure the new two-database rich endpoint.

## Rich-endpoint production acceptance

After deployment, record a new validation artifact containing:

```text
request ID
input IP
HTTP status
response source.city_database
response source.asn_database
rich lookup latency
matching ClickHouse request ID
HMAC audit mode confirmation
```

Then run `k6-rich.js` externally and add a new measured capacity section here instead of rewriting the historical table.

## Durability under load

JetStream remains configured with:

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

The API still waits for durable JetStream acceptance before returning a normal response. Performance work must not remove that guarantee merely to improve benchmark numbers.
