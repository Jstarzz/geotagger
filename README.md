# GeoTagger

GeoTagger is a self-hosted, authenticated IP-to-country API for an on-premises K3s deployment. It uses a local MaxMind GeoLite2 Country database, requires durable audit acceptance through NATS JetStream before normal success, and persists audit events asynchronously to ClickHouse.

Production endpoint:

```text
https://geo.itsjosiahdavis.dev
```

## Documentation

Start with the documentation index:

- [`docs/README.md`](docs/README.md) — complete technical/operational handoff
- [`docs/PROJECT_OVERVIEW.md`](docs/PROJECT_OVERVIEW.md) — service purpose, components and request lifecycle
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — production architecture and diagrams
- [`docs/KUBERNETES.md`](docs/KUBERNETES.md) — K3s/Kubernetes resource model and operations
- [`docs/API.md`](docs/API.md) — API integration contract
- [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md) — measured load-test results
- [`docs/PERFORMANCE_TUNING.md`](docs/PERFORMANCE_TUNING.md) — applied tuning, Redis/cache decision and scaling roadmap
- [`docs/SECURITY_AUDIT.md`](docs/SECURITY_AUDIT.md) — security review and remediation priorities
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
    |                \
    |                 \ durable publish + ACK
    v                  v
GeoLite2 MMDB      NATS JetStream
                       |
                       v
                  audit worker
                       |
                       v
                   ClickHouse
```

The lookup itself is local; normal requests do not call MaxMind or another GeoIP provider over the network.

## API

```http
POST /v1/country
Authorization: Bearer <key-id.secret>
Content-Type: application/json
```

```json
{"ip":"8.8.8.8"}
```

Successful lookup:

```json
{"country":"United States"}
```

Every response includes `X-Request-ID`. The request ID is also stored in the audit event so a response can be correlated with its persisted audit record.

Generate a caller credential with:

```bash
go run ./cmd/keygen azure-prod
```

The client receives the generated token. The server stores only the corresponding SHA-256 digest entry.

Private, loopback and link-local target addresses are rejected by default.

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

The CPU request was raised from `250m` because HPA utilization is calculated against the request. At `250m`, a 65% target represented only `162.5m` per Pod and caused overly aggressive scale-out on the 9-vCPU single-node deployment.

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

First public-path load test on the 9-vCPU VM:

| Target | Achieved | p95 | p99 | HDD utilization |
|---:|---:|---:|---:|---:|
| 100 RPS | ~100 req/s | 190 ms | 203 ms | <1% |
| 1,000 RPS | ~668 req/s | 2.08 s | 3.81 s | <1% |
| 5,000 RPS | ~1,373 req/s | 14.3 s | 20.3 s | <3% |
| 10,000 RPS | ~322-643 req/s | 6.2-8.2 s | much higher | <1% |

The benchmark ran k6 on the same VM as the application and routed traffic through the public Cloudflare path. CPU contention appeared before disk saturation. A verified MMDB lookup took 56 microseconds.

For the tested topology, roughly 500-1,000 RPS is the current practical operating range before tail latency degrades sharply. That is not a clean server-side ceiling; the next benchmark should use an external generator.

Redis is not used or recommended for the current lookup path. The local memory-mapped MMDB lookup is already faster and simpler than an additional network cache. See [`docs/PERFORMANCE_TUNING.md`](docs/PERFORMANCE_TUNING.md).

The API now exports separate origin-side histograms for:

```text
geotagger_lookup_latency_microseconds
geotagger_audit_publish_latency_microseconds
geotagger_request_latency_microseconds
```

## MMDB updates

The updater downloads GeoLite2 Country, validates the replacement, fsyncs it and atomically renames it into the active path.

Cron schedule:

```text
17 3 * * *
```

`concurrencyPolicy: Forbid` prevents overlapping update Jobs. API Pods periodically reopen a changed database safely.

Required runtime secret:

```text
MAXMIND_LICENSE_KEY
```

## Configuration

API variables:

| Variable | Default | Purpose |
|---|---:|---|
| `HTTP_ADDR` | `:8080` | API listener |
| `ADMIN_ADDR` | `:9090` | internal probes/metrics |
| `MMDB_PATH` | `/data/GeoLite2-Country.mmdb` | MMDB path |
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
5. Deploy the K3s manifests.

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
```

Internal API port-forward:

```bash
kubectl -n geotagger port-forward svc/geotagger 18080:8080
```

Internal admin port-forward:

```bash
kubectl -n geotagger port-forward deployment/geotagger-api 19090:9090
curl -fsS http://127.0.0.1:19090/readyz
```

End-to-end lookup:

```bash
curl -i \
  -H "Authorization: Bearer $API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' \
  https://geo.itsjosiahdavis.dev/v1/country
```

A deployment is not considered validated until the returned `X-Request-ID` is also found in ClickHouse.

## Load testing

Run k6 from a machine other than the GeoTagger VM for capacity measurements.

```bash
RPS=100 DURATION=60s BASE_URL=https://geo.itsjosiahdavis.dev API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
RPS=500 DURATION=60s BASE_URL=https://geo.itsjosiahdavis.dev API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
RPS=1000 DURATION=60s BASE_URL=https://geo.itsjosiahdavis.dev API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
```

The load script treats both HTTP 200 and the legitimate `404 country not found` application result as expected responses for valid public fixture IPs. Unexpected 4xx/5xx responses still fail the test.

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
