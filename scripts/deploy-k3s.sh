#!/usr/bin/env sh
set -eu
kubectl apply -f deploy/k3s/namespace.yaml
if ! kubectl -n geotagger get secret geotagger-secrets >/dev/null 2>&1; then
  echo "missing geotagger-secrets; copy deploy/k3s/secrets.example.yaml, fill it, then apply it" >&2
  exit 1
fi
kubectl apply -k deploy/k3s
kubectl -n geotagger rollout status statefulset/nats --timeout=180s
kubectl -n geotagger rollout status statefulset/clickhouse --timeout=180s
kubectl -n geotagger rollout status deployment/geotagger-api --timeout=180s
kubectl -n geotagger rollout status deployment/geotagger-audit-worker --timeout=180s
