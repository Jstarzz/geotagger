# geotagger

High-throughput, self-hosted IP-to-country API designed for a single on-prem K3s node with API pod autoscaling, Cloudflare Tunnel ingress, authenticated callers, privacy-preserving durable audit logging, and 30-day ClickHouse audit retention.

## Documentation

For the complete technical handoff, start with:

- [`docs/PROJECT_OVERVIEW.md`](docs/PROJECT_OVERVIEW.md) — detailed project purpose, goals, components, trust boundaries, data flow, durability model, scaling, failure modes, and operational definition of success.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — detailed Mermaid diagrams covering physical deployment, Cloudflare ingress, Kubernetes topology, request/audit sequences, storage, networking, autoscaling, and recovery behavior.
- [`docs/KUBERNETES.md`](docs/KUBERNETES.md) — GeoTagger-specific explanation of Pods vs containers, K3s/containerd, Deployments, StatefulSets, Services, HPA, PVCs, probes, NetworkPolicy, CronJobs, Secrets, and useful `kubectl` operations.
- [`docs/API.md`](docs/API.md) — API integration guide.
- [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md) — measured load-test results and capacity interpretation.
- [`docs/SECURITY_AUDIT.md`](docs/SECURITY_AUDIT.md) — internal engineering security audit and hardening roadmap.

## Request path

```text
Authorized caller
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

Every response includes a server-generated `X-Request-ID`. The same ID is persisted in the audit record so application failures and audit rows can be correlated without putting the queried IP in ordinary access logs.

Authentication uses a per-caller bearer token in the form `key-id.secret`. The server stores only the SHA-256 digest of each secret. Generate one with:

```bash
go run ./cmd/keygen azure-prod
```

Give the printed `client token` to the caller and place only the printed `API_KEYS entry` in the server secret.

## Security model

- Cloudflare edge should restrict the public hostname to approved machine clients (mTLS/API Shield or equivalent policy).
- The Go service independently requires a per-service application token.
- Authentication failures are audited without parsing or retaining the requested target IP.
- The origin is reached through Cloudflare Tunnel; no inbound origin port needs to be exposed.
- Kubernetes `NetworkPolicy` defaults namespace ingress to deny and permits only Cloudflared→API, API/worker→NATS, and worker→ClickHouse paths.
- The API Kubernetes Service exposes only port `8080`; `/metrics`, `/healthz`, and `/readyz` remain pod-internal on `9090` for probes/administration.
- API and worker containers run non-root with read-only root filesystems and dropped capabilities.
- Request bodies are bounded; unknown JSON fields are rejected.
- Private/loopback/link-local lookup targets are rejected by default.
- Successful responses are returned only after JetStream acknowledges the audit event; ClickHouse is not on the request path.
- Raw target IPs are **not** stored by default. Audit records use HMAC-SHA256 so repeated IPs can be correlated without retaining the original IP. `AUDIT_HMAC_KEY` must be at least 32 bytes. Set `AUDIT_IP_MODE=raw` only if the compliance requirement explicitly demands it.
- ClickHouse is cluster-internal and uses the dedicated `geotagger_ingest` identity; the network policy allows its HTTP port only from the audit worker.

**HIPAA note:** software architecture alone does not make a deployment HIPAA compliant. If ePHI traverses Cloudflare or another vendor, use the appropriate service tier and execute the required BAA. Keep organizational controls, risk analysis, access review, incident response, backup/restore testing, and retention/destruction procedures with the deployment documentation.

## Why Go

This service does almost no business logic: authenticate, parse `netip.Addr`, memory-map lookup, durable audit publish, encode a tiny response. Go keeps that path small and concurrency-friendly without a JS runtime or unnecessary framework.

## Autoscaling

`deploy/k3s/api.yaml` defines a Kubernetes HPA:

- min: 1 API pod
- max: 8 API pods
- CPU target: 65%
- aggressive scale-up
- 5-minute scale-down stabilization

This scales **API Pods on the one physical K3s node**, not hardware. The ceiling is still the VM/node CPU and RAM. Once the VM is saturated, creating more Pods cannot manufacture additional compute.

K3s includes Metrics Server by default. Verify it before depending on HPA:

```bash
kubectl top nodes
kubectl -n geotagger top pods
```

## Audit retention and durability

ClickHouse uses a `MergeTree` table partitioned by day with:

```sql
TTL timestamp + INTERVAL 30 DAY DELETE
```

NATS JetStream is a durable transport/buffer for audit events that have not yet been successfully persisted to ClickHouse. The production stream uses:

```text
Retention: WorkQueue
MaxAge:    0
MaxBytes:  8 GiB
Discard:   DiscardNew
```

`MaxAge=0` is deliberate: required unpersisted audit events do not expire merely because an outage lasts a long time. If JetStream reaches the configured 8-GiB capacity, `DiscardNew` rejects new publishes rather than deleting older queued work. Because the API waits for a durable publish acknowledgement, this causes the lookup path to fail closed instead of silently returning successful unaudited responses.

The audit worker batches writes to ClickHouse and acknowledges consumed JetStream messages only after persistence succeeds. The pipeline is at-least-once, so a worker crash after a ClickHouse insert but before the NATS ACK can create a duplicate audit row; use `request_id` when deduplication matters.

The included ClickHouse PVC requests `50Gi`; that is only an initial deployment size, **not** a 10k-RPS-sustained capacity claim. Size disk from measured average RPS, compressed bytes/event, and the 30-day retention target after the first load test.

### ClickHouse CPU compatibility

The on-prem host uses an older pre-AVX2 Xeon generation. ClickHouse 26.6+ changed the default amd64 build to x86-64-v3/AVX2, so this deployment pins `clickhouse/clickhouse-server:26.3.33.24-alpine`. The 26.3 LTS branch remains on the x86-64-v2/SSE4.2 baseline and is compatible with the node while still receiving LTS patch releases.

Do not casually bump ClickHouse to 26.6+ on this hardware. Re-check the upstream CPU baseline first.

## MMDB updates

The updater downloads `GeoLite2-Country`, extracts only the `.mmdb`, writes it to a temporary file, fsyncs, and atomically renames it into place. A K3s CronJob checks daily. API pods check the file periodically and safely reopen/verify a changed database. HPA-created API pods reuse the already-seeded node-local MMDB; they do not each download a new copy.

Required secret: `MAXMIND_LICENSE_KEY`.

## Configuration

API variables:

| Variable | Default | Purpose |
|---|---:|---|
| `HTTP_ADDR` | `:8080` | API listener |
| `ADMIN_ADDR` | `:9090` | pod-internal probes/metrics |
| `MMDB_PATH` | `/data/GeoLite2-Country.mmdb` | MMDB path |
| `MMDB_RELOAD_INTERVAL` | `5m` | reload check |
| `API_KEYS` | required | `id:sha256hex` entries |
| `NATS_URL` | `nats://nats:4222` | audit transport |
| `AUDIT_IP_MODE` | `hmac` | `hmac`, `raw`, or `omit` |
| `AUDIT_HMAC_KEY` | required for `hmac` | >=32-byte HMAC secret |
| `AUDIT_TIMEOUT` | `50ms` | durable audit publish deadline |
| `ALLOW_PRIVATE_IPS` | `false` | permit private targets |

