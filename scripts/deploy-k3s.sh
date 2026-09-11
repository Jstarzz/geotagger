#!/usr/bin/env sh
set -eu

namespace=geotagger
secret=geotagger-secrets

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

need kubectl
need base64

kubectl apply -f deploy/k3s/namespace.yaml

if ! kubectl -n "$namespace" get secret "$secret" >/dev/null 2>&1; then
  echo "missing $secret; copy deploy/k3s/secrets.example.yaml, fill it, then apply it" >&2
  exit 1
fi

secret_value() {
  kubectl -n "$namespace" get secret "$secret" -o "jsonpath={.data.$1}" | base64 -d
}

for key in API_KEYS AUDIT_HMAC_KEY MAXMIND_LICENSE_KEY CLICKHOUSE_PASSWORD CLOUDFLARE_TUNNEL_TOKEN; do
  value="$(secret_value "$key")"
  if [ -z "$value" ]; then
    echo "secret $secret/$key is empty" >&2
    exit 1
  fi
  case "$value" in
    *REPLACE*)
      echo "secret $secret/$key still contains a placeholder" >&2
      exit 1
      ;;
  esac
done

hmac_key="$(secret_value AUDIT_HMAC_KEY)"
if [ "${#hmac_key}" -lt 32 ]; then
  echo "AUDIT_HMAC_KEY must be at least 32 bytes" >&2
  exit 1
fi

if ! kubectl top nodes >/dev/null 2>&1; then
  echo "warning: kubectl top nodes failed; verify K3s Metrics Server before relying on HPA" >&2
fi

# Render first so malformed Kustomize input fails before mutating workloads.
kubectl kustomize deploy/k3s >/dev/null

# Seed/update the node-local MMDB once before API replicas start.
kubectl -n "$namespace" delete job geotagger-mmdb-bootstrap --ignore-not-found --wait=true
kubectl apply -f deploy/k3s/mmdb-bootstrap.yaml
kubectl -n "$namespace" wait --for=condition=complete job/geotagger-mmdb-bootstrap --timeout=180s

kubectl apply -k deploy/k3s
kubectl -n "$namespace" rollout status statefulset/nats --timeout=180s
kubectl -n "$namespace" rollout status statefulset/clickhouse --timeout=180s
kubectl -n "$namespace" rollout status deployment/geotagger-api --timeout=180s
kubectl -n "$namespace" rollout status deployment/geotagger-audit-worker --timeout=180s
kubectl -n "$namespace" rollout status deployment/cloudflared --timeout=180s

printf '\nDeployment ready.\n'
kubectl -n "$namespace" get pods,hpa,networkpolicy
