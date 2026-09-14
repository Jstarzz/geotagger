# GeoTagger Internal Security Audit

## Scope

This is a repository and architecture review of the current GeoTagger implementation and single-node K3s deployment. It is not a penetration test, legal opinion, HIPAA certification, or substitute for the regulated entity's risk analysis.

Reviewed areas:

- API authentication and request validation;
- City/ASN enrichment and `/v1/me` trust boundaries;
- audit privacy and durability;
- Kubernetes workload security;
- service/network exposure;
- secrets and credential handling;
- NATS/ClickHouse deployment;
- Cloudflare Tunnel ingress;
- availability/recovery design; and
- readiness for use in a regulated healthcare workflow.

## Summary

No critical authentication-bypass or remote-code-execution issue was identified in this repository-level review. The machine API has useful controls: per-caller secrets stored as digests, strict input handling, public-address filtering, HMAC-based IP audit storage, durable JetStream acceptance, internal-only stateful services, non-root containers and default-deny ingress.

The City + ASN feature expands the data returned to authenticated clients but does not expand the durable ClickHouse audit schema. Approximate city/coordinate/ASN data therefore remains transient by default.

Main production gaps:

1. pod egress is not default-deny;
2. API-token lifecycle is external/manual;
3. some runtime images remain tag-pinned instead of digest-pinned;
4. ClickHouse SQL privileges should be narrowed further;
5. backup/restore evidence is not yet part of the deployed control set;
6. the service remains in one VM/host failure domain;
7. privileged-human MFA/access governance must be enforced outside the API;
8. `/v1/me` depends on forwarded client-IP metadata from the trusted ingress path; and
9. HIPAA/BAA/risk-management controls must be completed before ePHI use.

## Current controls

### Machine authentication

Caller token format:

```text
key-id.secret
```

The server stores only the SHA-256 digest of the secret and uses constant-time comparison. Unknown key IDs still perform comparable hashing work before rejection.

Risk remaining: key expiry, owner, rotation date, revocation workflow and last-use metadata are not modeled in the application. Maintain those in an operational credential inventory or dedicated secret/identity system.

### Request validation

The API:

- limits request-body size;
- rejects unknown JSON fields;
- rejects multiple JSON values;
- validates and normalizes IP syntax;
- rejects private, loopback and link-local target IPs by default; and
- performs authentication before parsing/storing an explicit target IP.

### Rich response data minimization

`/v1/lookup` can return approximate city/region/coordinates, timezone, network/CIDR and ASN metadata. Those fields are not copied into the durable ClickHouse audit record by default.

This is an intentional minimum-necessary boundary. See `DATA_CLASSIFICATION.md`.

### Audit privacy

Default mode stores HMAC-SHA256 of the target IP. Raw target IP is not retained in the default audit record. Authentication failures are audited without storing the requested target IP.

HMAC values remain sensitive because they are stable/correlatable under one key. Protect the HMAC key separately from ClickHouse.

### Audit durability

Normal lookup success depends on a durable JetStream publish ACK.

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

At capacity, new required events are rejected and the API fails closed rather than deleting old queued audit work.

### Network exposure

Intended ingress:

```text
Internet
  -> Cloudflare
  -> Cloudflare Tunnel
  -> cloudflared
  -> API Service :8080
```

NATS and ClickHouse are ClusterIP-only. Port 9090 is not exposed by the public Service.

Current namespace ingress relationships:

```text
cloudflared -> API        TCP 8080
API         -> NATS       TCP 4222
worker      -> NATS       TCP 4222
worker      -> ClickHouse TCP 8123
```

### MMDB integrity/activation

City and ASN downloads are staged in one versioned release directory. Every staged MMDB is fsynced, opened and verified before activation. One atomic `current` symlink swap activates the complete generation, preventing a live half-updated City/ASN pair.

### Container hardening

Validated identities:

```text
API/worker: UID 65532, GID 65532
NATS:       UID 1000,  GID 1000
```

API/worker controls include non-root execution, disabled privilege escalation, read-only root filesystems and dropped capabilities.

### Secrets

Runtime credentials are referenced through `geotagger-secrets`. K3s is intended to run with secrets encryption at rest.

Kubernetes Secrets are not a complete secret-management program; production still requires administrator governance, rotation, protected recovery copies and incident procedures.

## Findings

### SEC-01 — Pod egress is not default-deny

**Severity: Medium**

A compromised workload can initiate outbound traffic according to CNI/node defaults.

Remediation:

- stage default-deny egress;
- explicitly permit cluster DNS;
- allow only required API/worker internal destinations;
- allow cloudflared to Cloudflare;
- allow the updater to MaxMind; and
- validate all flows before enforcement.

### SEC-02 — Caller-token lifecycle is external/manual

**Severity: Medium**

`API_KEYS` is a static allowlist and does not track issue date, expiry, owner, last use or revocation reason.

Remediation: unique key per integration/environment, external credential inventory, rotation/revocation procedure, activity review, and higher-assurance mTLS/workload identity where justified.

### SEC-03 — Runtime image reproducibility is mixed

