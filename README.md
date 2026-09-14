# GeoTagger

GeoTagger is a self-hosted, authenticated IP intelligence API for an on-premises K3s deployment. It resolves public IP addresses from local MaxMind GeoLite2 City and ASN databases, requires durable audit acceptance through NATS JetStream before normal success, and persists audit events asynchronously to ClickHouse.

Production endpoint:

```text
https://geo.itsjosiahdavis.dev
```

Normal lookups do not call MaxMind, IPinfo, or another third-party geolocation API. City and ASN databases are downloaded on a schedule, verified, activated as one local database generation and memory-mapped by the API Pods.

## Documentation

Start with:

- [`docs/README.md`](docs/README.md) — technical/operational handoff
- [`docs/API.md`](docs/API.md) — full lookup API, `/v1/me`, country compatibility endpoint and response schema
- [`docs/PROJECT_OVERVIEW.md`](docs/PROJECT_OVERVIEW.md) — service purpose and components
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — production architecture and diagrams
- [`docs/KUBERNETES.md`](docs/KUBERNETES.md) — K3s/Kubernetes resource model and operations
- [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md) — measured baseline load-test results and rich-endpoint benchmark plan
- [`docs/PERFORMANCE_TUNING.md`](docs/PERFORMANCE_TUNING.md) — applied tuning, Redis/cache decision and scaling roadmap
- [`docs/SECURITY_AUDIT.md`](docs/SECURITY_AUDIT.md) — security review and remediation priorities
- [`docs/DATA_CLASSIFICATION.md`](docs/DATA_CLASSIFICATION.md) — treatment of IP, location, ASN and audit data
- [`docs/HIPAA_READINESS.md`](docs/HIPAA_READINESS.md) — pre-ePHI control matrix and compliance gate
- [`docs/MFA_AND_IDENTITY.md`](docs/MFA_AND_IDENTITY.md) — machine identity and privileged-human MFA policy
- [`CHANGELOG.md`](CHANGELOG.md) — notable changes

## Request path

```text
Authorized caller
    |
    | HTTPS
    v
Cloudflare
    |
    | Cloudflare Tunnel
    v
cloudflared
    |
    v
Kubernetes Service
    |
    v
GeoTagger API Pods
    |          |                 \
    |          |                  \ durable publish + ACK
    v          v                   v
City MMDB   ASN MMDB          NATS JetStream
                                   |
                                   v
                              audit worker
                                   |
                                   v
                               ClickHouse
```

## API

### Full intelligence lookup

```http
POST /v1/lookup
Authorization: Bearer <key-id.secret>
Content-Type: application/json
```

```json
{"ip":"8.8.8.8"}
```

The response can include:

```text
matched network/CIDR
continent
country
region/subdivision
city
postal code
estimated latitude/longitude
accuracy radius
timezone
ASN
ASN organization
IP classification
City/ASN database build metadata
lookup latency
request ID
```

Example shape:

```json
{
  "ip": "8.8.8.8",
  "network": "8.8.8.0/24",
  "country": {"code":"US","name":"United States"},
  "city": "Mountain View",
  "location": {
    "latitude": 37.386,
    "longitude": -122.0838,
    "accuracy_radius_km": 20,
    "timezone": "America/Los_Angeles"
  },
  "asn": {"number":15169,"organization":"Google LLC"},
  "classification": {
    "public": true,
    "private": false,
    "loopback": false,
    "link_local": false,
    "ip_version": "IPv4"
  },
  "source": {
    "city_database": "GeoLite2-City@...",
    "asn_database": "GeoLite2-ASN@..."
  },
  "lookup_latency_us": 90,
  "request_id": "..."
}
```

The values above are illustrative. IP geolocation is approximate; clients must not present city/coordinate output as GPS-precise location. Use `accuracy_radius_km` to communicate uncertainty.

### Shell-style lookup

```bash
curl -sS \
  -H "Authorization: Bearer $API_TOKEN" \
  'https://geo.itsjosiahdavis.dev/v1/lookup?ip=8.8.8.8'
```

### Look up the caller

```bash
curl -sS \
  -H "Authorization: Bearer $API_TOKEN" \
  https://geo.itsjosiahdavis.dev/v1/me
```

`/v1/me` uses the caller IP observed through the intended Cloudflare Tunnel path and returns the same rich response.

### Backward-compatible country endpoint

```http
POST /v1/country
Authorization: Bearer <key-id.secret>
Content-Type: application/json
```

```json
{"ip":"8.8.8.8"}
```

```json
{"country":"United States"}
```

Every response includes `X-Request-ID`. Rich lookup responses also include the same ID in JSON.

Generate a caller credential with:

