# Changelog

All notable GeoTagger changes are recorded here. Dates use UTC calendar dates. This file describes repository changes; deployment-specific changes should also be recorded in the environment's change-management system.

## [Unreleased]

### Added

- Origin-side request-latency Prometheus histogram: `geotagger_request_latency_microseconds`.
- Durable JetStream publish-latency histogram: `geotagger_audit_publish_latency_microseconds`.
- HIPAA readiness/control matrix with an explicit pre-ePHI production gate.
- Data-classification and handling policy.
- Backup/recovery runbook and recovery evidence template.
- Security incident-response runbook.
- Performance tuning guide covering HPA behavior, caching/Redis decisions, benchmark methodology and future scaling options.

### Changed

- API Deployment baseline increased from one to two replicas to reduce cold burst/rollout sensitivity.
- API CPU request increased from `250m` to `750m` so the HPA's 65% utilization target reflects meaningful CPU pressure instead of scaling toward eight Pods at roughly 1.3 aggregate API cores.
- k6 treats HTTP `404 country not found` as an expected application result for valid public fixture IPs, removing the historical ~25% false failure baseline caused by GeoLite2 snapshot coverage.
- Audit worker reuses its batch row slice between flushes.
- ClickHouse batch writer pre-sizes the NDJSON buffer to reduce repeated allocations/copies.
- Kubernetes and architecture documentation Mermaid diagrams were rewritten with GitHub-safe quoted labels, including filesystem paths.
- Project documentation language was tightened to distinguish implemented technical safeguards from compliance, certification and availability claims.

### Security / Compliance

- Clarified that GeoTagger's public API is machine-to-machine and does not require interactive MFA on every API call.
- Added an administrative MFA policy: human access to Cloudflare, GitHub, Proxmox, Kubernetes administration, backup/secret systems and any future human admin UI should use MFA where supported and required by the organization's risk-management policy.
- Documented that the currently effective HIPAA Security Rule requires appropriate person/entity authentication; the HHS Security Rule modernization proposal would require MFA with limited exceptions. GeoTagger adopts MFA for privileged human administration as a forward-looking baseline without claiming the proposal is already final law.

## [2026-09-12]

### Added

- Full project overview and production architecture documentation.
- Kubernetes/K3s guide explaining Pods, OCI containers, containerd, Deployments, StatefulSets, Services, HPA, PVCs, probes, CronJobs, Secrets and NetworkPolicy.
- API integration guide.
- Performance/capacity report from the first deployed public-path load test.
- Internal security audit and release checklist.

### Fixed

- Pinned explicit numeric non-root identities required by strict Kubernetes admission/runtime checks:
  - API/worker: UID/GID `65532:65532`.
  - NATS: UID/GID `1000:1000`.
- Corrected stale documentation that described JetStream as having a seven-day age limit. The deployed safety configuration is `MaxAge=0`, `MaxBytes=8 GiB`, `DiscardNew`.

## [2026-09-11]

### Changed

- JetStream audit durability was hardened to fail closed under capacity pressure:
  - `Retention=WorkQueue`;
  - `MaxAge=0`;
  - `MaxBytes=8 GiB`;
  - `DiscardNew`.
- Existing stream configuration is reconciled on API startup so a stale age-expiry setting is removed rather than silently retained.

### Validated

- Public authenticated lookup succeeded end to end through Cloudflare Tunnel, Kubernetes Service, API, local GeoLite2 lookup, durable JetStream publication, worker and ClickHouse.
- Verified request ID `c75bb0a033c44dd87a9969df059dd6b4` returned `United States` for `8.8.8.8` and appeared in ClickHouse with HMAC-mode IP storage.
- Verified private target `10.0.0.1` is rejected with HTTP 422.
- Initial public-path load test identified CPU contention as the dominant observed bottleneck; HDD utilization remained below roughly 3%.
