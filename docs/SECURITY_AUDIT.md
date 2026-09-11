# GeoTagger Internal Security Audit

## Scope and status

This is an internal engineering security review of the GeoTagger repository and the deployed single-node K3s architecture. It is not a third-party penetration test, legal opinion, HIPAA certification, or substitute for organizational risk analysis.

Reviewed areas include:

- API authentication and request validation;
- audit/privacy handling;
- Kubernetes workload security;
- network exposure and NetworkPolicy;
- secret handling;
- ClickHouse/NATS deployment;
- Cloudflare Tunnel ingress; and
- operational availability/capacity risks.

## Executive summary

The deployed design has a strong baseline for a small self-hosted machine API: no direct inbound origin exposure, bearer credentials stored as digests, bounded request parsing, private-address filtering, HMAC-based IP audit privacy, durable fail-closed audit publication, default-deny ingress, internal-only data services, non-root/read-only application containers, and encrypted Kubernetes Secrets when the recommended K3s configuration is used.

No critical code-execution or authentication-bypass issue was identified in this review.

The most important remaining work is operational hardening rather than rewriting the lookup service: formal credential lifecycle, egress policy, immutable image pinning, stronger ClickHouse least privilege, multi-node/backup planning if availability requirements increase, and completion of the required compliance/vendor controls if regulated data is introduced.

## Existing controls

### Authentication

Caller credentials use:

```text
key-id.secret
```

The server stores only the SHA-256 digest of each secret. Verification uses constant-time comparison. Unknown key IDs still perform comparable hashing work before rejection.

The key generator creates 32 bytes of cryptographically random secret material and emits a URL-safe token plus its server-side digest entry.

Assessment: **Good**.

### Request validation

The API:

- limits request-body size;
- rejects unknown JSON fields;
- requires a single JSON object;
- validates IP syntax;
- normalizes IPv4-mapped addresses; and
- rejects private, loopback, and link-local targets unless explicitly configured otherwise.

Assessment: **Good**.

### Audit privacy

Default mode stores HMAC-SHA256 of the queried IP instead of the raw address. Authentication failures are audited without retaining the requested target IP. Request IDs allow event correlation without placing the IP into ordinary access logs.

Assessment: **Good**.

### Audit durability

Successful/normal lookup paths require a durable NATS JetStream publish acknowledgement before returning. The production stream configuration uses:

```text
WorkQueue retention
MaxAge = 0
MaxBytes = 8 GiB
DiscardNew
```

This prevents old, unpersisted audit records from disappearing solely because of age. When capacity is exhausted, the system fails closed rather than silently dropping the oldest required audit data.

Assessment: **Strong** for a single-node design.

### Network exposure

The namespace applies default-deny ingress and explicitly permits only:

```text
cloudflared -> API :8080
API/worker  -> NATS :4222
worker      -> ClickHouse :8123
```

NATS and ClickHouse use ClusterIP services. The API's port 9090 administrative listener is not exposed by the public Service. Cloudflare Tunnel removes the need for an inbound WAN/NAT port.

Assessment: **Good**.

### Container hardening

Application manifests use non-root execution, dropped Linux capabilities, disabled privilege escalation, and read-only root filesystems where practical.

A deployment-time issue was found because `runAsNonRoot: true` did not always provide Kubernetes admission with a verifiable numeric non-root identity. The validated fix pins:

```text
API/worker UID:GID 65532:65532
NATS       UID:GID 1000:1000
```

This fix is tracked upstream so future deployments do not depend on a local manifest patch.

Assessment after fix: **Good**.

### Secrets at rest

The repository recommends K3s secrets encryption using the secretbox provider. Credentials are injected through Kubernetes Secrets rather than committed into manifests.

Assessment: **Good, provided the production K3s configuration actually enables secrets encryption and host access is controlled**.

## Findings and recommendations

### SEC-01 — No explicit egress-deny policy

**Severity: Medium**

Current NetworkPolicies default-deny namespace ingress but do not default-deny egress. A compromised workload can therefore initiate outbound connections according to the node/CNI defaults.

Recommendation:

- add a namespace default-deny egress policy;
- allow DNS explicitly;
- allow cloudflared to reach Cloudflare;
- allow the MMDB updater to reach only required MaxMind/download endpoints where practical;
- allow API/worker only the internal destinations they require; and
- verify K3s/CNI behavior before rollout to avoid accidentally blocking cluster DNS or required control traffic.

