# GeoTagger Production Architecture

## Overview

GeoTagger is a self-hosted, authenticated IP-to-country API deployed on a single on-premises K3s node behind Cloudflare Tunnel. The design keeps the geolocation lookup path local and uses a durable audit queue so the API does not depend synchronously on ClickHouse.

Current public endpoint:

```text
https://geo.itsjosiahdavis.dev
```

## Logical topology

```text
Caller
  |
  | HTTPS
  v
Cloudflare edge
  |
  | outbound-established Cloudflare Tunnel
  v
cloudflared pod
  |
  v
ClusterIP service :8080
  |
  v
GeoTagger Go API pods  <---- HPA 1..8
  |          |
  |          +---- memory-mapped GeoLite2 Country MMDB
  |
  +---- durable publish + JetStream ACK
               |
               v
          NATS JetStream
               |
               v
        audit worker (batched)
               |
               v
           ClickHouse
           TTL 30 days
```

Administrative traffic is separate from the public API path:

```text
GeoTagger API pod :9090
  |- /healthz
  |- /readyz
  `- /metrics
```

Port 9090 is not exposed by the public Kubernetes Service.

## Physical/deployment topology

The production/staging deployment runs inside a dedicated Linux VM on the existing Proxmox host. K3s, the API, NATS, ClickHouse, the audit worker, cloudflared, and the MMDB updater all run inside that VM.

The load test reported in `PERFORMANCE.md` used a 9-vCPU VM and ran the load generator on the same VM as the entire application stack. That fact is important when interpreting the measured ceiling.

The backing storage is HDD-based. Deployment validation showed the disk was not the limiting resource in the tested workload; CPU saturation occurred first.

## Request path

A lookup follows this order:

1. Cloudflare accepts the HTTPS connection.
2. The existing Cloudflare Tunnel forwards the request to the internal `geotagger` Kubernetes Service.
3. The Go API authenticates the caller.
4. The request body is bounded and parsed with unknown JSON fields rejected.
5. The IP is normalized and checked against the public-address policy.
6. The local memory-mapped GeoLite2 Country MMDB is queried.
7. An audit event is published to NATS JetStream.
8. The API waits for the JetStream durable publish acknowledgement.
9. Only after the durable acknowledgement does the API return the lookup result.
10. The audit worker later consumes queued events and writes them to ClickHouse in batches.

ClickHouse is therefore outside the synchronous response path.

## GeoLite2 lifecycle

GeoTagger does not call a third-party geolocation API per request.

The MMDB lifecycle is:

```text
MaxMind download
  -> updater/bootstrap
  -> temporary file
  -> validation/fsync
  -> atomic rename
  -> local MMDB path
  -> API reload
```

API pods reuse the local database rather than downloading a copy for every replica.

## Audit durability model

The audit queue uses NATS JetStream with work-queue retention.

Production safety settings are:

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

`MaxAge=0` is deliberate: an unpersisted audit event must not disappear simply because it has been queued for a certain number of days. Capacity exhaustion is handled fail-closed instead. When the durable queue cannot accept a required event, the API returns a service error rather than silently completing an unaudited successful lookup.

The worker provides at-least-once consumption semantics. A crash after a ClickHouse insert but before NATS acknowledgement can produce a duplicate audit row; downstream analysis should account for that edge case using `request_id` where appropriate.

## ClickHouse

ClickHouse stores audit records in `geotagger.audit_events` using a `MergeTree` table partitioned by date. Rows include:

- UTC timestamp;
- request ID;
- caller ID;
- HMAC/raw/omitted IP representation according to policy;
- country code and country;
- outcome and HTTP status;
- lookup latency; and
- MMDB version.

The table has a 30-day deletion TTL. TTL deletion is merge-driven rather than an exact wall-clock deletion guarantee.

The deployment intentionally pins a ClickHouse 26.3 LTS image compatible with the Westmere-generation host. Standard 26.6+ amd64 images require a newer x86-64/AVX2 baseline and should not be adopted without rechecking CPU compatibility.

## Kubernetes scaling

The API Deployment uses an HPA:

```text
minimum replicas: 1
maximum replicas: 8
CPU target:       65%
```

This scales API containers on one physical K3s node. It is not hardware high availability and cannot add CPU/RAM beyond the VM/node.

NATS and ClickHouse remain single stateful instances in the current architecture.

## Network isolation

The namespace applies default-deny ingress and then opens only the required flows:

```text
cloudflared -> API        TCP 8080
API/worker  -> NATS       TCP 4222
worker      -> ClickHouse TCP 8123
```

NATS, ClickHouse, and the API administrative listener are not intended to be Internet-accessible.

The current NetworkPolicy set restricts ingress only. Egress policy hardening is discussed in `SECURITY_AUDIT.md`.

## Secrets

Kubernetes Secrets hold:

- caller API-key digests (`API_KEYS`);
- audit HMAC key;
- MaxMind license key;
- ClickHouse password; and
- Cloudflare Tunnel token.

The recommended K3s configuration enables secrets encryption at rest using the K3s secretbox provider.

Client bearer secrets are never stored in plaintext by GeoTagger; only their SHA-256 digests are configured server-side.

## Failure behavior

| Failure | Expected effect |
|---|---|
| MMDB unavailable at startup | API waits/fails readiness rather than serving without the database |
| NATS unavailable | readiness fails; required audit publication cannot complete |
| JetStream full | new durable events are rejected; API fails closed |
| Worker stopped | API can continue while JetStream has capacity; backlog accumulates |
| ClickHouse stopped | worker cannot persist; JetStream backlog remains until recovery |
| API pod restarted | Kubernetes recreates it; Service routes to healthy replicas |
| Cloudflare connector unavailable | public endpoint unavailable; origin remains non-public |

Controlled deployment testing confirmed that worker/ClickHouse interruptions recover and queued audit work drains after restoration.

## Availability boundary

The current design improves process-level resilience but is still a single-node deployment. The VM, physical Proxmox host, local power/network, single NATS stateful pod, and single ClickHouse stateful pod are availability dependencies.

A future HA design would require separate failure domains, replicated state, tested backup/restore, and a network/power strategy. HPA alone does not provide that.
