# GeoTagger Internal Security Audit

## Scope

This is a repository and architecture review of the current GeoTagger implementation and single-node K3s deployment. It is not a penetration test, legal opinion, HIPAA certification, or substitute for the regulated entity's risk analysis.

Reviewed areas:

- API authentication and request validation;
- audit privacy and durability;
- Kubernetes workload security;
- service/network exposure;
- secrets and credential handling;
- NATS/ClickHouse deployment;
- Cloudflare Tunnel ingress;
- availability/recovery design; and
- readiness for use in a regulated healthcare workflow.

## Summary

No critical authentication-bypass or remote-code-execution issue was identified in this repository-level review. The current machine API has several useful controls: per-caller secrets stored as digests, strict input handling, public-address filtering, HMAC-based IP audit storage, durable JetStream acceptance, internal-only stateful services, non-root application containers and default-deny ingress.

The main production gaps are operational rather than lookup-algorithm problems:

1. pod egress is not default-deny;
2. API-token lifecycle is external/manual;
3. some runtime images remain tag-pinned instead of digest-pinned;
4. ClickHouse SQL privileges should be narrowed further;
5. backups/restore evidence are not yet part of the deployed control set;
6. the entire service remains on one VM/host failure domain;
7. privileged-human MFA/access governance must be enforced outside the API; and
8. HIPAA/BAA/risk-management controls must be completed before ePHI use.

## Current controls

### Machine authentication

Caller token format:

```text
key-id.secret
```

The server stores the SHA-256 digest of the secret and uses constant-time comparison. Unknown key IDs still execute comparable hashing work before rejection.

The key generator uses cryptographically secure random material.

Risk remaining: key expiry, owner, rotation date, revocation workflow and last-use metadata are not modeled in the application. Maintain those in an operational credential inventory or dedicated secret/identity system.

### Request validation

The API:

- limits body size;
- rejects unknown JSON fields;
- rejects multiple JSON values;
- validates IP syntax;
- normalizes IPv4-mapped addresses; and
- rejects private, loopback and link-local targets unless explicitly configured otherwise.

### Audit privacy

Default mode stores HMAC-SHA256 of the target IP. Raw target IP is not retained in the default audit record. Authentication failures are audited without storing the requested target IP.

HMAC values remain sensitive because they are stable/correlatable under one key. Protect the HMAC key separately from the ClickHouse dataset.

### Audit durability

Normal lookup success depends on a durable JetStream publish ACK.

Stream safety settings:

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

This prevents required unpersisted events from expiring solely because of age. At the storage ceiling, new required events are rejected and the API fails closed instead of deleting older queued work.

### Network exposure

Current intended ingress:

```text
Internet
  -> Cloudflare
  -> outbound-established Tunnel
  -> cloudflared
  -> API Service :8080
```

NATS and ClickHouse are ClusterIP-only. The API administrative listener on port 9090 is not exposed by the public Service.

Current namespace ingress rules allow only:

```text
cloudflared -> API        TCP 8080
API         -> NATS       TCP 4222
worker      -> NATS       TCP 4222
worker      -> ClickHouse TCP 8123
```

### Container hardening

Validated runtime identities:

```text
API/worker: UID 65532, GID 65532
NATS:       UID 1000,  GID 1000
```

API/worker controls include:

```text
runAsNonRoot: true
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities: drop ALL
```

Explicit numeric identities avoid Kubernetes admission ambiguity around `runAsNonRoot`.

### Secrets

Runtime credentials are referenced through `geotagger-secrets`. K3s is intended to run with secrets encryption at rest.

A Kubernetes Secret is not equivalent to a complete secret-management program. Node/Kubernetes administrators can be highly privileged; production controls still need administrator governance, rotation, recovery copies and incident procedures.

## Findings

### SEC-01 — Pod egress is not default-deny

**Severity: Medium**

The namespace applies default-deny ingress but does not currently apply default-deny egress. A compromised workload can initiate outbound traffic according to the CNI/node defaults.

Remediation:

- stage a namespace default-deny egress policy;
- explicitly allow cluster DNS;
- allow API/worker only required internal destinations;
- allow cloudflared to reach Cloudflare;
- allow the updater to reach required MaxMind endpoints; and
- validate all flows in staging before enforcement.

Do not deploy an egress-deny policy blindly; breaking DNS/tunnel/update traffic can make the service unavailable.

### SEC-02 — Caller-token lifecycle is external/manual

**Severity: Medium**

`API_KEYS` is a static allowlist of caller IDs and secret digests. The service does not track issue date, expiry, owner, last use or revocation reason.

Remediation:

- unique key per integration/environment;
- external credential inventory;
- documented rotation interval;
- immediate revocation procedure;
- alert/review of unusual caller activity; and
- consider mTLS or signed workload identity for higher-assurance deployments.

See `MFA_AND_IDENTITY.md`.

