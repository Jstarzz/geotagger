#!/usr/bin/env sh
set -eu

kubectl apply -f deploy/k3s/namespace.yaml

if ! kubectl -n geotagger get secret geotagger-secrets >/dev/null 2>&1; then
  echo "missing geotagger-secrets; copy deploy/k3s/secrets.example.yaml, fill it, then apply it" >&2
  exit 1
fi

# Seed/update the node-local MMDB once before API replicas start.
kubectl -n geotagger delete job geotagger-mmdb-bootstrap --ignore-not-found --wait=true
kubectl apply -f deploy/k3s/mmdb-bootstrap.yaml
kubectl -n geotagger wait --for=condition=complete job/geotagger-mmdb-bootstrap --timeout=180s

kubectl apply -k deploy/k3s
kubectl -n geotagger rollout status statefulset/nats --timeout=180s
kubectl -n geotagger rollout status statefulset/clickhouse --timeout=180s
kubectl -n geotagger rollout status deployment/geotagger-api --timeout=180s
kubectl -n geotagger rollout status deployment/geotagger-audit-worker --timeout=180s
