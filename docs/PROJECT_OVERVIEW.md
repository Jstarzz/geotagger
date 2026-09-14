# GeoTagger Project Overview

## Executive summary

GeoTagger is a self-hosted, authenticated IP intelligence service for on-premises deployment. It resolves public IPv4 and IPv6 addresses against local MaxMind GeoLite2 City and ASN databases, returns geographic/network metadata, and records a durable privacy-preserving audit event for each authenticated lookup.

Public endpoint:

```text
https://geo.itsjosiahdavis.dev
```

The core business function is now:

```text
public IP address
-> geographic estimate
-> network/ASN identity
-> source/version metadata
-> durable audit record
```

The project is not intended to provide GPS-quality location. IP-derived city/region/coordinates are estimates and are returned with an accuracy radius where available.

## Why the service exists

A hosted service such as IPinfo can answer a simple GeoIP question, but GeoTagger is built around a different set of operational requirements:

- local lookup data rather than a third-party HTTP dependency per request;
- machine-specific authentication;
- privacy-preserving retained audit data;
- durable request accounting;
- request/audit correlation through server-generated IDs;
- controlled retention and infrastructure ownership;
- autoscaling and health-aware routing;
- network isolation for stateful components;
- scheduled local database refreshes; and
- explicit source/build metadata and geolocation uncertainty.

The architecture should therefore be evaluated as an internal infrastructure service, not merely as a replacement for a one-line `curl` command.

## Public API surface

```text
POST /v1/lookup       full City + ASN intelligence
GET  /v1/lookup?ip=  full lookup using query parameter
GET  /v1/me           full lookup for observed caller IP
POST /v1/country      backward-compatible country-only response
```

All endpoints require the same application bearer credential.

## Full intelligence model

A rich lookup can return:

```text
normalized IP
matched network/CIDR
continent
country
region/subdivision
city
postal code
estimated latitude/longitude
accuracy radius
timezone
ASN
ASN organization
public/private/loopback/link-local classification
IP version
City MMDB build/version
ASN MMDB build/version
lookup latency
request ID
```

Fields unavailable in the current database snapshot are left empty/omitted rather than inferred.

## Data sources

GeoTagger uses:

```text
GeoLite2-City.mmdb
GeoLite2-ASN.mmdb
```

Both are downloaded with the existing `MAXMIND_LICENSE_KEY`. No additional third-party lookup API key is required.

The API uses the Go MaxMind DB reader and keeps long-lived memory-mapped readers open for both files.

## Normal request flow

```text
authenticate caller
-> parse/normalize IP
-> apply public-address policy
-> City MMDB lookup
-> ASN MMDB lookup
-> construct response
-> create privacy-preserving audit event
-> durable JetStream publish + ACK
-> return response
```

ClickHouse insertion occurs asynchronously after the request returns.

## Audit model

GeoTagger deliberately does not retain the full rich response by default.

The audit record contains:

```text
timestamp
request ID
caller ID
HMAC/raw/omitted IP according to policy
country code/name
outcome
HTTP status
lookup latency
combined City/ASN database-version metadata
```

The default IP mode is HMAC-SHA256, so the raw target IP is not stored in the ClickHouse audit row.

This design allows repeated-address correlation while reducing retained identifier exposure.

## Durability contract

Normal success depends on NATS JetStream accepting the audit event durably.

Current stream safety settings:

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

If the required durable publish cannot complete, GeoTagger fails closed instead of returning a normal successful lookup without its required audit event.

## Asynchronous persistence

The worker consumes JetStream messages in batches and inserts them into ClickHouse.

```text
JetStream
-> audit worker
-> batched JSONEachRow insert
-> ClickHouse
-> ACK NATS only after successful persistence
```

Delivery is at least once. A crash between ClickHouse insertion and the NATS ACK can create duplicate rows; `request_id` is available for deduplication/correlation.

## MMDB update lifecycle

The scheduled updater manages City and ASN together.

```text
CronJob starts updater
-> download both archives
-> stage both MMDB files
-> fsync staged files
-> open/verify each database
-> begin live replacement only after all verification succeeds
-> atomic rename City
-> atomic rename ASN
-> fsync directory
-> API notices changed files
-> hot-reload changed readers
```

Each final file replacement is atomic. A host/process crash between the two final renames can temporarily leave City and ASN on different build timestamps; GeoTagger tolerates that state and exposes each source version separately.

The updater keeps legacy single-edition configuration support for custom deployments.

