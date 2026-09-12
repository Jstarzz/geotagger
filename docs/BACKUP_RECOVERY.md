# Backup and Recovery Runbook

## Purpose

This runbook defines what must be backed up, where copies must live, how recovery is verified, and how recovery evidence is recorded. The current deployment is single-node; backups are therefore part of the availability design, not an optional convenience.

## Recovery objectives

The service owner must approve explicit targets before production use:

```text
RPO: maximum acceptable data loss interval
RTO: maximum acceptable service restoration interval
```

Do not copy placeholder values into a production plan. Record the approved values in the environment's operational record and in the HIPAA readiness approval if ePHI is in scope.

## Data inventory

| Asset | Why it matters | Backup requirement |
|---|---|---|
| ClickHouse audit data | Persisted request/audit evidence | Required according to approved retention policy |
| NATS JetStream state | Unpersisted durable audit work | Required if the recovery design must preserve queued, not-yet-inserted events |
| Kubernetes manifests | Rebuild cluster workloads | Keep in Git and retain approved release/commit references |
| Kubernetes Secrets | Required to restore service identity and encryption/HMAC behavior | Maintain protected recovery copy outside Git and outside the failed VM |
| K3s configuration | Cluster settings, including secrets-encryption configuration | Required for consistent rebuild |
| Cloudflare tunnel configuration/token | Public ingress recovery | Protected recovery record required; rotate after suspected exposure |
| GeoLite2 configuration/license key | MMDB refresh | Protect credential; MMDB itself can be re-downloaded if contract/license permits |
| Application images/releases | Deterministic restore | Use immutable image references and preserve release metadata |

## Backup placement

A backup stored only on the GeoTagger VM or its virtual disk does not protect against VM-disk failure.

Minimum topology:

```text
GeoTagger VM
    |
    +-- primary state
    |
    `-- backup export
            |
            v
Independent storage / failure domain
            |
            `-- protected secondary copy where policy requires it
```

For regulated data, confirm that every backup destination and operator is within the approved legal/vendor boundary. If a third-party provider stores ePHI, determine whether a BAA is required before use.

## ClickHouse backup

Use a backup method supported by the deployed ClickHouse version and storage design. The exact command/mechanism must be selected and tested for this environment before production.

The backup procedure must record:

```text
backup ID
timestamp
source ClickHouse version
source schema version
row count / size where practical
checksum or integrity metadata
destination
operator/automation identity
result
```

A backup is not considered validated until a restore test succeeds.

## NATS JetStream recovery

The audit stream is configured to retain unacknowledged work. Recovery planning must decide whether queued audit events are included in the RPO.

If JetStream state is backed up, capture it only using a NATS-supported procedure that preserves stream metadata and storage consistency. Do not copy live storage files blindly while assuming the result is crash-consistent.

At restore time verify:

```text
[ ] stream exists
[ ] retention is WorkQueue
[ ] MaxAge is 0
[ ] MaxBytes is 8 GiB
[ ] discard policy is DiscardNew
[ ] worker consumer can resume
[ ] pending messages drain into ClickHouse
```

## Secrets recovery

Do not store plaintext production secrets in Git, ordinary tickets, chat logs, or unencrypted backup archives.

Recovery material should include only what is required to rebuild the environment and should be protected with access controls independent of ordinary application access.

After any recovery involving possible secret exposure, rotate at least:

- caller API tokens;
- `AUDIT_HMAC_KEY` when policy permits the correlation break;
- `CLICKHOUSE_PASSWORD`;
- Cloudflare Tunnel token; and
- MaxMind license key if exposure is suspected.

Document the effect of HMAC-key rotation: new audit events will no longer correlate to old IP HMAC values unless key-version handling is added.

## Full recovery procedure

1. Restore or provision a clean GeoTagger VM with approved CPU/RAM/storage sizing.
2. Install the approved K3s version and restore the required K3s configuration.
3. Verify secrets encryption is enabled before applying production secrets.
4. Restore protected secrets from the approved recovery store.
5. Deploy the exact approved GeoTagger release/commit using immutable image references.
6. Restore ClickHouse state or import the approved backup.
7. Restore NATS state if required by the selected RPO/recovery design.
8. Restore or re-download the GeoLite2 Country MMDB.
9. Start workloads and verify readiness/liveness.
10. Verify Cloudflare Tunnel connectivity.
11. Run the end-to-end validation sequence below.
12. Record recovery start/end times, data-loss window, validation results and incident/change references.

## End-to-end restore validation

A restore is incomplete until the full path works:

```text
[ ] public DNS resolves
[ ] Cloudflare Tunnel connected
[ ] valid bearer token authenticates
[ ] public IP lookup returns expected country
[ ] X-Request-ID returned
[ ] JetStream accepts durable audit event
[ ] audit worker consumes event
[ ] matching request ID appears in ClickHouse
[ ] HMAC/raw/omit policy matches configured mode
[ ] invalid token returns 401
[ ] private address returns 422
[ ] NATS/ClickHouse are not directly exposed
```

## Restore testing cadence

Set a documented cadence based on risk analysis and service criticality. Run an additional restore test after material changes to:

- storage layout;
- K3s version;
- ClickHouse version;
- NATS version;
- secrets/encryption design;
- backup provider/location; or
- deployment automation.

## Evidence template

```text
exercise date:
environment:
backup ID:
restore target:
operator:
start time:
service restored time:
RTO target:
RTO achieved:
RPO target:
observed data-loss window:
end-to-end request ID:
ClickHouse row verified:
issues found:
corrective actions:
reviewer:
```
