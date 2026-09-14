# Data Classification and Handling

## Purpose

This document classifies the data GeoTagger handles and defines minimum handling rules. It covers the richer City + ASN response model introduced by `/v1/lookup` and `/v1/me` as well as the minimized durable audit record.

Classification must be reviewed again if GeoTagger is integrated into a workflow containing patient identifiers, clinical information, authoritative workforce identity, or other regulated data.

## Data categories

| Data | Example | Default storage | Classification / handling note |
|---|---|---|---|
| Queried public IP | `8.8.8.8` | not retained raw by default | Sensitive identifier/contextual data |
| IP HMAC | `5a3ec8b2...` | ClickHouse audit | Sensitive pseudonymous/correlatable data |
| Caller ID | `azure-prod` | ClickHouse audit | Internal security/audit metadata |
| Request ID | 128-bit hex | response + audit | Internal correlation metadata |
| Country result | `Saint Kitts and Nevis` | response + audit | Low sensitivity alone; context dependent |
| Region/city/postal code | `Basseterre` | response only by default | Approximate location/context data; potentially sensitive when linked to a person/workflow |
| Estimated coordinates | latitude/longitude | response only by default | Approximate location data; potentially sensitive; not GPS |
| Accuracy radius | `50 km` | response only by default | Quality/uncertainty metadata |
| Timezone | `America/St_Kitts` | response only by default | Location/context metadata |
| Network/CIDR | `76.76.168.0/22` | response only by default | Network metadata; context dependent |
| ASN and organization | `AS11139`, organization | response only by default | Network/provider metadata; context dependent |
| IP classification | IPv4/public/private/etc. | response only by default | Operational metadata |
| City/ASN database versions | GeoLite2 build metadata | response + combined audit version | Operational/provenance metadata |
| Status/outcome | `200`, `ok` | audit | Internal audit metadata |
| Lookup latency | microseconds | response + audit | Operational metadata |
| Caller bearer secret | `key-id.secret` | client only; digest server-side | Secret / restricted |
| API secret digest | SHA-256 digest | Kubernetes Secret | Restricted authentication material |
| Audit HMAC key | random secret | Kubernetes Secret | Secret / restricted |
| ClickHouse password | random secret | Kubernetes Secret | Secret / restricted |
| Cloudflare Tunnel token | vendor token | Kubernetes Secret | Secret / restricted |
| MaxMind license key | vendor credential | Kubernetes Secret | Secret / restricted |

## Rich response versus retained audit data

GeoTagger deliberately separates **transient response enrichment** from **durable audit retention**.

The authenticated response may contain:

```text
country / region / city
postal code
estimated coordinates
accuracy radius
timezone
network/CIDR
ASN / organization
source versions
```

The ClickHouse audit record does not store those rich fields by default. It retains the smaller operational record:

```text
request ID
caller ID
HMAC/raw/omitted target-IP representation
country code/name
outcome/status
lookup latency
combined MMDB version metadata
```

This is an intentional minimum-necessary decision. Adding rich location or ASN fields to the audit schema requires a new documented purpose, retention decision and privacy/security review.

## IP geolocation is not precise device location

City, region and coordinates are derived from an IP-geolocation database. They may represent an ISP allocation, network point of presence or approximate area rather than the physical device.

Consumers must:

- not label IP-derived coordinates as GPS;
- not claim that the result proves a person's physical location;
- use `accuracy_radius_km` when location precision matters;
- tolerate missing values; and
- expect values to change after database updates.

A more detailed response is not automatically a more accurate response.

## Context changes classification

A value with low sensitivity in isolation can become sensitive or regulated when combined with another system.

For example:

```text
public IP + ASN + country
```

may be ordinary security telemetry. But:

```text
patient account + timestamp + public IP + estimated city
```

may become part of an ePHI or otherwise sensitive workflow depending on the parties and purpose.

Do not determine the classification of GeoTagger data solely from its own schema. Review the complete upstream/downstream data flow.

## Default handling rules

### Raw queried IP