**Severity: Medium**

ClickHouse is digest-pinned, while several other workloads use tags such as `latest`.

Remediation: pin approved production images by digest and update through reviewed CI changes.

### SEC-04 — ClickHouse SQL privileges need explicit least privilege

**Severity: Low/Medium**

The worker has a dedicated identity and network isolation, but the manifests do not prove a minimal SQL grant set.

Remediation: explicit ingest role, least required privileges, separate schema/admin identity, deployment verification.

### SEC-05 — Backup/restore control is not yet proven

**Severity: Medium; higher if ePHI or required audit evidence is stored**

Persistent volumes protect against Pod recreation, not host/storage loss.

Remediation: approved RPO/RTO, independent backups, protected backup credentials, clean-environment restore tests and retained recovery evidence. See `BACKUP_RECOVERY.md`.

### SEC-06 — Single-node infrastructure is a single failure domain

**Severity: Medium operational risk**

One host/VM contains K3s, API, NATS, worker, ClickHouse and cloudflared. HPA is not hardware HA.

### SEC-07 — Privileged-human MFA/access governance is external

**Severity: Medium; High if administrators can access ePHI or regulated audit data**

The machine API has no interactive human login. Privileged Cloudflare, GitHub, Proxmox, Kubernetes, SSH/VPN/bastion, backup and secret-store access still requires named identities, MFA, least privilege, recovery controls and periodic review.

See `MFA_AND_IDENTITY.md`.

### SEC-08 — Compliance requires non-code controls and vendor review

**Severity: High if ePHI enters the service path; informational otherwise**

Repository controls do not establish HIPAA compliance. Applicable risk analysis, risk management, workforce controls, BAA/vendor review, access procedures, contingency testing, incident/breach handling, audit retention/review and periodic evaluation remain organizational responsibilities.

See `HIPAA_READINESS.md`.

### SEC-09 — Historical load test shared target CPU

**Severity: Informational / availability planning**

The historical k6 run shared the same 9-vCPU VM as the service and therefore does not establish a clean throughput ceiling. Rich endpoint capacity must be tested externally with `test/load/k6-rich.js`.

### SEC-10 — `/v1/me` trusts forwarded client-IP metadata

**Severity: Medium if the origin becomes directly reachable; Low in the intended Tunnel-only topology**

`/v1/me` prefers `CF-Connecting-IP`, then `X-Forwarded-For`, then the remote peer. These headers identify the client only when they arrive through a trusted proxy path.

Current mitigating architecture: NetworkPolicy/public ingress is designed so external traffic reaches the API through cloudflared rather than directly.

Required controls:

- do not expose the origin directly to untrusted clients while trusting these headers;
- if direct/internal ingress expands, maintain an explicit trusted-proxy list and ignore forwarded headers from other peers;
- test header-spoof behavior whenever ingress architecture changes; and
- treat `/v1/me` as approximate IP intelligence, not proof of a person's physical location.

### SEC-11 — Rich IP-derived location can be over-interpreted

**Severity: Medium privacy/product risk**

City, subdivision and coordinates may be inaccurate or represent ISP/network infrastructure rather than the endpoint. A technically valid response can therefore still be semantically misleading.

Controls:

- return `accuracy_radius_km`;
- document the fields as estimates;
- do not persist rich fields in audit by default;
- prohibit describing coordinates as GPS/device location; and
- require a new data/privacy review before using IP location for high-impact decisions or adding it to durable analytics/audit storage.

## Release security checklist

```text
[ ] no plaintext production secrets committed
[ ] invalid/missing bearer token -> 401
[ ] private target -> 422
[ ] malformed/oversized request rejected
[ ] every response has X-Request-ID
[ ] rich response request_id matches response header
[ ] rich response identifies City + ASN source builds
[ ] /v1/me works through intended Cloudflare ingress
[ ] direct-origin/header spoofing exposure has not been introduced
[ ] matching audit row reaches ClickHouse
[ ] rich city/coordinate/ASN response fields are absent from audit unless explicitly approved
[ ] raw IP is absent when AUDIT_IP_MODE=hmac
[ ] authentication-failure audit does not retain target IP
[ ] API fails closed when required durable audit publication cannot complete
[ ] NATS/ClickHouse/admin port are not externally exposed
[ ] NetworkPolicy enforcement active
[ ] Kubernetes Secrets encryption verified
[ ] API/worker/NATS run with expected numeric non-root identities
[ ] capabilities remain dropped
[ ] read-only rootfs remains enabled where designed
[ ] active MMDB current symlink points to a complete verified release
[ ] caller credential inventory/rotation status current
[ ] privileged administrator MFA/access review current
[ ] backup/restore evidence current
[ ] incident-response contacts/runbook current
[ ] data classification and HIPAA gate match the environment
```

## Residual risk

For a controlled internal machine service, the current code and deployment model provide a reasonable security foundation. Regulated production use still requires the unresolved controls to be dispositioned through the organization's formal risk-management process, including evidence for vendor contracts, identity/access, backups, incident response, audit handling, recovery and the intended use of approximate location data.
