# GeoTagger Project Overview

## Executive summary

GeoTagger is a self-hosted, authenticated IP intelligence service for on-premises deployment. It resolves public IPv4 and IPv6 addresses against local MaxMind GeoLite2 City and ASN databases, returns geographic/network metadata, and records a durable privacy-preserving audit event for each authenticated lookup.

Public endpoint:

```text
https://geo.itsjosiahdavis.dev
```

The core function is:

```text
public IP address
-> local geographic estimate
-> local ASN/network identity
-> source/version metadata
-> durable audit record
```

GeoTagger is not a GPS system. City, region and coordinates are approximate IP-geolocation data and are returned with an accuracy radius when the database supplies one.

## Why this service exists

A hosted API can answer a basic GeoIP question, but GeoTagger is designed around additional operational requirements:

- no third-party lookup request on the hot path;
- local City and ASN datasets;
- machine-specific authentication;
- durable request accounting;
- privacy-preserving retained audit data;
- server-generated request/audit correlation IDs;
- controlled retention and infrastructure ownership;
- Kubernetes health-aware routing and autoscaling;
- isolated NATS/ClickHouse stateful services;
- scheduled, verified database refreshes; and
- explicit data provenance and location uncertainty.

The project should therefore be evaluated as an internal infrastructure service, not merely as an alternative to a one-line public GeoIP request.

## API surface

```text
POST /v1/lookup       full City + ASN intelligence
GET  /v1/lookup?ip=  full lookup using a query parameter
GET  /v1/me           full lookup for the observed caller IP
POST /v1/country      backward-compatible country-only response
```

All endpoints require the same application bearer credential.

## Rich intelligence model

A successful `/v1/lookup` can include:

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
IPv4/IPv6 classification
City MMDB build/version
ASN MMDB build/version
local lookup latency
request ID
```

Unavailable fields are omitted or left empty rather than inferred.

## Data sources

Production uses:

```text
GeoLite2-City.mmdb
GeoLite2-ASN.mmdb
```

Both are downloaded with the existing `MAXMIND_LICENSE_KEY`; no additional provider/API key is needed.

The Go API keeps long-lived memory-mapped readers open for both databases.

## Normal request flow

```text
authenticate caller
-> parse and normalize IP
-> enforce public-address policy
-> decode City MMDB
-> decode ASN MMDB
-> construct rich response
-> create minimized privacy-preserving audit event
-> durable JetStream publish + ACK
-> return response
```

ClickHouse persistence happens asynchronously after the durable NATS acceptance.

## Authentication

Machine credentials use:

```text
key-id.secret
```

Only the SHA-256 digest of the secret is configured server-side. Comparison is constant-time. Authentication occurs before the requested target IP is parsed, so unauthenticated requests do not cause target-IP retention in the audit event.

## Public-address policy

By default GeoTagger rejects:

```text
malformed IP addresses
RFC1918 private IPv4 addresses
loopback addresses
link-local addresses
other targets that are not permitted public global-unicast addresses
```

This avoids assigning Internet geolocation semantics to internal/private addresses.

## Caller self-lookup

`GET /v1/me` uses the public caller address observed through the ingress path rather than an explicit request body.

Current precedence:

```text
CF-Connecting-IP
first X-Forwarded-For entry
RemoteAddr for trusted internal/direct testing
```

Because forwarding headers can be spoofed by direct clients, the API origin must remain restricted to the intended Cloudflare Tunnel/internal path. A future direct-origin architecture must implement explicit trusted-proxy verification.

## Geolocation accuracy boundary

Country-level results are generally more reliable than city-level estimates. City, region and coordinates can represent an ISP allocation or approximate area rather than the endpoint's physical location.

Consumers must:

- not call IP-derived coordinates GPS;
- use `accuracy_radius_km` where precision matters;
- tolerate missing values;
- expect results to change after database updates; and
- not use approximate IP location as the sole basis for a high-impact decision.

## Audit model

GeoTagger deliberately does **not** retain the complete rich response by default.

The ClickHouse audit record contains:

```text
timestamp
request ID
caller ID
HMAC/raw/omitted IP representation according to policy
country code/name
outcome
HTTP status
lookup latency
combined City/ASN source version metadata
```

The default `AUDIT_IP_MODE=hmac` stores HMAC-SHA256 of the target IP rather than the raw address. City, coordinates, postal code, timezone and ASN remain response-only unless a future reviewed requirement explicitly adds them to durable storage.

## Durability contract

Normal success requires NATS JetStream to accept the audit event durably.

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

If the required publish cannot complete, GeoTagger fails closed instead of returning a normal unaudited result.

## Asynchronous audit persistence

```text
JetStream
-> audit worker
-> batched JSONEachRow insert
-> ClickHouse
-> ACK NATS only after successful persistence
```

Delivery is at least once. A crash after ClickHouse accepts an insert but before NATS receives the ACK can create a duplicate. Use `request_id` for correlation/deduplication where needed.

## Atomic MMDB generation lifecycle

City and ASN are maintained as one active **bundle generation**, not independently replaced live files.

Filesystem model:

```text
/var/lib/geotagger/mmdb/
├── current -> releases/release-<generation>/
└── releases/
    ├── release-<older>/
    ├── release-<previous>/
    └── release-<current>/
        ├── GeoLite2-City.mmdb
        └── GeoLite2-ASN.mmdb