- Do not retain it by default.
- Do not put it in ordinary application logs.
- Do not add it to exception traces unless explicitly justified.
- Use `AUDIT_IP_MODE=hmac` unless an approved requirement states otherwise.
- If `raw` mode is enabled, document the purpose, owner, retention, access controls and approval.

### HMAC IP representation

HMAC reduces exposure but does not make the value anonymous.

- Treat it as sensitive audit data.
- Restrict ClickHouse access.
- Protect the HMAC key separately from the audit dataset.
- Record HMAC-key rotation events.
- Do not publish/export HMAC values casually.

### Rich geolocation/network response

- Return only to authenticated callers.
- Do not persist the response wholesale merely for convenience.
- Avoid copying city/coordinates into ordinary access logs.
- Do not use IP-derived location as the sole basis for high-impact clinical, identity, employment, disciplinary or law-enforcement decisions.
- If an upstream product persists rich response fields, that product must define its own purpose, retention and access policy.

### Authentication and infrastructure secrets

- Never commit plaintext production secrets to Git.
- Never embed them in screenshots, tickets, Markdown examples or logs.
- Limit Kubernetes Secret access to required administrators/workloads.
- Keep recovery copies only in an approved secret/recovery store.
- Rotate after suspected disclosure.
- Use a unique caller credential per integrating system.

### Audit metadata

- Restrict write paths to the application/worker.
- Define who may query/export audit data.
- Govern administrator access where required.
- Apply approved retention and destruction rules.
- Use request IDs as the primary technical correlation handle.

## Minimum necessary principle

A rich IP lookup still needs only an IP address as input:

```json
{"ip":"8.8.8.8"}
```

Do not add patient name, medical-record number, diagnosis, encounter information, free-text notes or other clinical context to the request. The IP lookup does not require them.

A caller that must associate a lookup with a patient/transaction should keep that association in its authoritative upstream system and use the GeoTagger `X-Request-ID` for technical correlation where feasible.

`GET /v1/me` needs no IP body; it derives the caller address from the trusted ingress path. It must not be used to infer a physical device location beyond the limitations documented above.

## Recommended logging pattern

Appropriate application logging:

```text
timestamp=...
request_id=...
caller_id=azure-prod
status=200
country_code=KN
latency_us=...
```

Avoid:

```text
bearer_token=...
raw_ip=...
latitude=...
longitude=...
patient_id=...
clinical_context=...
```

unless an approved operational requirement explicitly needs the additional field and the logging system is governed for that data.

## Retention

The current ClickHouse audit table has a 30-day TTL. That is an application default, not a universal legal or HIPAA retention period.

Before regulated use, document:

- operational purpose for retained audit data;
- legal/contractual retention requirements;
- whether the records form part of required compliance documentation;
- archival requirements;
- deletion/destruction method; and
- whether the 30-day TTL remains appropriate.

Do not extend retention solely because richer fields are now available.

## Export rules

An audit export should record:

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

A rich-response export performed by a calling application should be governed by that application's classification and purpose as well.

## Environment separation

Production secrets and audit data must not be copied into development/test environments as sample data.

Use synthetic/documentation ranges where a test does not need real GeoIP content:

```text
192.0.2.0/24
198.51.100.0/24
203.0.113.0/24
```

Those documentation ranges are intentionally not suitable for testing positive public GeoIP results. Use approved public fixtures when a real City/ASN record is required.

If an incident requires copying production evidence into an investigation environment, protect the destination at least as strongly as the source and follow incident/evidence procedures.

## Data-flow review triggers

Re-run classification/privacy review if any of the following changes:

- additional enrichment fields are added;
- VPN/proxy/Tor/reputation datasets are introduced;
- raw IP retention is enabled;
- rich City/ASN fields are added to ClickHouse;
- user/patient identifiers are added;
- logs move to a new provider;
- backups move to a third party;
- Cloudflare/service tier changes;
- audit data is replicated externally;
- new analytics/ML uses are introduced; or
- GeoTagger becomes directly integrated with patient/clinical records.