## Caller self-lookup

`GET /v1/me` resolves the caller IP observed through the public ingress path.

Preferred source:

```text
CF-Connecting-IP
```

Fallbacks exist for trusted internal/direct testing.

The origin must remain restricted to the Cloudflare Tunnel/internal trust boundary. Forwarded headers must not become an untrusted identity source if the origin is later exposed directly.

## Public-address policy

By default, requested targets must be valid public global-unicast addresses.

Rejected by default:

```text
RFC1918 private addresses
loopback
link-local
malformed IPs
```

This prevents callers from treating internal/private addresses as meaningful Internet geolocation targets.

## Geolocation accuracy boundary

Country-level results are generally more robust than city-level results. City, region and coordinates may represent an ISP/network registration or approximate geolocation rather than the physical endpoint.

Consumers must:

- treat city/region as estimates;
- display/use the returned accuracy radius where relevant;
- avoid describing IP-derived coordinates as GPS/device location; and
- tolerate missing or changing values after MMDB updates.

## Kubernetes deployment

The deployed service runs inside one K3s VM.

Main resources:

```text
Deployment/geotagger-api
HorizontalPodAutoscaler/geotagger-api
Deployment/geotagger-audit-worker
StatefulSet/nats
StatefulSet/clickhouse
Deployment/cloudflared
Job/geotagger-mmdb-bootstrap
CronJob/geotagger-mmdb-update
Service/geotagger
Service/nats
Service/clickhouse
NetworkPolicy objects
Kubernetes Secret/geotagger-secrets
```

See `KUBERNETES.md` for details.

## API scaling

Current API autoscaling:

```text
minimum replicas: 2
maximum replicas: 8
CPU request: 750m
CPU target: 65%
```

Scaling is horizontal only within the one 9-vCPU VM. It can use spare cores but cannot create new hardware capacity.

## State and storage

Stateful data:

```text
NATS JetStream -> persistent volume
ClickHouse     -> persistent volume
City MMDB      -> node-local file
ASN MMDB       -> node-local file
```

API Pods are otherwise replaceable/stateless.

## Security boundary

Implemented controls include:

```text
per-caller bearer credentials
server-side SHA-256 secret digests
constant-time verification
HMAC IP audit mode
strict/bounded request parsing
Cloudflare Tunnel ingress
Kubernetes NetworkPolicy
internal-only NATS/ClickHouse Services
non-root application containers
read-only root filesystems where supported
dropped capabilities
internal admin listener
K3s secret encryption recommendation
```

The richer response data does not change the basic authentication model.

## MFA boundary

Interactive MFA is for privileged humans, not automated API requests.

Privileged human systems should use MFA where supported:

```text
Cloudflare
GitHub
Proxmox
SSH/VPN/bastion
Kubernetes administration
backup/secret systems
future human admin UI
```

Machine authentication can later be strengthened with mTLS/workload identity if needed.

## HIPAA/compliance boundary

The repository does not declare the deployment HIPAA compliant.

Before ePHI use, complete the applicable:

```text
risk analysis
risk-management plan
vendor/BAA review
access-control procedures
backup/restore validation
incident/breach procedures
audit retention/review
workforce/administrative controls
periodic evaluation
```

See `HIPAA_READINESS.md`.

## Performance status

The first benchmark measured the earlier country-only implementation and found CPU contention before HDD saturation. A verified country MMDB lookup took 56 microseconds, and disk utilization stayed low during the test.

The new full lookup performs two MMDB decodes and returns substantially more JSON, so those old throughput numbers must not be presented as measured `/v1/lookup` capacity.

Required follow-up benchmark:

```text
external load generator
country endpoint baseline
rich POST lookup
rich GET lookup
self lookup
origin-side latency metrics
public Cloudflare latency
CPU/HPA behavior
JetStream ACK latency
```

Redis is not currently justified because City and ASN are already local memory-mapped datasets. Add caching only if realistic traffic proves MMDB decode work is a meaningful bottleneck.

## Operational success criteria

A deployment is considered functionally validated when:

1. City and ASN MMDB files are present and verified;
2. API Pods are ready;
3. NATS and ClickHouse are healthy;
4. Cloudflare Tunnel is connected;
5. an authenticated public rich lookup succeeds;
6. the response includes `X-Request-ID` and source metadata;
7. the exact request ID appears in ClickHouse; and
8. the retained `ip_value` follows the configured privacy mode.

Capacity/SLO validation is a separate benchmark exercise.
