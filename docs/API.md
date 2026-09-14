# GeoTagger API Guide

## Purpose

GeoTagger is a self-hosted IP intelligence service. It resolves public IPv4 and IPv6 addresses against local MaxMind GeoLite2 City and ASN databases and records a durable privacy-preserving audit event for each authenticated lookup.

Base URL:

```text
https://geo.itsjosiahdavis.dev
```

No per-request geolocation call is made to MaxMind, IPinfo, or another external lookup service. The databases are downloaded by a scheduled updater and queried locally through memory-mapped MMDB readers.

## Authentication

Every public API endpoint requires:

```http
Authorization: Bearer <key-id>.<secret>
```

Only the SHA-256 digest of the secret is configured server-side. Generate a caller credential with:

```bash
go run ./cmd/keygen azure-prod
```

Never commit or log the client token.

---

## Full IP intelligence lookup

### `POST /v1/lookup`

Recommended machine-to-machine interface.

```http
POST /v1/lookup
Authorization: Bearer <client-token>
Content-Type: application/json

{"ip":"8.8.8.8"}
```

Example:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' \
  https://geo.itsjosiahdavis.dev/v1/lookup
```

Response shape:

```json
{
  "ip": "8.8.8.8",
  "network": "8.8.8.0/24",
  "continent": {
    "code": "NA",
    "name": "North America"
  },
  "country": {
    "code": "US",
    "name": "United States"
  },
  "region": {
    "code": "CA",
    "name": "California"
  },
  "city": "Mountain View",
  "postal_code": "94035",
  "location": {
    "latitude": 37.386,
    "longitude": -122.0838,
    "accuracy_radius_km": 20,
    "timezone": "America/Los_Angeles"
  },
  "asn": {
    "number": 15169,
    "organization": "Google LLC"
  },
  "classification": {
    "public": true,
    "private": false,
    "loopback": false,
    "link_local": false,
    "ip_version": "IPv4"
  },
  "source": {
    "city_database": "GeoLite2-City@<build-time>",
    "asn_database": "GeoLite2-ASN@<build-time>"
  },
  "lookup_latency_us": 90,
  "request_id": "..."
}
```

The values above are illustrative. GeoIP city/region/coordinates are database estimates, not device GPS. Consumers should use `accuracy_radius_km` and must not represent IP-derived coordinates as precise physical location.

### `GET /v1/lookup?ip=...`

Convenient shell/browser-style interface using the same authentication and response contract:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  'https://geo.itsjosiahdavis.dev/v1/lookup?ip=8.8.8.8'
```

Use POST for normal application integrations when request bodies are easier to control/log safely.

---

## Look up the caller's public address

### `GET /v1/me`

This is GeoTagger's equivalent of the common `curl ipinfo.io` workflow:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  https://geo.itsjosiahdavis.dev/v1/me
```

The service resolves the authenticated caller's observed public IP and returns the same full intelligence structure as `/v1/lookup`.

For the production Cloudflare Tunnel path, the application prefers `CF-Connecting-IP`. It can fall back to the first `X-Forwarded-For` value or the direct remote address for trusted/internal testing. The origin must remain restricted to the intended Cloudflare/internal path so untrusted direct clients cannot spoof forwarded-address headers.

---

## Backward-compatible country endpoint

### `POST /v1/country`

The original minimal API remains supported:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' \
  https://geo.itsjosiahdavis.dev/v1/country
```

Response:

```json
{"country":"United States"}
```

New integrations that need network/provider or location metadata should use `/v1/lookup` instead.

---

## Data fields

| Field | Source | Meaning |
|---|---|---|
| `ip` | request/observed client | normalized queried address |
| `network` | City or ASN MMDB match | network prefix associated with the database record |
| `continent` | GeoLite2 City | continent code/name |
| `country` | GeoLite2 City | country code/name |
| `region` | GeoLite2 City | first subdivision/region code/name |
| `city` | GeoLite2 City | estimated city |
| `postal_code` | GeoLite2 City | postal code when supplied by the database |
| `location.latitude/longitude` | GeoLite2 City | estimated geolocation coordinates |
| `accuracy_radius_km` | GeoLite2 City | radius expressing approximate geolocation precision |
| `timezone` | GeoLite2 City | database timezone |
| `asn.number` | GeoLite2 ASN | autonomous system number |
| `asn.organization` | GeoLite2 ASN | autonomous-system organization |
| `classification` | local Go address classification | public/private/loopback/link-local/IP version |
| `source` | local MMDB metadata | exact City and ASN database type/build time used |
| `lookup_latency_us` | GeoTagger | time spent in local MMDB lookup work |
| `request_id` | GeoTagger | correlation ID also present in the response header/audit event |

Not every public IP has every field. Missing optional fields are omitted or left empty rather than fabricated.

## Public-address policy

By default, requested targets must be public global-unicast addresses. Private, loopback, and link-local addresses are rejected before the MMDB lookup.

Example:

```json
{"ip":"10.0.0.1"}
```

returns:

```http
HTTP/2 422
```

```json
{"error":"IP address is not a permitted public address"}
```

`/v1/me` similarly rejects a non-public observed address. In the normal Cloudflare production path it should receive the caller's public Internet address.

## Response headers

Every API response includes:

```text
X-Request-ID: <server-generated-id>
Cache-Control: no-store
X-Content-Type-Options: nosniff
```

The full lookup response also includes the request ID in JSON for clients where reading response headers is inconvenient.

## Error contract

| Status | Meaning |
|---:|---|
| 200 | lookup completed |
| 400 | malformed JSON, missing query parameter, or caller IP unavailable |
| 401 | missing/invalid bearer token |
| 413 | body exceeds configured limit |
| 422 | invalid or disallowed IP |
| 404 | no usable City/ASN intelligence record for the address |
| 500 | local database lookup failure |
| 503 | durable audit transport unavailable |

A valid public IP may legitimately return `404` if the installed GeoLite2 snapshot does not contain a usable record.

## Retry behavior

- `400`, `401`, `413`, `422`: do not retry unchanged input.
- `404`: treat as data unavailable for the installed database snapshot.
- `500`/`503`: retry only with bounded exponential backoff and a total timeout.

Do not weaken the durable audit guarantee to hide transient `503` responses.

## Audit/privacy behavior

The default audit mode stores an HMAC-SHA256 representation of the queried IP rather than the raw IP. A successful response is not returned until NATS JetStream acknowledges the audit event. ClickHouse persistence is asynchronous after that durable queue acknowledgement.

The audit row records country-level result metadata and a combined City/ASN database version string. Rich city/coordinate/ASN response data is not added to the audit schema by this feature, keeping retained audit data smaller and reducing unnecessary location-data retention.

## IPv6

All three lookup interfaces support IPv6. Example:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  'https://geo.itsjosiahdavis.dev/v1/lookup?ip=2001:4860:4860::8888'
```

## Internal health and metrics

The API Service exposes only port `8080`. The administrative listener on `9090` remains internal and provides:

```text
GET /healthz
GET /readyz
GET /metrics
```

Use `kubectl port-forward` or another administrator-only path to inspect it.
