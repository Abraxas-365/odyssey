#!/usr/bin/env bash
# End-to-end check against the running stack with a real IAMKit token:
# project create -> find -> list -> patch -> archive, plus auth and
# validation denials. Exits non-zero on any mismatch.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; source .env; set +a
base="http://localhost:${ODYSSEY_PORT:-8095}"
api="$base/api/v1"
token=$(./scripts/token.sh)
auth=(-H "Authorization: Bearer $token")
json=(-H 'Content-Type: application/json')
fail=0

expect() { # expect NAME WANT GOT
  if [[ "$2" == "$3" ]]; then echo "ok    $1 ($3)"; else echo "FAIL  $1: want $2, got $3"; fail=1; fi
}
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

expect health 200 "$(code "$base/health")"
expect "public config" 200 "$(code "$base/api/config")"
expect "no token" 401 "$(code "$api/projects")"
expect "bad token" 401 "$(code "$api/projects" -H 'Authorization: Bearer nope')"

slug="smoke-$(date +%s)"
created=$(curl -s -w '\n%{http_code}' -X POST "$api/projects" "${auth[@]}" "${json[@]}" \
  -d "$(jq -n --arg s "$slug" '{name:"Smoke test",slug:$s}')")
expect create 201 "$(tail -1 <<<"$created")"
id=$(head -1 <<<"$created" | jq -r .id)
expect "find tenant" "$IAMKIT_ORGANIZATION_ID" "$(curl -s "$api/projects/$id" "${auth[@]}" | jq -r .organization_id)"
expect "duplicate slug" 409 "$(code -X POST "$api/projects" "${auth[@]}" "${json[@]}" -d "$(jq -n --arg s "$slug" '{name:"x",slug:$s}')")"
expect "bad slug" 400 "$(code -X POST "$api/projects" "${auth[@]}" "${json[@]}" -d '{"name":"x","slug":"Bad Slug"}')"
expect "listed" true "$(curl -s -G "$api/projects" -d search="$slug" "${auth[@]}" | jq --arg id "$id" 'any(.items[]; .id == $id)')"
expect patch "Smoke renamed" "$(curl -s -X PATCH "$api/projects/$id" "${auth[@]}" "${json[@]}" -d '{"name":"Smoke renamed"}' | jq -r .name)"
expect archive archived "$(curl -s -X POST "$api/projects/$id/archive" "${auth[@]}" | jq -r .status)"
expect "archived refuses changes" 422 "$(code -X PATCH "$api/projects/$id" "${auth[@]}" "${json[@]}" -d '{"name":"x"}')"

exit $fail