```bash
go run ./cmd/keygen azure-prod
```

The client receives the generated token. The server stores only the corresponding SHA-256 digest entry.

Private, loopback and link-local target addresses are rejected by default.

## Data sources

Production uses two local MaxMind databases:

```text
GeoLite2-City.mmdb
GeoLite2-ASN.mmdb
```

The same existing `MAXMIND_LICENSE_KEY` downloads both. No second MaxMind/API key is required.

Production bundle layout:

```text
/var/lib/geotagger/mmdb/
├── current -> releases/release-<generation>/
└── releases/
    ├── release-<older>/
    ├── release-<previous>/
    └── release-<current>/
        ├── GeoLite2-City.mmdb
        └── GeoLite2-ASN.mmdb
```

The updater downloads and verifies every configured edition inside a new release directory. Only after the complete bundle passes verification does it atomically replace the single `current` symlink. API Pods therefore see the old complete City+ASN generation or the new complete generation, not a half-updated pair. The updater retains the three newest complete releases for short rollback/forensics headroom.

The API reads through `/data/current/...`, notices the new file generation on its periodic reload check, verifies the replacement readers and swaps them without restarting the Pod.

## Security model

Current technical controls include:

- per-caller machine credentials;
- SHA-256 server-side secret digests and constant-time verification;
- HMAC-SHA256 IP audit mode by default;
- bounded request bodies and strict JSON parsing;
- Cloudflare Tunnel instead of an inbound public origin port;
- default-deny Kubernetes namespace ingress;
- internal-only NATS and ClickHouse Services;
- numeric non-root UIDs for API, worker and NATS;
- dropped Linux capabilities and read-only root filesystems for Go application containers;
- separate internal admin listener for health/readiness/metrics; and
- durable JetStream acceptance before normal lookup success.

The richer City/ASN response is not copied wholesale into ClickHouse. The audit record remains intentionally minimized to caller/request IDs, HMAC-derived IP value, country-level result, outcome/status, lookup latency and database-version metadata.

`/v1/me` relies on Cloudflare forwarding metadata in the intended Tunnel origin path. Do not expose the API origin directly to untrusted clients and then trust arbitrary forwarding headers.

GeoTagger is a machine API, so interactive MFA is not performed on each API request. Privileged human access to Cloudflare, GitHub, Proxmox, Kubernetes administration, backup/secret systems and any future human admin UI should use MFA. See [`docs/MFA_AND_IDENTITY.md`](docs/MFA_AND_IDENTITY.md).

The repository does not claim that the deployment is HIPAA compliant. Before ePHI use, complete the risk analysis, vendor/BAA review, backup/recovery, identity/access, incident-response, audit-retention and other controls in [`docs/HIPAA_READINESS.md`](docs/HIPAA_READINESS.md).

## Kubernetes autoscaling

`deploy/k3s/api.yaml` configures:

```text
minimum API replicas: 2
maximum API replicas: 8
CPU request per API Pod: 750m
CPU limit per API Pod: 2
HPA CPU target: 65% of request
scale-down stabilization: 5 minutes
```

Autoscaling changes Pod count; it does not add hardware. All API Pods currently share the same VM CPU budget.

## Audit durability

JetStream configuration:

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

`MaxAge=0` prevents required unpersisted audit events from expiring solely because they are old. When the 8-GiB limit is reached, `DiscardNew` rejects new required audit events instead of deleting older queued work. The API then fails closed rather than returning an unaudited normal result.

The worker inserts ClickHouse rows in batches and ACKs NATS only after successful persistence. Delivery is at least once; a crash after ClickHouse insert but before NATS ACK can create a duplicate row. Use `request_id` when deduplication matters.

Current ClickHouse audit TTL:

```sql
TTL timestamp + INTERVAL 30 DAY DELETE
```

That TTL is an application default, not a universal HIPAA/legal retention requirement.

## Performance status

The first public-path load test was performed on the earlier country-only lookup path on the same 9-vCPU VM that ran k6 and the full K3s stack:

| Target | Achieved | p95 | p99 | HDD utilization |
|---:|---:|---:|---:|---:|
| 100 RPS | ~100 req/s | 190 ms | 203 ms | <1% |
| 1,000 RPS | ~668 req/s | 2.08 s | 3.81 s | <1% |
| 5,000 RPS | ~1,373 req/s | 14.3 s | 20.3 s | <3% |
| 10,000 RPS | ~322-643 req/s | 6.2-8.2 s | much higher | <1% |

CPU contention appeared before disk saturation. A verified single-MMDB country lookup took 56 microseconds.

