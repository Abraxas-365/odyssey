#!/usr/bin/env bash
# Connect the local k3d cluster to the platform, as "Conectar clúster" in
# the console does: register it through the API (the demo user needs
# odyssey:clusters:manage) and store the one-time enrolment token in the
# agent's Secret. The agent (make agent / make agent-image) enrols with it.
#
#   KUBECONFIG=.dev-secrets/kubeconfig scripts/agent-enroll.sh [NAME]
#
# Re-running for a cluster that already exists issues it a new token (the
# agent enrols again; its previous certificate stops working).
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; source .env; set +a
name=${1:-local}
api="http://localhost:${ODYSSEY_PORT:-8095}/api/v1"
token=$(./scripts/token.sh)
auth=(-H "Authorization: Bearer $token")
json=(-H 'Content-Type: application/json')

existing=$(curl --fail-with-body -sS -G "$api/clusters" "${auth[@]}" -d limit=100 |
  jq -r --arg n "$name" '[.items[] | select(.name == $n and .status != "revoked")][0].id // empty')
if [[ -n "$existing" ]]; then
  enrollment=$(curl --fail-with-body -sS -X POST "$api/clusters/$existing/enrollment-token" "${auth[@]}")
  echo "cluster $name ($existing): new enrolment token"
else
  created=$(jq -n --arg n "$name" '{name:$n}' |
    curl --fail-with-body -sS -X POST "$api/clusters" "${auth[@]}" "${json[@]}" --data-binary @-)
  enrollment=$(jq -c .enrollment <<<"$created")
  echo "cluster $name ($(jq -r .cluster.id <<<"$created")) registered"
fi

kubectl apply -f agent/install/namespace.yaml >/dev/null
# the token goes through stdin (printf is a builtin): never in a process list
printf '%s' "$(jq -r .token <<<"$enrollment")" | kubectl -n odyssey-system create secret generic odyssey-agent \
  --from-file=token=/dev/stdin \
  --from-literal=gateway="$(jq -r .gateway <<<"$enrollment")" \
  --from-literal=ca-pin="$(jq -r .ca_pin <<<"$enrollment")" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
echo "token stored in odyssey-system/odyssey-agent (valid until $(jq -r .expires_at <<<"$enrollment")); start the agent: make agent"
