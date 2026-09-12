# Data Classification and Handling

## Purpose

This document classifies the data GeoTagger handles and defines the minimum handling controls for each category. It is written for the current machine-to-machine service and must be reviewed if GeoTagger is integrated into a workflow containing protected health information, patient identifiers, authentication context, or other regulated data.

## Data categories

| Data | Example | Default storage | Classification |
|---|---|---|---|
| Queried public IP | `8.8.8.8` | not retained in raw form by default | Sensitive identifier/contextual data |
| IP HMAC | `5a3ec8b2...` | ClickHouse audit table | Sensitive pseudonymous/correlatable data |
| Caller ID | `azure-prod` | ClickHouse audit table | Internal security/audit metadata |
| Request ID | 128-bit hex ID | response + audit row | Internal correlation metadata |
| Country result | `United States` | response + audit row | Low sensitivity alone; context dependent |
| Status/outcome | `200`, `ok` | audit row | Internal audit metadata |
| Lookup latency/MMDB version | `56 us`, database version | audit row | Operational metadata |
| Caller bearer secret | `key-id.secret` | client only; digest server-side | Secret / restricted |
| API secret digest | SHA-256 digest | Kubernetes Secret | Restricted authentication material |
| Audit HMAC key | random secret | Kubernetes Secret | Secret / restricted |
| ClickHouse password | random secret | Kubernetes Secret | Secret / restricted |
| Cloudflare Tunnel token | vendor token | Kubernetes Secret | Secret / restricted |
| MaxMind license key | vendor credential | Kubernetes Secret | Secret / restricted |

## Context changes classification

A value that is low sensitivity in isolation can become regulated or sensitive when linked to another system.

Example:

```text
public IP + country
```

may be ordinary operational data in one application, but:

```text
patient account + timestamp + public IP + country
```

may become part of a protected health-information context when handled by a covered entity/business associate.

Do not classify GeoTagger data solely by looking at the GeoTagger schema. Review the end-to-end workflow.

## Default handling rules

### Raw queried IP

- Do not retain by default.
- Do not place in ordinary access logs.
- Do not include in exception traces unless explicitly required and approved.
- Use `AUDIT_IP_MODE=hmac` unless an approved requirement states otherwise.
- If `raw` mode is enabled, document why, who approved it, retention, access controls, and downstream uses.

### HMAC IP representation

HMAC mode reduces exposure but remains correlatable data.

- Treat the value as sensitive audit data.
- Restrict ClickHouse access.
- Do not publish or export HMAC values casually.
- Protect the HMAC key separately from the audit dataset.
- Record key rotation events.

### Authentication and infrastructure secrets

- Never commit plaintext production secrets to Git.
- Never embed them in Markdown examples, screenshots, tickets, or application logs.
- Limit Kubernetes Secret access to required administrators/workloads.
- Keep recovery copies in an approved secret/recovery store.
- Rotate after suspected disclosure.
- Prefer unique credentials per caller/system.

### Audit metadata

- Restrict write paths to the application/worker.
- Define who may query/export audit data.
- Log or otherwise govern privileged administrative access where required.
- Apply approved retention and destruction rules.
- Preserve request IDs as the primary correlation handle.

## Minimum necessary principle

If the service is used in a regulated healthcare workflow, send only the information required for the lookup. The current API requires only:

```json
{"ip":"203.0.113.10"}
```

Do not add patient name, medical-record number, diagnosis, encounter details, free-text notes, or other clinical data to the request because the GeoIP lookup does not need them.

A caller that needs to correlate a lookup to a patient/transaction should keep that mapping in the authoritative upstream system and use `X-Request-ID` for technical correlation where feasible.

## Approved logging pattern

Recommended application log:

```text
timestamp=...
request_id=...
caller_id=azure-prod
status=200
country_code=US
latency_us=...
```

Avoid:

```text
bearer_token=...
raw_ip=...
patient_id=...
clinical_context=...
```

## Retention

Current ClickHouse schema has a 30-day TTL. That is an application default, not a universal legal/compliance retention period.

Before regulated use, document:

- operational need for audit records;
- legal/contractual retention requirements;
- whether audit records are part of a HIPAA-required documentation set;
- archival needs;
- deletion/destruction method; and
- whether the 30-day TTL is appropriate.

Do not extend retention "just in case" without an identified purpose and owner.

## Export rules

Any export of audit data should record:

```text
requester
purpose
fields exported
time range
record count
destination
approval/reference
retention/destruction expectation
```

If regulated data is in scope, use only approved encrypted destinations and vendors/contracts.

## Environment separation

Production secrets and audit data must not be copied into development/test environments as sample data.

Use synthetic values for tests:

```text
198.51.100.10
203.0.113.20
caller-test
```

If a production incident requires copying evidence into an investigation environment, treat the destination at least as sensitively as the source and follow incident/evidence procedures.

## Data-flow review trigger

Re-run the data-classification and privacy review if any of the following changes:

- new API fields;
- raw IP retention is enabled;
- user/patient identifiers are added;
- logs are sent to a new provider;
- backups move to a third party;
- Cloudflare/service tier changes;
- audit data is replicated externally;
- new analytics/ML uses are introduced; or
- GeoTagger becomes directly integrated with patient/clinical records.
