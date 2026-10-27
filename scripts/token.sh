#!/usr/bin/env bash
# Log the demo user in through IAMKit and print an access token for the API.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; source .env; set +a
: "${IAMKIT_ENVIRONMENT_ID:?run make seed first}"
jq -n --arg e "$IAMKIT_ENVIRONMENT_ID" --arg o "$IAMKIT_ORGANIZATION_ID" \
      --arg a "$IAMKIT_APPLICATION_ID" --arg r "$IAMKIT_RESOURCE_ID" \
      --arg email "$DEMO_EMAIL" --arg pw "$DEMO_PASSWORD" \
  '{environment_id:$e,organization_id:$o,application_id:$a,resource_id:$r,email:$email,password:$pw}' |
  curl --fail-with-body --silent --show-error "http://localhost:${IAMKIT_PORT:-8085}/identity/v1/login" \
    -H 'Content-Type: application/json' --data-binary @- | jq -er .access_token
