# City + ASN Production Upgrade Runbook

## Purpose

This runbook upgrades an existing country-only GeoTagger K3s deployment to the City + ASN release without changing caller credentials, the public hostname, NATS durability settings, or the ClickHouse audit schema.

The upgrade adds:

```text
POST /v1/lookup
GET  /v1/lookup?ip=...
GET  /v1/me
```

and keeps:

```text
POST /v1/country
```

backward compatible.

## Preconditions

Before changing the VM:

```text
[ ] main branch CI is green
[ ] GHCR images for the target main commit are published
[ ] existing /v1/country request works
[ ] current API token is available to the operator/client test machine
[ ] MAXMIND_LICENSE_KEY remains present in geotagger-secrets
[ ] NATS and ClickHouse are healthy
[ ] current ClickHouse audit row can be queried by request_id
[ ] enough free space exists under /var/lib/geotagger/mmdb for several City+ASN generations
```

Do not delete the existing `GeoLite2-Country.mmdb` during the first upgrade. Keeping it through the rollback window makes reverting to an older country-only deployment simpler.

## Change summary

Old lookup data:

```text
/var/lib/geotagger/mmdb/GeoLite2-Country.mmdb
```

New lookup data:

```text
/var/lib/geotagger/mmdb/
├── current -> releases/release-.../
└── releases/
    └── release-.../
        ├── GeoLite2-City.mmdb
        └── GeoLite2-ASN.mmdb
```

The updater stages and verifies both databases before atomically changing the `current` symlink to the new complete generation.

## Upgrade

From the GeoTagger repository on the VM:

```bash
git fetch origin
git checkout main
git pull --ff-only origin main
```

Record the deployed commit:

```bash
git rev-parse HEAD
```

Render the manifests before mutation:

```bash
kubectl kustomize deploy/k3s >/dev/null
```

Check the current cluster:

```bash
kubectl -n geotagger get pods,hpa,pvc,cronjob
kubectl -n geotagger get endpoints geotagger
kubectl -n geotagger top pods || true
```

Run the repository deployment script:

```bash
./scripts/deploy-k3s.sh
```

The script runs the MMDB bootstrap Job first. The API rollout does not proceed until that Job completes successfully.

## Verify the active MMDB generation

On the node/VM:

```bash
ls -lah /var/lib/geotagger/mmdb
readlink -f /var/lib/geotagger/mmdb/current
ls -lh /var/lib/geotagger/mmdb/current/
```

Expected active files:

```text
GeoLite2-City.mmdb
GeoLite2-ASN.mmdb
```

Inspect bootstrap/update logs if needed:

```bash
kubectl -n geotagger get jobs --sort-by=.metadata.creationTimestamp
kubectl -n geotagger logs job/geotagger-mmdb-bootstrap
```

## Verify Kubernetes

```bash
kubectl -n geotagger get pods -o wide
kubectl -n geotagger get deployment geotagger-api
kubectl -n geotagger get hpa geotagger-api
kubectl -n geotagger get endpoints geotagger
```

Expected API baseline:

```text
ready replicas: at least 2
HPA minimum:    2
HPA maximum:    8
```

Check internal readiness:

```bash
kubectl -n geotagger port-forward deployment/geotagger-api 19090:9090
```

In another shell:

```bash
curl -i http://127.0.0.1:19090/readyz
```

Expected: HTTP 204.

## Verify backward compatibility

```bash
curl -i \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' \
  https://geo.itsjosiahdavis.dev/v1/country
```

Expected: HTTP 200 and a country response when the installed City database contains the record.

Record `X-Request-ID`.

## Verify the rich endpoint

```bash
curl -i \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ip":"8.8.8.8"}' \
  https://geo.itsjosiahdavis.dev/v1/lookup
```

Verify the response contains the expected structural fields:

```text
ip
country
classification
source.city_database
source.asn_database
lookup_latency_us
request_id
```

City/region/ASN fields may vary by address and database snapshot.

## Verify `/v1/me`

From an external client through the Cloudflare hostname:

```bash
curl -i \
  -H "Authorization: Bearer $GEOTAGGER_TOKEN" \
  https://geo.itsjosiahdavis.dev/v1/me
```

Confirm the response resolves the externally observed client IP rather than a Kubernetes/VM private address.

Do not use a direct untrusted origin path to validate `/v1/me`; the endpoint is designed around the Cloudflare Tunnel trust boundary.

## Verify audit persistence

For at least one successful new rich lookup, take the returned `X-Request-ID` and query ClickHouse for the matching row.

The row should still contain the minimized audit contract:

```text
request_id
caller_id
HMAC-derived ip_value when hmac mode is active
country_code / country
outcome
status_code
lookup_latency_us
MMDB version metadata
```

The audit schema intentionally does not persist the full city/coordinates/ASN response.

## Verify scheduled updates

```bash
kubectl -n geotagger get cronjob geotagger-mmdb-update
```

Expected schedule:

```text
17 3 * * *
```

Expected concurrency policy:

```text
Forbid
```

Optional manual test:

```bash
kubectl -n geotagger create job \
  --from=cronjob/geotagger-mmdb-update \
  geotagger-mmdb-manual-$(date +%s)
```

After it completes, verify `current` resolves to a complete release directory containing both MMDB files.

## Rollback

Rollback only if the new release cannot meet the service's functional/security requirements and the failure cannot be corrected safely in place.

First capture evidence:

```bash
kubectl -n geotagger get pods -o wide
kubectl -n geotagger logs deployment/geotagger-api --tail=300
kubectl -n geotagger get jobs --sort-by=.metadata.creationTimestamp
readlink -f /var/lib/geotagger/mmdb/current || true
```

Then check out the previously deployed known-good commit and redeploy using its documented procedure.

If rolling back to the old country-only code, verify that the legacy file still exists before starting old API Pods:

```text
/var/lib/geotagger/mmdb/GeoLite2-Country.mmdb
```

Do not destroy NATS or ClickHouse persistent volumes during rollback. The City+ASN feature does not require a destructive audit-schema migration.

## Post-upgrade benchmark

The historical capacity numbers are country-only measurements. Do not relabel them as rich-endpoint results.

Run the dedicated rich test from a machine outside the GeoTagger VM:

```bash
RPS=100 \
DURATION=60s \
BASE_URL=https://geo.itsjosiahdavis.dev \
API_TOKEN="$GEOTAGGER_TOKEN" \
k6 run test/load/k6-rich.js
```

Increase load progressively while collecting API Pod CPU/RAM, HPA replica count, origin request latency, MMDB lookup latency, JetStream publish latency, queue backlog and ClickHouse health.