### SEC-03 — Runtime image reproducibility is mixed

**Severity: Medium**

ClickHouse is digest-pinned. Several other images use version or `latest` tags. Mutable tags reduce byte-for-byte reproducibility.

Remediation:

- pin production application and infrastructure images by digest after validation;
- retain readable version metadata next to each digest; and
- update image references through reviewed changes/CI.

### SEC-04 — ClickHouse SQL privileges need explicit least privilege

**Severity: Low/Medium**

The worker uses a dedicated `geotagger_ingest` identity and NetworkPolicy restricts network access. The manifests do not demonstrate a minimal SQL role limited strictly to required operations.

Remediation:

- create explicit ClickHouse roles/users;
- grant only required insert/query/metadata privileges;
- separate schema/admin identity from ingest identity; and
- verify privileges in deployment validation.

### SEC-05 — Backup/restore control is not yet proven

**Severity: Medium; higher if ePHI or required audit evidence is stored**

Persistent volumes protect against Pod recreation, not VM/storage/host loss. A backup on the same VM/disk is not an independent backup.

Remediation:

- approve RPO/RTO;
- back up required ClickHouse/NATS/recovery material to an independent failure domain;
- protect backup credentials/data;
- test full restore into a clean environment; and
- retain evidence of restore exercises.

See `BACKUP_RECOVERY.md`.

### SEC-06 — Single-node infrastructure remains a single failure domain

**Severity: Medium operational risk**

One physical host and one GeoTagger VM contain the K3s control plane, API, NATS, worker, ClickHouse and tunnel connector.

HPA protects against API-process load/failure; it does not protect against VM, host, power, storage or local-network failure.

Remediation depends on the required availability target. Do not add multi-node stateful complexity until an RTO/RPO/SLO requires it.

### SEC-07 — Privileged-human MFA/access governance is external to GeoTagger

**Severity: Medium; High if administrators can access ePHI or regulated audit data**

The machine API has no human login, so interactive MFA is not part of `/v1/country`. However, Cloudflare, GitHub, Proxmox, Kubernetes administration, SSH/VPN/bastion, backup systems and secret stores can change or expose the service.

Remediation:

- unique named administrator identities;
- MFA for privileged human access where supported;
- prefer phishing-resistant FIDO2/WebAuthn/passkeys/security keys;
- protect recovery methods;
- least privilege;
- periodic access review; and
- documented break-glass process.

See `MFA_AND_IDENTITY.md` for the current-HIPAA-rule versus proposed-MFA-rule distinction.

### SEC-08 — Compliance requires non-code controls and vendor review

**Severity: High if ePHI enters the service path; informational otherwise**

Technical controls in this repository do not establish HIPAA compliance.

Before ePHI use, the regulated entity/business associate must address, as applicable:

- formal risk analysis and risk management;
- security responsibility and workforce controls;
- vendor/BAA review, including Cloudflare/backup providers where they handle ePHI;
- access-control/identity procedures;
- backup/contingency testing;
- incident and breach procedures;
- audit review/retention;
- documentation retention; and
- periodic technical/nontechnical evaluation.

See `HIPAA_READINESS.md`.

### SEC-09 — Load test shared the target's CPU

**Severity: Informational / availability planning**

k6 ran on the same 9-vCPU VM as the target stack. That does not create a confidentiality vulnerability, but it can understate capacity and distort HPA/CPU behavior.

Remediation: run the next capacity test from an external generator and use origin/audit latency metrics. See `PERFORMANCE_TUNING.md`.

## Release security checklist

```text
[ ] no plaintext production secrets are committed
[ ] invalid/missing bearer token returns 401
[ ] valid private target returns 422
[ ] malformed/oversized JSON is rejected
[ ] every response has X-Request-ID
[ ] matching audit row reaches ClickHouse
[ ] raw IP is absent when AUDIT_IP_MODE=hmac
[ ] authentication-failure audit does not retain target IP
[ ] API fails closed if required durable audit publication cannot complete
[ ] NATS is not Internet/LAN exposed
[ ] ClickHouse is not Internet/LAN exposed
[ ] port 9090 is not publicly exposed
[ ] NetworkPolicy enforcement is active
[ ] Kubernetes Secrets encryption is verified
[ ] containers run with expected numeric non-root identities
[ ] capabilities remain dropped
[ ] read-only rootfs remains enabled where designed
[ ] production image references are approved
[ ] caller credential inventory/rotation status is current
[ ] privileged administrator MFA/access review is current
[ ] backup/restore evidence is current
[ ] incident-response contacts/runbook are current
[ ] data classification and HIPAA gate match the environment
```

## Residual risk

For a controlled internal machine service, the current code and deployment model provide a reasonable security foundation. Before a regulated production deployment, the unresolved items above must be dispositioned through the organization's risk-management process, with evidence for vendor contracts, identity/access, backups, incident response, audit handling and recovery.