The rich endpoint performs City + ASN decoding and emits a larger JSON response, so the old benchmark is a baseline, **not a measured capacity claim for `/v1/lookup`**. Re-run the external-generator benchmark after deploying this feature.

Redis is still not recommended by default. Both datasets are local memory-mapped MMDBs; adding Redis would create another network hop and stateful dependency before evidence shows MMDB decode cost is a material bottleneck. See [`docs/PERFORMANCE_TUNING.md`](docs/PERFORMANCE_TUNING.md).

The API exports separate origin-side histograms for:

```text
geotagger_lookup_latency_microseconds
geotagger_audit_publish_latency_microseconds
geotagger_request_latency_microseconds
```

## MMDB updates

The updater defaults to:

```text
MAXMIND_EDITIONS=GeoLite2-City,GeoLite2-ASN
MMDB_DIR=/data
```

Cron schedule:

```text
17 3 * * *
```

`concurrencyPolicy: Forbid` prevents overlapping update Jobs. A complete City+ASN release is staged and verified first, then one atomic `current` symlink switch activates the new generation. The API periodically checks the files reached through `current` and hot-reloads changed readers.

Required runtime secret:

```text
MAXMIND_LICENSE_KEY
```

Legacy/custom single-database updater mode remains available through `MAXMIND_EDITION` and `MMDB_PATH`.

## Configuration

API variables:

| Variable | Default | Purpose |
|---|---:|---|
| `HTTP_ADDR` | `:8080` | API listener |
| `ADMIN_ADDR` | `:9090` | internal probes/metrics |
| `CITY_MMDB_PATH` | `/data/current/GeoLite2-City.mmdb` | active City database path |
| `ASN_MMDB_PATH` | `/data/current/GeoLite2-ASN.mmdb` | active ASN database path |
| `MMDB_RELOAD_INTERVAL` | `5m` | update detection interval |
| `API_KEYS` | required | `id:sha256hex` entries |
| `NATS_URL` | `nats://nats:4222` | audit transport |
| `AUDIT_IP_MODE` | `hmac` | `hmac`, `raw`, or `omit` |
| `AUDIT_HMAC_KEY` | required for `hmac` | HMAC key, at least 32 bytes |
| `AUDIT_TIMEOUT` | `50ms` | durable publish deadline |
| `ALLOW_PRIVATE_IPS` | `false` | permit private lookup targets |

Worker variables include `CLICKHOUSE_URL`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`, `BATCH_SIZE`, `FLUSH_INTERVAL`, and NATS durable/subject settings.

## Deployment

For a fresh K3s node:

1. Configure K3s secrets encryption before production secrets are applied.
2. Verify Metrics Server works.
3. Apply the `geotagger` namespace and a local, uncommitted production Secret.
4. Configure the Cloudflare Tunnel public hostname to the internal Service.
5. Deploy. The bootstrap Job downloads and atomically activates a complete City+ASN bundle before the API rollout.

```bash
kubectl apply -f deploy/k3s/namespace.yaml
kubectl apply -f /path/to/production-secrets.yaml
./scripts/deploy-k3s.sh
```

Verify:

```bash
kubectl -n geotagger get pods,hpa,networkpolicy
kubectl -n geotagger top pods
kubectl -n geotagger get pvc
readlink -f /var/lib/geotagger/mmdb/current
ls -lh /var/lib/geotagger/mmdb/current/GeoLite2-{City,ASN}.mmdb
```

End-to-end rich lookup:

```bash
curl -i \
  -H "Authorization: Bearer $API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' \
  https://geo.itsjosiahdavis.dev/v1/lookup
```

A deployment is not considered validated until the returned `X-Request-ID` is also found in ClickHouse.

## Load testing

Run k6 from a machine other than the GeoTagger VM for capacity measurements.

Country compatibility path:

```bash
RPS=500 DURATION=60s BASE_URL=https://geo.itsjosiahdavis.dev API_TOKEN="$API_TOKEN" \
  k6 run test/load/k6.js
```

Rich City+ASN path:

```bash
RPS=500 DURATION=60s BASE_URL=https://geo.itsjosiahdavis.dev API_TOKEN="$API_TOKEN" \
  k6 run test/load/k6-rich.js
```

Publish capacity numbers for the rich endpoint only after the external rich test has been run.

## CI/CD

Pull requests and feature branches:

- verify the Go module graph;
- run `go test -race ./...`;
- run `go vet ./...`;
- run reachable Go vulnerability checks;
- render K3s manifests;
- verify the pinned ClickHouse image starts;
- build the API, worker and updater images; and
- exercise the NATS-to-ClickHouse integration path.

Pushes to `main` publish application images to GHCR. Production deployments should use approved immutable image tags/digests rather than relying indefinitely on `latest`.