Worker variables include `CLICKHOUSE_URL`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`, `BATCH_SIZE`, and `FLUSH_INTERVAL`. The deployment sets `CLICKHOUSE_USER=geotagger_ingest`.

Generate a strong audit HMAC key and database password, for example:

```bash
openssl rand -hex 32
openssl rand -base64 36
```

## Deploy on the St. Kitts node

1. Install K3s. For a fresh node, merge `deploy/k3s/k3s-config.example.yaml` into `/etc/rancher/k3s/config.yaml` before first use so Kubernetes Secrets are encrypted at rest. If K3s is already running, follow the K3s secrets-encryption rotation procedure instead of blindly overwriting the server config.
2. Ensure Metrics Server works (`kubectl top nodes`) and do not disable K3s network-policy enforcement.
3. Copy `deploy/k3s/secrets.example.yaml`, replace every placeholder, apply it, and do **not** commit it.
4. Create a remotely managed Cloudflare Tunnel and configure its public hostname (for example `api.example.com`) to `http://geotagger.geotagger.svc.cluster.local:8080`.
5. Put the tunnel token in `CLOUDFLARE_TUNNEL_TOKEN`.
6. Ensure the node can pull the GHCR application images after they are published from `main`.
7. Deploy:

```bash
kubectl apply -f deploy/k3s/namespace.yaml
kubectl apply -f /path/to/your/secrets.yaml
./scripts/deploy-k3s.sh
```

8. Verify:

```bash
kubectl -n geotagger get pods,hpa,networkpolicy
kubectl -n geotagger top pods

# Public API service
kubectl -n geotagger port-forward svc/geotagger 18080:8080

# Admin listener is intentionally not exposed by a Service
kubectl -n geotagger port-forward deployment/geotagger-api 19090:9090

curl -fsS http://127.0.0.1:19090/readyz
curl -i -sS -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' http://127.0.0.1:18080/v1/country
```

The API response should contain `X-Request-ID`; use it to find the corresponding ClickHouse audit row during validation.

## Load test

The repo contains a k6 constant-arrival-rate test. Start below the target and ramp deliberately:

```bash
RPS=1000 DURATION=60s BASE_URL=https://api.example.com API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
RPS=5000 DURATION=60s BASE_URL=https://api.example.com API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
RPS=10000 DURATION=60s BASE_URL=https://api.example.com API_TOKEN="$API_TOKEN" k6 run test/load/k6.js
```

Watch HPA, CPU, memory, error rate, request p95/p99, JetStream backlog, disk I/O, ClickHouse insertion throughput, and storage growth. Do not treat 10k RPS as proven until this passes on the actual node and Azure→Cloudflare→St. Kitts path.

## CI/CD

Pull requests and feature branches:

- verify the Go module graph is tidy
- run `go test -race ./...`
- run `go vet ./...`
- run `govulncheck` against reachable Go code
- render the K3s Kustomize tree
- verify the pinned ClickHouse image exists and starts
- build all three application container targets

Pushes to `main` also publish:

- `ghcr.io/jstarzz/geotagger-api:latest`
- `ghcr.io/jstarzz/geotagger-worker:latest`
- `ghcr.io/jstarzz/geotagger-updater:latest`

GitHub Actions are pinned by commit SHA. Production application deployments should move from `latest` to immutable SHA image tags after the first node is commissioned.
