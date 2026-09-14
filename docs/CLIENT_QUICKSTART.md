# Client Quickstart

This is the shortest path from a client tool to a verified GeoTagger request.

Base URL:

```text
https://geo.itsjosiahdavis.dev
```

Authentication header:

```http
Authorization: Bearer <key-id.secret>
```

For JSON POST requests also send:

```http
Content-Type: application/json
```

## Yaak

GeoTagger includes an OpenAPI 3.1 contract at the repository root:

```text
openapi.yaml
```

Import that file into Yaak as an OpenAPI workspace/collection. Set the Bearer authentication value to the issued GeoTagger client token. The imported contract includes all public lookup endpoints, request bodies, response schemas and common error responses.

For a manual request in Yaak:

```text
Method: POST
URL:    https://geo.itsjosiahdavis.dev/v1/lookup
Auth:   Bearer Token
Body:   JSON
```

Body:

```json
{
  "ip": "8.8.8.8"
}
```

Do not add both Yaak's Bearer authentication and a second manual `Authorization` header. Use one or the other.

## cURL

Full lookup:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' \
  https://geo.itsjosiahdavis.dev/v1/lookup
```

Query-string form:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  'https://geo.itsjosiahdavis.dev/v1/lookup?ip=8.8.8.8'
```

Caller lookup:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  https://geo.itsjosiahdavis.dev/v1/me
```

Country-only compatibility lookup:

```bash
curl -sS \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' \
  https://geo.itsjosiahdavis.dev/v1/country
```

## Useful test addresses

Use several address classes when validating a deployment:

| Address | Purpose | Typical result |
|---|---|---|
| `8.8.8.8` | public IPv4 / Google | `200` with US intelligence |
| `208.67.222.222` | public IPv4 / OpenDNS | `200` when present in installed MMDBs |
| `2001:4860:4860::8888` | public IPv6 / Google | `200` when present in installed MMDBs |
| `1.1.1.1` | public anycast / Cloudflare | `200` or legitimate `404`, depending on MMDB snapshot |
| `9.9.9.9` | public anycast / Quad9 | `200` or legitimate `404`, depending on MMDB snapshot |
| `10.0.0.1` | RFC1918 private IPv4 | `422` by default |
| `192.168.1.1` | RFC1918 private IPv4 | `422` by default |
| `127.0.0.1` | loopback | `422` by default |
| `::1` | IPv6 loopback | `422` by default |
| `not-an-ip` | malformed input | `422` |

A valid public IP returning `404` is a data-coverage result, not automatically a service failure.

## Verify the durable audit path

Every response carries:

```text
X-Request-ID: <32-hex-character ID>
```

For an end-to-end deployment check, capture that request ID and confirm the matching audit event reaches ClickHouse. A normal successful lookup means JetStream already acknowledged the event; finding the row in ClickHouse additionally proves the worker/persistence path completed.

## Interpreting location fields

City, region and latitude/longitude are IP-geolocation estimates. They must not be presented as GPS/device location. Use the returned `accuracy_radius_km` when displaying or reasoning about location precision.

## Secrets

Treat the bearer token as a password:

- do not commit it;
- do not paste it into screenshots or public tickets;
- do not put it into frontend/browser source code;
- use one credential per integration/environment where practical; and
- rotate/revoke it after suspected disclosure.
