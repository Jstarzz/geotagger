# Security Incident Response Runbook

## Purpose

This runbook defines the technical response for suspected compromise, credential exposure, unauthorized access, audit-pipeline failure, or data exposure involving GeoTagger. It does not determine whether an event is a reportable HIPAA breach; that determination belongs to the organization's designated privacy/security process.

## Severity model

| Severity | Example | Initial action |
|---|---|---|
| SEV-1 | confirmed unauthorized access to regulated data, host compromise, destructive attack, widespread credential compromise | contain immediately; engage security/privacy leadership |
| SEV-2 | suspected credential exposure, unexpected administrative access, audit pipeline unable to preserve required records | restrict affected access and investigate urgently |
| SEV-3 | isolated policy violation, failed attack, vulnerability without evidence of exploitation | investigate, remediate, document |
| SEV-4 | operational anomaly with no security impact | normal operations/change process |

## Detection sources

Potential indicators include:

- unusual 401/403 rates or caller IDs;
- requests from unexpected networks or systems;
- unexpected Kubernetes API/Proxmox administrative activity;
- NATS publish failures or unexpected stream reconfiguration;
- ClickHouse audit gaps or unauthorized queries;
- unexpected Pod/image changes;
- modified Kubernetes Secrets;
- unexplained Cloudflare Tunnel changes;
- GitHub/release provenance mismatch;
- malware/ransomware indicators on the VM or host;
- backup deletion or integrity failure; and
- external notification from a vendor or caller.

## Immediate containment

Choose the least destructive action that stops ongoing harm while preserving evidence.

Possible actions:

```text
compromised caller token
    -> remove/revoke caller digest
    -> deploy secret update
    -> issue replacement credential through approved channel

suspected tunnel-token compromise
    -> rotate Cloudflare Tunnel token
    -> restart cloudflared with new secret

suspected Kubernetes secret compromise
    -> restrict administrative access
    -> rotate affected application/vendor credentials
    -> preserve cluster audit/evidence before broad changes where possible

suspected workload compromise
    -> isolate affected workload/node
    -> preserve logs/runtime evidence
    -> redeploy only from approved immutable image/reference

suspected host compromise
    -> isolate host from untrusted networks
    -> escalate to infrastructure/security owner
    -> do not assume container recreation removes host-level compromise
```

Do not delete logs, Pods, disks, accounts, or audit records solely to make the system appear clean. Evidence preservation and coordinated containment take priority.

## Evidence to preserve

Collect timestamps in UTC and preserve at least:

- incident start/discovery time;
- reporter and responder identities;
- affected caller IDs;
- relevant `X-Request-ID` values;
- Cloudflare logs available under the approved plan;
- API/worker/NATS/ClickHouse logs;
- Kubernetes events and workload manifests;
- relevant Kubernetes administrative/audit evidence if enabled;
- Proxmox/host authentication and system logs;
- GitHub commit/image provenance;
- ClickHouse audit rows;
- NATS stream/consumer state;
- snapshots/backups created for investigation; and
- all containment/rotation actions with timestamps.

Evidence copies must be access-controlled and retained under the organization's incident/evidence policy.

## Credential-compromise procedure

### Caller API token

1. Identify the key ID.
2. Remove the compromised digest from `API_KEYS`.
3. Apply the secret update and verify old token returns `401`.
4. Generate a new token for the caller.
5. Transfer it through an approved confidential channel.
6. Verify the new token works.
7. Search audit records for use of the compromised caller ID during the suspected exposure window.
8. Record findings and affected systems.

### Audit HMAC key

Treat exposure as sensitive because it permits offline correlation/guessing attacks against HMAC values.

1. Determine whether the key was actually accessible to the suspected actor.
2. Preserve evidence before rotation.
3. Rotate according to approved change procedure.
4. Record the rotation timestamp/key version in the incident record.
5. Document that correlation across the rotation boundary changes unless key-version-aware logic exists.

### ClickHouse password

1. Restrict network/administrative access.
2. Rotate the application credential.
3. Update Kubernetes Secret.
4. Restart/reload dependent workload as required.
5. Validate worker inserts and review database access evidence.

### Cloudflare Tunnel token

1. Revoke/rotate at Cloudflare.
2. Update Kubernetes Secret.
3. Restart connector.
4. Verify only expected connectors are active.
5. Validate public path and origin exposure assumptions.

## Audit-pipeline incident

If NATS or ClickHouse cannot satisfy the documented audit guarantee:

1. Determine whether API requests are failing closed as designed.
2. Capture stream configuration and pending counts.
3. Do not change `MaxAge=0`, `DiscardNew`, or durability settings merely to restore throughput.
4. Restore worker/database capacity.
5. Confirm backlog drains.
6. Reconcile request IDs between source logs and ClickHouse where required.
7. Record whether any audit event was lost, duplicated, delayed, or inaccessible.

## Data-exposure assessment inputs

The privacy/security officer may need:

- what data fields were involved;
- whether raw IP or HMAC mode was active;
- whether data could be linked to an individual/health context;
- number of affected records/individuals;
- unauthorized person/entity involved;
- whether data was actually acquired/viewed;
- encryption/security state;
- duration of exposure;
- mitigation/containment completed; and
- vendor/subcontractor involvement.

The software team supplies facts; it does not make the legal breach determination.

## Recovery

Recovery must use a known-good release and validated state.

```text
[ ] affected credentials rotated
[ ] compromised access removed
[ ] approved image/commit restored
[ ] NATS safety configuration verified
[ ] ClickHouse available and audit path verified
[ ] public API smoke test passed
[ ] matching audit row verified
[ ] backups checked
[ ] monitoring/alerts restored
[ ] temporary containment controls reviewed before removal
```

See `BACKUP_RECOVERY.md` for full environment restoration.

## Post-incident review

Within the organization's required timeframe, document:

```text
incident ID:
severity:
discovery method:
root cause:
affected systems/data:
containment:
eradication:
recovery:
credential rotations:
evidence locations:
privacy/breach review reference:
notifications required/completed:
control failures:
corrective actions:
owners/due dates:
lessons learned:
```

Corrective actions should be tracked to closure. Update the threat model, risk analysis, runbooks, tests, and monitoring when the incident reveals a new or underestimated risk.
