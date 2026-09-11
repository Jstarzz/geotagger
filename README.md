# geotagger

High-throughput, self-hosted IP-to-country API designed for a single on-prem K3s node with container autoscaling, Cloudflare Tunnel ingress, authenticated callers, and 30-day audit retention.

## Request path

```text
Azure caller
   │ HTTPS + Cloudflare edge controls
   ▼
Cloudflare Tunnel
   ▼
K3s Service
   ▼
Go API pods (HPA 1..8)
   │
   ├─ local memory-mapped GeoLite2 Country MMDB
   └─ durable JetStream audit publish
             ▼
            NATS
             ▼
      batched audit worker
             ▼
         ClickHouse
          TTL 30d
```

The lookup path never queries an external GeoIP API or database. MaxMind data is downloaded to the on-prem node and memory-mapped by each API process.

## API

`POST /v1/country`

```json
{"ip":"8.8.8.8"}
```

```json
{"country":"United States"}
```

Authentication uses a per-caller bearer token in the form `key-id.secret`. The server stores only the SHA-256 digest of each secret. Generate one with:

```bash
go run ./cmd/keygen azure-prod
```

Then give the printed `client token` to the caller and place only the printed `API_KEYS entry` in the server secret.

## Security model

- Cloudflare edge should restrict the public hostname to approved machine clients (mTLS/API Shield or equivalent policy).
- The Go service independently requires a per-service application token.
- The origin is reached through Cloudflare Tunnel; no inbound origin port needs to be exposed.
- API and worker containers run non-root with a read-only root filesystem and dropped capabilities.
- Request bodies are bounded; unknown JSON fields are rejected.
- Private/loopback/link-local lookup targets are rejected by default.
- Successful responses are returned only after JetStream acknowledges the audit event; ClickHouse is not on the request path.
- Raw target IPs are **not** stored by default. Audit records use HMAC-SHA256 so repeated IPs can be correlated without retaining the original IP. Set `AUDIT_IP_MODE=raw` only if the compliance requirement explicitly demands it.
- `/metrics`, `/healthz`, and `/readyz` listen on the internal admin port `9090`, separate from the public API port `8080`.

**HIPAA note:** software architecture alone does not make a deployment HIPAA compliant. If ePHI traverses Cloudflare or another vendor, use the appropriate service tier and execute the required BAA. Keep the organizational controls, risk analysis, access review, incident response, and retention procedures with the deployment documentation.

## Why Go

This service does almost no business logic: authenticate, parse `netip.Addr`, memory-map lookup, durable audit publish, encode a tiny response. Go keeps that path small and concurrency-friendly without a JS runtime or unnecessary framework.

## Autoscaling

`deploy/k3s/api.yaml` defines a Kubernetes HPA:

- min: 1 API pod
- max: 8 API pods
- CPU target: 65%
- aggressive scale-up
- 5-minute scale-down stabilization

This scales **containers on the one physical node**, not hardware. The ceiling is still the node's CPU/RAM. Benchmark before changing the limits; one Go process may already exceed the 10k RPS target.

## Audit retention

ClickHouse uses a `MergeTree` table partitioned by day with:

```sql
TTL timestamp + INTERVAL 30 DAY DELETE
```

NATS JetStream uses work-queue retention and a 7-day maximum age as a durable outage buffer; acknowledged rows are removed after the ClickHouse write succeeds. The audit worker batches writes to ClickHouse so the analytical store never sits directly in the lookup path.

## MMDB updates

The updater downloads `GeoLite2-Country`, extracts only the `.mmdb`, writes it to a temporary file, fsyncs, and atomically renames it into place. A K3s CronJob checks daily. API pods check the file periodically and safely reopen/verify a changed database.

Required secret: `MAXMIND_LICENSE_KEY`.

## Local configuration

API variables:

| Variable | Default | Purpose |
|---|---:|---|
| `HTTP_ADDR` | `:8080` | public API listener |
| `ADMIN_ADDR` | `:9090` | internal probes/metrics |
| `MMDB_PATH` | `/data/GeoLite2-Country.mmdb` | MMDB path |
| `MMDB_RELOAD_INTERVAL` | `5m` | reload check |
| `API_KEYS` | required | `id:sha256hex` entries |
| `NATS_URL` | `nats://nats:4222` | audit transport |
| `AUDIT_IP_MODE` | `hmac` | `hmac`, `raw`, or `omit` |
| `AUDIT_HMAC_KEY` | required for `hmac` | HMAC secret |
| `AUDIT_TIMEOUT` | `50ms` | durable audit ack deadline |
| `ALLOW_PRIVATE_IPS` | `false` | permit private targets |

Worker variables include `CLICKHOUSE_URL`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`, `BATCH_SIZE`, and `FLUSH_INTERVAL`.

## Deploy on the St. Kitts node

1. Install K3s and ensure the bundled metrics-server is healthy (`kubectl top nodes`).
2. Copy `deploy/k3s/secrets.example.yaml`, replace every placeholder, apply it, and do **not** commit it.
3. Create a remotely managed Cloudflare Tunnel and configure its public hostname (for example `api.example.com`) to the in-cluster service `http://geotagger.geotagger.svc.cluster.local:8080`.
4. Put the tunnel token in `CLOUDFLARE_TUNNEL_TOKEN`.
5. Deploy:

```bash
kubectl apply -f deploy/k3s/namespace.yaml
kubectl apply -f /path/to/your/secrets.yaml
./scripts/deploy-k3s.sh
```

6. Verify:

```bash
kubectl -n geotagger get pods,hpa
kubectl -n geotagger top pods
kubectl -n geotagger port-forward svc/geotagger 18080:8080 19090:9090
curl -fsS http://127.0.0.1:19090/readyz
curl -sS -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' http://127.0.0.1:18080/v1/country
```

## Load test

The repo contains a k6 constant-arrival-rate test. Start below the target and ramp deliberately:

```bash
RPS=1000 DURATION=60s BASE_URL=https://api.example.com API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
RPS=5000 DURATION=60s BASE_URL=https://api.example.com API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
RPS=10000 DURATION=60s BASE_URL=https://api.example.com API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
```

Watch HPA, CPU, error rate, request p95/p99, JetStream backlog, and ClickHouse insertion throughput during the run. Do not treat 10k RPS as proven until this passes on the actual node and network path.

## CI/CD

Pull requests run race-enabled Go tests, `go vet`, and build all three container targets. Pushes to `main` also publish:

- `ghcr.io/jstarzz/geotagger-api:latest`
- `ghcr.io/jstarzz/geotagger-worker:latest`
- `ghcr.io/jstarzz/geotagger-updater:latest`

Production deployments should eventually pin immutable SHA tags instead of `latest` once the first node is commissioned.