```

Update lifecycle:

```text
CronJob starts updater
-> create versioned staging directory
-> download City archive
-> download ASN archive
-> write/fsync both MMDB files
-> open and Verify both databases
-> publish complete release directory
-> atomically rename one current symlink
-> fsync parent directory
-> retain newest three complete releases
-> API detects the new generation
-> API opens/verifies replacement readers
-> API swaps readers without Pod restart
```

The atomic `current` symlink is the commit point. API processes see the old complete City+ASN generation or the new complete generation, not a mixed half-update.

Legacy/custom single-edition updater mode remains supported through `MAXMIND_EDITION` and `MMDB_PATH`.

## Kubernetes deployment

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
Secret/geotagger-secrets
```

See `KUBERNETES.md` for operating details.

## API scaling

```text
minimum replicas: 2
maximum replicas: 8
CPU request:      750m
CPU target:       65% of request
CPU limit:        2 cores per Pod
```

All API Pods currently share one 9-vCPU VM. HPA can consume unused CPU but cannot create additional physical capacity or hardware HA.

## State and storage

```text
NATS JetStream -> persistent volume
ClickHouse     -> persistent volume
MMDB releases  -> node-local host path on data disk
```

API Pods are otherwise replaceable/stateless.

## Security controls

Current controls include:

```text
per-caller bearer credentials
server-side SHA-256 secret digests
constant-time authentication
HMAC IP audit mode
strict/bounded request parsing
Cloudflare Tunnel ingress
default-deny Kubernetes ingress
internal-only NATS and ClickHouse Services
non-root numeric container UIDs
read-only root filesystems where designed
dropped Linux capabilities
internal-only admin listener
K3s secrets-encryption recommendation
```

Privileged-human MFA belongs on Cloudflare, GitHub, Proxmox, SSH/VPN/bastion, Kubernetes, backup and secret-management access rather than on every automated API request. See `MFA_AND_IDENTITY.md`.

## HIPAA/compliance boundary

The repository does not declare the deployment HIPAA compliant. Before ePHI use, complete the applicable risk analysis, risk management, vendor/BAA review, access governance, backup/restore validation, incident/breach procedures, audit review/retention, workforce controls and periodic evaluations described in `HIPAA_READINESS.md`.

The richer location fields expand the transient response data surface, so `DATA_CLASSIFICATION.md` must be part of any regulated integration review.

## Performance status

Historical load testing measured the earlier country-only path. The rich endpoint now performs two MMDB decodes and serializes a larger response.

Therefore:

```text
old 500-1,000 RPS operating estimate = historical country path only
old 56 us lookup observation         = historical single-MMDB lookup only
```

Use `test/load/k6-rich.js` from an external load generator before publishing `/v1/lookup` capacity numbers. Origin metrics separate total handler latency, local lookup latency and durable JetStream publish latency.

Redis remains intentionally absent. Both City and ASN datasets are already local memory-mapped files; caching should be added only if realistic rich-endpoint measurements show MMDB decode CPU is a material bottleneck.

## Operational acceptance

A rich deployment is functionally validated when:

1. `current` points to a complete verified release;
2. both City and ASN files are accessible through `current`;
3. API Pods are ready;
4. NATS, worker and ClickHouse are healthy;
5. Cloudflare Tunnel is connected;
6. an authenticated public `/v1/lookup` succeeds;
7. response source metadata identifies both database builds;
8. response and header request IDs match; and
9. the exact request ID appears in ClickHouse with the configured HMAC audit mode.

Capacity/SLO validation is a separate benchmark exercise.
