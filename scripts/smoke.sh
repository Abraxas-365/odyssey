#!/usr/bin/env bash
# End-to-end check against the running stack (API + worker) with a real
# IAMKit token: project create -> find -> list -> patch; publish a version,
# a failing one (auto-restore), "Volver a esta"; audit; archive; auth and
# validation denials. Exits non-zero on any mismatch.
#
# SMOKE_CLUSTER=kubernetes runs the releases on the local k3d cluster (make
# cluster, make agent, make worker CLUSTER=kubernetes): real images, and the
# published URL must answer.
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
# --- Releases. The in-memory cluster runs any digest and fails the health
# check of a repository containing "unhealthy"; on Kubernetes the versions
# are whoami (differing by WHOAMI_NAME) and the broken one listens on a port
# other than the declared one. ---
env_id=$(curl -s "$api/projects/$id/environments" "${auth[@]}" | jq -r '.items[] | select(.kind=="production") | .id')
expect "production environment" production "$(curl -s "$api/projects/$id/environments/$env_id" "${auth[@]}" | jq -r .slug)"
deps="$api/projects/$id/environments/$env_id/deployments"
digest() { printf 'sha256:%s' "$(printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1)"; }
whoami=docker.io/traefik/whoami@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab
kube=$([[ "${SMOKE_CLUSTER:-fake}" == kubernetes ]] && echo 1 || true)
version() { # version NAME -> deploy body
  if [[ -n "$kube" ]]; then
    local port=8080; [[ "$1" == bad ]] && port=9090
    jq -n --arg i "$whoami" --arg n "$1" --argjson p "$port" '{image:$i,port:$p,health_path:"/health",env:{GREETING:"hola",WHOAMI_PORT_NUMBER:"8080",WHOAMI_NAME:$n}}'
  else
    local repo=ghcr.io/inedge/smoke; [[ "$1" == bad ]] && repo=ghcr.io/inedge/smoke-unhealthy
    jq -n --arg i "$repo@$(digest "$1")" '{image:$i,port:8080,health_path:"/healthz",env:{GREETING:"hola"}}'
  fi
}
deploy() { # deploy VERSION -> deployment id
  curl -s -X POST "$deps" "${auth[@]}" "${json[@]}" -d "$(version "$1")" | jq -r .id
}
serving() { # serving [WANT] -> the WHOAMI_NAME the published URL answers with (Kubernetes only)
  local u hostport port n
  u=$(curl -s "$api/projects/$id/environments/$env_id/current" "${auth[@]}" | jq -r .url)
  hostport=${u#http://}; port=${hostport##*:}
  for _ in $(seq 1 15); do
    n=$(curl -s --max-time 3 --resolve "${hostport%%:*}:$port:127.0.0.1" "$u" | sed -n 's/^Name: //p' | tr -d '\r')
    [[ -n "$n" && "$n" == "${1:-$n}" ]] && { echo "$n"; return; }
    sleep 1
  done
  echo "nothing at $u (${n:-no answer})"
}
wait_done() { # wait_done DEPLOYMENT -> final status
  local st
  for _ in $(seq 1 240); do
    st=$(curl -s "$deps/$1" "${auth[@]}" | jq -r .status)
    case "$st" in ready|failed|cancelled) echo "$st"; return;; esac
    sleep 1
  done
  echo "timeout($st)"
}
expect "tag refused" 400 "$(code -X POST "$deps" "${auth[@]}" "${json[@]}" -d '{"image":"ghcr.io/inedge/app:latest","port":8080}')"
expect "deploy accepted" 202 "$(code -X POST "$deps" "${auth[@]}" "${json[@]}" -d "$(version v0)")"
v1=$(deploy v1)
expect "v0 replaced by v1" cancelled "$(curl -s -G "$deps" -d status=cancelled "${auth[@]}" | jq -r '.items[0].status')"
expect "v1 live" ready "$(wait_done "$v1")"
expect "current is v1" "$v1" "$(curl -s "$api/projects/$id/environments/$env_id/current" "${auth[@]}" | jq -r .deployment.id)"
[[ -z "$kube" ]] || expect "v1 answers at its URL" v1 "$(serving v1)"
bad=$(deploy bad)
expect "unhealthy fails" failed "$(wait_done "$bad")"
expect "failure code" HEALTH_CHECK_FAILED "$(curl -s "$deps/$bad" "${auth[@]}" | jq -r .error.code)"
restore=$(curl -s -G "$deps" -d status=pending -d status=applying -d status=verifying -d status=ready "${auth[@]}" | jq -r --arg b "$bad" '[.items[] | select(.rollback_of==$b)][0].id')
expect "auto-restore" ready "$(wait_done "$restore")"
expect "still serving v1" "$(curl -s "$deps/$v1" "${auth[@]}" | jq -r .release_id)" "$(curl -s "$api/projects/$id/environments/$env_id/current" "${auth[@]}" | jq -r .release.id)"
[[ -z "$kube" ]] || expect "the URL kept answering v1" v1 "$(serving)"
expect "rollback current refused" 422 "$(code -X POST "$deps/$v1/rollback" "${auth[@]}")"
expect "rollback failed refused" 422 "$(code -X POST "$deps/$bad/rollback" "${auth[@]}")"
v2=$(deploy v2)
expect "v2 live" ready "$(wait_done "$v2")"
[[ -z "$kube" ]] || expect "v2 answers" v2 "$(serving v2)"
back=$(curl -s -X POST "$deps/$v1/rollback" "${auth[@]}" | jq -r .id)
expect "volver a v1" ready "$(wait_done "$back")"
expect "back on v1" "$(curl -s "$deps/$v1" "${auth[@]}" | jq -r .release_id)" "$(curl -s "$api/projects/$id/environments/$env_id/current" "${auth[@]}" | jq -r .release.id)"
[[ -z "$kube" ]] || expect "v1 answers again" v1 "$(serving v1)"
expect "env values stay out of the trail" 0 "$(curl -s -G "$api/audit" -d limit=100 "${auth[@]}" | grep -c hola || true)"
trail=0
for _ in $(seq 1 20); do
  trail=$(curl -s -G "$api/audit" -d subject="deployment/$restore" "${auth[@]}" | jq '[.items[] | select(.type=="deployment.ready")] | length')
  [[ "$trail" == 1 ]] && break; sleep 1
done
expect "audited" 1 "$trail"

expect archive archived "$(curl -s -X POST "$api/projects/$id/archive" "${auth[@]}" | jq -r .status)"
expect "archived refuses changes" 422 "$(code -X PATCH "$api/projects/$id" "${auth[@]}" "${json[@]}" -d '{"name":"x"}')"

exit $fail