### SEC-02 — Static bearer keys have no built-in lifecycle metadata

**Severity: Medium**

Application bearer credentials are strong secrets, but the current `API_KEYS` format is effectively a static allowlist. It does not provide built-in expiration, last-used tracking, scoped permissions, or a revocation API.

Recommendation:

- maintain documented issue/owner/purpose/creation/expiry metadata outside the secret itself;
- rotate machine tokens on a defined schedule;
- give each integration its own key ID;
- revoke compromised callers by removing their digest and rolling the secret; and
- consider mTLS or Cloudflare API-level machine authentication as a second factor when the client environment supports it.

Do not put a browser-based Cloudflare Access login in front of machine clients unless the caller is designed to satisfy that flow.

### SEC-03 — Container image reproducibility is mixed

**Severity: Medium**

The ClickHouse image is digest-pinned, which is excellent. Other runtime images use version tags rather than immutable digests. A tag can be republished upstream, so a future redeploy may not be byte-for-byte identical.

Recommendation:

- pin production runtime images by digest after validation;
- retain human-readable version comments/tags next to the digest; and
- update digests through reviewed dependency PRs.

### SEC-04 — ClickHouse SQL least privilege can be tightened

**Severity: Low/Medium**

NetworkPolicy limits ClickHouse ingress to the audit worker, and the worker uses a dedicated `geotagger_ingest` identity. However, the deployment does not currently demonstrate a narrowly scoped SQL RBAC grant limited to only the required database/table operations.

Recommendation:

- create explicit ClickHouse roles/users;
- grant only the insert/select/metadata privileges actually required by the worker/operations path;
- prevent schema/admin operations from the ingest identity; and
- keep administrative credentials separate from application secrets.

### SEC-05 — Single-node state is an availability/security risk

**Severity: Medium operational risk**

NATS, ClickHouse, K3s control plane, the VM, the Proxmox host, local power, and the local network are single failure domains. HPA protects against API-process load but does not provide infrastructure HA.

Recommendation:

- define recovery time and recovery point objectives;
- automate/configure backups;
- test ClickHouse restore procedures;
- document how JetStream state is recovered;
- retain Kubernetes manifests/secrets recovery material securely; and
- move to separate failure domains only if the availability requirement justifies the added complexity.

### SEC-06 — Compliance depends on non-code controls

**Severity: High if ePHI/regulated data enters the path; otherwise informational**

The architecture contains useful technical safeguards, but HIPAA or equivalent compliance is not established by code alone.

Before transmitting ePHI through this service, complete:

- formal risk analysis;
- vendor/BAA review, including Cloudflare where applicable;
- access-control and account-review procedures;
- incident response;
- backup/restore validation;
- retention/destruction policy;
- workforce/device controls; and
- security monitoring/review.

Do not label the deployment "HIPAA compliant" solely because the application uses encryption, audit logs, or private networking.

### SEC-07 — Load generator and target shared a CPU failure domain

**Severity: Informational**

The production benchmark ran k6 on the same VM as the stack. This is not a confidentiality/integrity vulnerability, but it can produce misleading capacity assumptions. Overstating capacity is an availability risk.

Recommendation: repeat the ceiling test from an external generator and base production rate limits/SLOs on that result.

## Security test checklist

Before each production release, verify at minimum:

```text
[ ] no secrets are committed
[ ] invalid/missing bearer token -> 401
[ ] private IP target -> 422
[ ] malformed JSON/IP is rejected
[ ] response carries X-Request-ID
[ ] matching audit row is present
[ ] raw IP is absent when AUDIT_IP_MODE=hmac
[ ] authentication-failure audit does not contain target IP
[ ] API cannot succeed normally when durable audit publication is unavailable
[ ] NATS is not Internet/LAN exposed
[ ] ClickHouse is not Internet/LAN exposed
[ ] port 9090 is not publicly exposed
[ ] NetworkPolicy enforcement is active
[ ] Kubernetes Secrets encryption is enabled
[ ] containers run with expected numeric non-root UID/GID
[ ] capabilities remain dropped
[ ] read-only rootfs remains enabled where designed
[ ] backup and restore procedures are current
```

## Residual risk statement

With the numeric UID/GID deployment fix applied, the current GeoTagger implementation is suitable as a hardened prototype/internal machine service when operated within its documented single-node availability boundary. The largest remaining risks are credential/operations governance, unrestricted pod egress, single-node availability, and compliance obligations external to the codebase.
