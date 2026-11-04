#!/usr/bin/env bash
# Provision (or re-sync) the LOCAL IAMKit for development: scripts/provision-iam.sh
# with the local stack's defaults, then the IAMKIT_* settings go into .env
# (the block marked "written by scripts/seed-iam.sh", rewritten each run).
#
# Idempotent: run it again after changing iam/permissions.json (make iam-sync).
# Arguments go to provision-iam.sh: --dry-run, --prune.
#
# The IAMKIT_INTROSPECTION_CLIENT_* the API needs for /mcp land in .env with
# the other IAMKIT_* values (the secret is created once and kept in
# .dev-secrets/iamkit.env, so re-runs keep it).
#
# The demo user (DEMO_EMAIL / DEMO_PASSWORD) joins the demo organization with
# the manifest's first role (TI).
#
# Requires: docker compose stack running (make up), curl, jq.
# Never enable shell tracing: the management key and password are secrets.
set -euo pipefail
cd "$(dirname "$0")/.."

[[ -f .env ]] || { echo ".env missing: run make setup" >&2; exit 1; }
set -a; source .env; set +a
: "${DEMO_EMAIL:?Set DEMO_EMAIL in .env}"
: "${DEMO_PASSWORD:?Set DEMO_PASSWORD in .env}"

umask 077
mkdir -p .dev-secrets
if [[ ! -f .dev-secrets/owner.json ]]; then
  docker compose exec -T iamkit sh -c 'rm -f /tmp/owner.json && iamkit bootstrap --email "$0" --workspace "Odyssey" --output /tmp/owner.json >/dev/null && cat /tmp/owner.json && rm -f /tmp/owner.json' \
    "${IAMKIT_OPERATOR_EMAIL:-admin@odyssey.local}" > .dev-secrets/owner.json
fi

dry_run=0
for arg in "$@"; do [[ "$arg" != --dry-run ]] || dry_run=1; done

# The resource audience is the MCP endpoint's URL (RFC 8707: the audience of an
# MCP token is the server it is for) and is immutable in IAMKit. It is NOT read
# from IAMKIT_AUDIENCE: that is what an earlier seed wrote into .env, so it
# could never reveal a resource created with another audience.
# MCP_PRESETS names the assistants admitted to it (empty = no MCP resource).
IAMKIT_URL="http://localhost:${IAMKIT_PORT:-8085}" \
IAMKIT_KEY_FILE=.dev-secrets/owner.json \
SPEC=iam/permissions.json \
APP_URL="${APP_URL:-http://localhost:5178}" \
AUDIENCE="${ODYSSEY_MCP_URL:-http://localhost:${ODYSSEY_PORT:-8095}/mcp}" \
ACCESS_API_URL="${ACCESS_API_URL:-}" \
MCP_PRESETS="${MCP_PRESETS-claude_code,codex,vscode,antigravity}" \
PROJECT_NAME="Odyssey" ENVIRONMENT_NAME=Development ORG_NAME="${ORG_NAME:-Demo inEdge}" APP_NAME="Odyssey Console" \
OWNER_EMAIL="$DEMO_EMAIL" OWNER_PASSWORD="$DEMO_PASSWORD" OWNER_NAME="Demo TI" \
OUT=.dev-secrets/iamkit.env \
  scripts/provision-iam.sh "$@"
(( dry_run )) && exit 0

marker='# --- written by scripts/seed-iam.sh ---'
before=$(grep -c '^IAMKIT_ENVIRONMENT_ID=.' .env || true)
sed -i.bak "/^$(sed 's/[\/.]/\\&/g' <<<"$marker")\$/,\$d" .env && rm -f .env.bak
{
  echo "$marker"
  grep -v -e '^#' -e '^IAMKIT_URL=' .dev-secrets/iamkit.env
} >> .env

echo
if [[ "$before" == 0 ]]; then
  echo "IAMKit seeded. Start the API with the ids: make up"
else
  echo ".env updated. Restart the API if an IAMKIT_* value changed (make up); get a fresh token for new permissions (make token)."
fi
