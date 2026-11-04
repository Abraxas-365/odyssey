#!/usr/bin/env bash
# iamkit-provision.sh — idempotently provision an app in ANY IAMKit (local,
# staging, production) and write the app's IAMKIT_* settings to an env file.
#
# Everything is found by name and created only when missing, so re-running it
# after editing the spec (new permissions, new roles) syncs IAMKit. Generalized
# from ../pp/scripts/provision-iam.sh.
#
# Creates / syncs:
#   1. project + environment
#   2. organization (the first tenant)
#   3. application + resource (prefix, audience, permission catalog) + binding
#   4. roles on the resource (spec .roles)
#   5. public OAuth client for the browser app (code + PKCE; hosted login
#      unless HOSTED_LOGIN=0), with redirect/logout URIs and allowed origins
#   6. optional backend service account on the environment's IAM resource
#      (spec .service_account) -> secret written to OUT once
#   7. optional first user (OWNER_EMAIL [+ OWNER_PASSWORD]): member of the
#      organization with the first role of the spec
#   8. optional MCP protected resource (MCP_PRESETS set): the resource of step 3
#      becomes an OAuth 2.1 / RFC 8707 resource server for MCP assistants.
#        - protected resource "Odicea MCP" (consent per user, client metadata
#          documents on, dynamic registration off, admission by allowlist,
#          short access tokens), converged on every run;
#        - the assistants of MCP_PRESETS are admitted: metadata-document
#          presets (claude_code, codex, vscode, …) join the allowlist, template
#          presets (antigravity, cursor, gemini_cli) get their registered client;
#        - a confidential client (token-exchange grant only, never used to sign
#          anyone in) whose only job is calling /oauth/introspect: its id and
#          secret go to OUT as IAMKIT_INTROSPECTION_CLIENT_ID/_SECRET.
#      9. The access client (apps protected with an Odicea login): a
#          confidential authorization_code client with hosted sign-in whose
#          redirect is <API>/access/callback; its id and secret go to OUT as
#          IAMKIT_ACCESS_CLIENT_ID/_SECRET. <API> is ACCESS_API_URL, or the
#          AUDIENCE without its /mcp.
#      The protected resource's URI is the resource's audience, which is
#      immutable: AUDIENCE must already be the MCP endpoint URL (e.g.
#      https://odicea.example.com/mcp). On an older resource with another
#      audience the step is skipped with a WARNING (recreate the resource).
#
# Spec / permission manifest (SPEC=path, JSON) — the single source of the app's
# permissions (see references/permissions-as-code.md):
#   {
#     "resource": {"name": "Invoices API", "prefix": "invoices"},
#     "permissions": [{"name": "invoices:invoices:read", "label": "View invoices"},
#                     "invoices:invoices:write"],
#     "roles": [{"name": "Admin", "permissions": ["invoices:invoices:read","invoices:invoices:write"]},
#               {"name": "Viewer", "permissions": ["invoices:invoices:read"]}],
#     "service_account": {"name": "backend", "iam_permissions": ["iam:members:read","iam:members:write"]}
#   }
#   Permissions are strings or {name, label} objects and must start with
#   "<prefix>:"; role permissions must be in the catalog. roles and
#   service_account are optional; extra keys (e.g. a role "kind") are ignored.
#
# Usage: iamkit-provision.sh [--dry-run] [--prune]
#   --dry-run  print what would change; write nothing (not even OUT)
#   --prune    also remove permissions that left the spec, from the catalog and
#              from the spec's roles. Without it they are KEPT: IAMKit strips a
#              permission removed from the catalog from every role, grant and
#              service account at once, so prune only after no deployed code
#              checks it (rename = add new, sync, deploy, sync --prune).
#
# Variables (defaults in brackets):
#   IAMKIT_URL        IAMKit base URL                              [http://localhost:8080]
#   IAMKIT_KEY        management key (ik_mgmt_…); or IAMKIT_KEY_FILE with .management_key
#   SPEC              manifest file                  [iam/permissions.json]
#   APP_URL           where the app's SPA is served                 [http://localhost:3000]
#   AUDIENCE          resource audience (immutable once created!)   [$APP_URL]
#   CALLBACK_PATH     OAuth redirect path on APP_URL                 [/auth/callback]
#   EXTRA_ORIGINS     comma-separated extra SPA origins (dev ports)
#   PROJECT_NAME [My App]  ENVIRONMENT_NAME [Development]  ORG_NAME [Demo Org]  APP_NAME [Web]
#   HOSTED_LOGIN      1 = IAMKit's hosted sign-in pages, 0 = custom UI  [1]
#   OWNER_EMAIL / OWNER_PASSWORD (12-72 chars) / OWNER_NAME
#   OUT               env file receiving the settings               [.iamkit.env]
#   MCP_PRESETS       comma-separated assistants to admit (step 8); empty = no MCP
#                     e.g. claude_code,codex,vscode,antigravity (see
#                     GET /management/v1/environments/<env>/oauth-client-presets)
#   MCP_SCOPES        comma-separated default scopes of the MCP resource
#                     [<prefix>:projects:read,<prefix>:projects:write,
#                      <prefix>:deployments:read,<prefix>:deployments:create]
#   MCP_TOKEN_TTL     access token lifetime in seconds, 60-3600      [300]
#   ACCESS_API_URL    the API's public base URL, for the access client's
#                     redirect (step 9)                  [$AUDIENCE without /mcp]
#
# Secrets (service account, introspection client) exist only at creation: they
# are kept in OUT (mode 600). If OUT lost one, revoke the account / disable the
# client in IAMKit and re-run.
# Never run with `set -x`: the key and secrets are in play.
set -euo pipefail

command -v curl >/dev/null && command -v jq >/dev/null || { echo "curl and jq are required" >&2; exit 1; }

DRY_RUN=0 PRUNE=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --prune) PRUNE=1 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $arg (try --help)" >&2; exit 2 ;;
  esac
done

IAMKIT_URL=${IAMKIT_URL:-http://localhost:8080}; IAMKIT_URL=${IAMKIT_URL%/}
APP_URL=${APP_URL:-http://localhost:3000}; APP_URL=${APP_URL%/}
AUDIENCE=${AUDIENCE:-$APP_URL}; AUDIENCE_WANTED=$AUDIENCE
CALLBACK_PATH=${CALLBACK_PATH:-/auth/callback}
SPEC=${SPEC:-iam/permissions.json}
PROJECT_NAME=${PROJECT_NAME:-My App}
ENVIRONMENT_NAME=${ENVIRONMENT_NAME:-Development}
ORG_NAME=${ORG_NAME:-Demo Org}
APP_NAME=${APP_NAME:-Web}
HOSTED_LOGIN=${HOSTED_LOGIN:-1}
OUT=${OUT:-.iamkit.env}
MCP_PRESETS=$(tr -d '[:space:]' <<<"${MCP_PRESETS:-}")
MCP_TOKEN_TTL=${MCP_TOKEN_TTL:-300}
[[ "$MCP_TOKEN_TTL" =~ ^[0-9]+$ ]] || { echo "MCP_TOKEN_TTL must be a number of seconds" >&2; exit 1; }

KEY=${IAMKIT_KEY:-}
if [[ -z "$KEY" && -n "${IAMKIT_KEY_FILE:-}" ]]; then KEY=$(jq -er .management_key "$IAMKIT_KEY_FILE"); fi
[[ -n "$KEY" ]] || { echo "Set IAMKIT_KEY (or IAMKIT_KEY_FILE)" >&2; exit 1; }
[[ -f "$SPEC" ]] || { echo "Spec $SPEC not found (set SPEC=path)" >&2; exit 1; }
jq -e . "$SPEC" >/dev/null || { echo "Spec $SPEC is not valid JSON" >&2; exit 1; }
# The spec must be coherent before anything is sent.
problems=$(jq -r '
  (.resource.prefix // "") as $p
  | [.permissions[]? | if type == "string" then . else .name end] as $c
  | (if $p == "" then ["resource.prefix is required"] else [] end)
  + [$c[] | select(startswith($p + ":") | not) | "permission \(.) does not start with \($p):"]
  + [$c | group_by(.)[] | select(length > 1) | "permission \(.[0]) is listed twice"]
  + [.roles[]? as $r | $r.permissions[] | select(IN($c[]) | not) | "role \($r.name): \(.) is not in the catalog"]
  | .[]' "$SPEC")
[[ -z "$problems" ]] || { echo "Spec $SPEC is invalid:" >&2; sed 's/^/  /' <<<"$problems" >&2; exit 1; }

# Secrets created earlier survive a re-run.
IAMKIT_SERVICE_ACCOUNT_SECRET="" IAMKIT_INTROSPECTION_CLIENT_ID="" IAMKIT_INTROSPECTION_CLIENT_SECRET=""
IAMKIT_ACCESS_CLIENT_ID="" IAMKIT_ACCESS_CLIENT_SECRET=""
if [[ -f "$OUT" ]]; then
  IAMKIT_ACCESS_CLIENT_ID=$(sed -n 's/^IAMKIT_ACCESS_CLIENT_ID=//p' "$OUT" | tail -1)
  IAMKIT_ACCESS_CLIENT_SECRET=$(sed -n 's/^IAMKIT_ACCESS_CLIENT_SECRET=//p' "$OUT" | tail -1)
  IAMKIT_SERVICE_ACCOUNT_SECRET=$(sed -n 's/^IAMKIT_SERVICE_ACCOUNT_SECRET=//p' "$OUT" | tail -1)
  IAMKIT_INTROSPECTION_CLIENT_ID=$(sed -n 's/^IAMKIT_INTROSPECTION_CLIENT_ID=//p' "$OUT" | tail -1)
  IAMKIT_INTROSPECTION_CLIENT_SECRET=$(sed -n 's/^IAMKIT_INTROSPECTION_CLIENT_SECRET=//p' "$OUT" | tail -1)
fi

log() { echo "  $*" >&2; }
# change: a log line for a write ("[dry-run]" marked when nothing is written).
change() { if (( DRY_RUN )); then echo "  [dry-run] ${*/ (secret saved to $OUT)/}" >&2; else echo "  $*" >&2; fi; }
step() { echo "$*" >&2; }
q() { jq -Rn --arg v "$1" '$v'; } # JSON string literal for jq filters
(( DRY_RUN )) && echo "Dry run: nothing is written." >&2

# api METHOD PATH < body -> response body; fails on non-2xx with IAMKit's error.
api() {
  curl --fail-with-body --silent --show-error --request "$1" "$IAMKIT_URL/management/v1$2" \
    -H "X-API-Key: $KEY" -H 'Content-Type: application/json' --data-binary @-
}
# write is api for changes: a dry run sends nothing and answers placeholder ids.
write() {
  if (( DRY_RUN )); then cat >/dev/null; echo '{"id":"dry-run","client_id":"dry-run","secret":""}'
  else api "$@"; fi
}
# get: reads; paths under objects a dry run did not create answer an empty list.
get() { if [[ "$1" == *dry-run* ]]; then echo '[]'; else api GET "$1" </dev/null; fi; }
# changes OLD NEW (JSON arrays) -> "+a -b" or empty.
changes() {
  jq -rn --argjson o "$1" --argjson n "$2" \
    '[($n - $o | map("+" + .)), ($o - $n | map("-" + .))] | add | join(" ")'
}
# all PATH -> every item of a list (paged {items,page} or plain array) as one array.
all() {
  local sep='?' offset=0 page total items='[]'
  [[ "$1" == *\?* ]] && sep='&'
  while :; do
    page=$(get "$1${sep}limit=100&offset=$offset")
    items=$(jq -c --argjson p "$page" '. + (if ($p | type) == "object" then $p.items else $p end)' <<<"$items")
    total=$(jq -r 'if type == "object" then .page.total // empty else empty end' <<<"$page")
    offset=$((offset + 100))
    [[ -n "$total" && "$offset" -lt "$total" ]] || break
  done
  echo "$items"
}
# pick JSON-ARRAY JQ-FILTER -> id of the first match, or empty.
pick() { jq -r "[.[] | select($2)][0].id // empty" <<<"$1"; }

get /me >/dev/null || { echo "Management key rejected by $IAMKIT_URL" >&2; exit 1; }

step "1. Project and environment"
project=$(pick "$(get /projects)" ".name == $(q "$PROJECT_NAME")")
if [[ -z "$project" ]]; then
  project=$(jq -n --arg n "$PROJECT_NAME" '{name:$n}' | write POST /projects | jq -er .id); change "created project $PROJECT_NAME"
fi
environment=$(pick "$(get "/projects/$project/environments")" ".name == $(q "$ENVIRONMENT_NAME")")
if [[ -z "$environment" ]]; then
  environment=$(jq -n --arg n "$ENVIRONMENT_NAME" '{name:$n}' | write POST "/projects/$project/environments" | jq -er .id); change "created environment $ENVIRONMENT_NAME"
fi
e="/environments/$environment"
log "environment $environment"

step "2. Organization"
organization=$(pick "$(all "$e/organizations")" ".name == $(q "$ORG_NAME")")
if [[ -z "$organization" ]]; then
  organization=$(jq -n --arg n "$ORG_NAME" '{name:$n}' | write POST "$e/organizations" | jq -er .id); change "created organization $ORG_NAME"
fi
log "organization $organization"

step "3. Application, resource (permission catalog), binding"
callback="$APP_URL$CALLBACK_PATH"
application=$(pick "$(all "$e/applications")" ".name == $(q "$APP_NAME")")
if [[ -z "$application" ]]; then
  application=$(jq -n --arg n "$APP_NAME" --arg r "$callback" '{name:$n,redirect_uris:[$r]}' | write POST "$e/applications" | jq -er .id); change "created application $APP_NAME"
fi
res_name=$(jq -er .resource.name "$SPEC"); prefix=$(jq -er .resource.prefix "$SPEC")
wanted=$(jq -c '[.permissions[] | if type == "string" then . else .name end]' "$SPEC")
stale='[]'
resources=$(all "$e/resources")
resource=$(pick "$resources" ".prefix == $(q "$prefix")")
if [[ -z "$resource" ]]; then
  resource=$(jq -n --arg n "$res_name" --arg p "$prefix" --arg a "$AUDIENCE" --argjson perms "$wanted" \
    '{name:$n,prefix:$p,audience:$a,permissions:$perms}' | write POST "$e/resources" | jq -er .id)
  change "created resource $res_name (audience $AUDIENCE, $(jq length <<<"$wanted") permissions)"
else
  current=$(jq -r --arg id "$resource" '.[] | select(.id == $id) | .audience' <<<"$resources")
  [[ "$current" == "$AUDIENCE" ]] || { log "NOTE: resource audience is $current (immutable); using it"; AUDIENCE=$current; }
  have=$(jq -c --arg id "$resource" '.[] | select(.id == $id) | .permissions // []' <<<"$resources")
  stale=$(jq -cn --argjson h "$have" --argjson w "$wanted" '$h - $w')
  # Without --prune, permissions that left the spec stay in the catalog: IAMKit
  # would strip them from every role, grant and service account (no error).
  catalog=$(if (( PRUNE )); then echo "$wanted"; else jq -cn --argjson w "$wanted" --argjson s "$stale" '$w + $s'; fi)
  diff=$(changes "$have" "$catalog")
  if [[ -n "$diff" ]]; then
    jq -n --arg n "$res_name" --argjson perms "$catalog" '{name:$n,permissions:$perms}' | write PUT "$e/resources/$resource" >/dev/null
    change "catalog: $diff"
  else log "catalog: $(jq length <<<"$catalog") permissions, unchanged"; fi
fi
linked=$(get "$e/applications/$application/resources" | jq -r --arg r "$resource" '[(if type == "object" then .items else . end)[] | select((.id // .resource_id) == $r)] | length')
if [[ "$linked" == 0 ]]; then
  jq -n --arg a "$application" --arg r "$resource" '{application_id:$a,resource_id:$r}' | write POST "$e/application-resources" >/dev/null
  change "linked $APP_NAME -> $res_name"
fi

step "4. Roles"
role_lines=()
first_role=""
roles=$(all "$e/roles")
while IFS= read -r spec_role; do
  [[ -n "$spec_role" ]] || continue
  name=$(jq -r .name <<<"$spec_role")
  perms=$(jq -c .permissions <<<"$spec_role")
  id=$(pick "$roles" ".resource_id == $(q "$resource") and .name == $(q "$name")")
  if [[ -z "$id" ]]; then
    id=$(jq -n --arg n "$name" --arg r "$resource" --argjson p "$perms" '{name:$n,resource_id:$r,permissions:$p}' |
      write POST "$e/roles" | jq -er .id)
    change "created role $name"
  else
    have=$(jq -c --arg id "$id" '.[] | select(.id == $id) | .permissions // []' <<<"$roles")
    # A role keeps the stale permissions it holds until --prune.
    (( PRUNE )) || perms=$(jq -cn --argjson p "$perms" --argjson h "$have" --argjson s "$stale" '$p + ($h - ($h - $s)) | unique')
    diff=$(changes "$have" "$perms")
    if [[ -n "$diff" ]]; then
      jq -n --arg n "$name" --arg r "$resource" --argjson p "$perms" '{name:$n,resource_id:$r,permissions:$p}' |
        write PUT "$e/roles/$id" >/dev/null
      change "role $name: $diff"
    else log "role $name"; fi
  fi
  [[ -n "$first_role" ]] || first_role=$id
  slug=$(tr '[:lower:]' '[:upper:]' <<<"$name" | tr -c 'A-Z0-9\n' '_')
  role_lines+=("IAMKIT_ROLE_${slug}_ID=$id")
done < <(jq -c '.roles // [] | .[]' "$SPEC")
# Roles that are not in the spec are left alone (they may be tenant-made).

step "5. Browser OAuth client (code + PKCE)"
origins=$(jq -cn --arg p "$APP_URL" --arg extra "${EXTRA_ORIGINS:-}" \
  '[$p] + ($extra | split(",") | map(gsub("^\\s+|\\s+$"; "") | sub("/+$"; "")) | map(select(. != ""))) | unique')
hosted=$([[ "$HOSTED_LOGIN" == 1 ]] && echo true || echo false)
web_body=$(jq -c --arg cb "$CALLBACK_PATH" --argjson h "$hosted" \
  '{redirect_uris: map(. + $cb), post_logout_redirect_uris: map(. + "/"), allowed_origins: ., hosted_login: $h}' <<<"$origins")
clients=$(all "$e/oauth-clients?application_id=$application")
web=$(pick "$clients" ".public == true and .resource_id == $(q "$resource") and ((.preset // \"\") == \"\")")
if [[ -z "$web" ]]; then
  web=$(jq -c --arg a "$application" --arg r "$resource" \
    '. + {application_id:$a, resource_id:$r, public:true, grant_types:["authorization_code","refresh_token"]}' <<<"$web_body" |
    write POST "$e/oauth-clients" | jq -er .client_id)
  change "created web client $web"
else
  current=$(jq -c --arg id "$web" '.[] | select(.id == $id) | {redirect_uris, post_logout_redirect_uris, allowed_origins, hosted_login}' <<<"$clients")
  if [[ "$(jq -S . <<<"$current")" != "$(jq -S . <<<"$web_body")" ]]; then
    write PATCH "$e/oauth-clients/$web" <<<"$web_body" >/dev/null; change "synced web client $web"
  else log "web client $web"; fi
fi

service_lines=()
if jq -e '.service_account' "$SPEC" >/dev/null; then
  step "6. Backend service account (IAM resource)"
  sa_name=$(jq -er .service_account.name "$SPEC")
  sa_perms=$(jq -c '.service_account.iam_permissions' "$SPEC")
  iam_resource=$(pick "$(all "$e/resources")" '.prefix == "iam"')
  backend_app="$APP_NAME Backend"
  backend=$(pick "$(all "$e/applications")" ".name == $(q "$backend_app")")
  if [[ -z "$backend" ]]; then
    backend=$(jq -n --arg n "$backend_app" '{name:$n}' | write POST "$e/applications" | jq -er .id); change "created application $backend_app"
  fi
  linked=$(get "$e/applications/$backend/resources" | jq -r --arg r "$iam_resource" '[(if type == "object" then .items else . end)[] | select((.id // .resource_id) == $r)] | length')
  if [[ "$linked" == 0 ]]; then
    jq -n --arg a "$backend" --arg r "$iam_resource" '{application_id:$a,resource_id:$r}' | write POST "$e/application-resources" >/dev/null
    change "linked $backend_app -> IAM"
  fi
  accounts=$(all "$e/service-accounts")
  sa=$(pick "$accounts" ".name == $(q "$sa_name") and (.revoked_at == null)")
  if [[ -z "$sa" ]]; then
    created=$(jq -n --arg n "$sa_name" --arg a "$backend" --arg r "$iam_resource" --argjson p "$sa_perms" \
      '{name:$n,application_id:$a,resource_id:$r,permissions:$p,expires_in:"never"}' | write POST "$e/service-accounts")
    sa=$(jq -er .id <<<"$created"); IAMKIT_SERVICE_ACCOUNT_SECRET=$(jq -r .secret <<<"$created")
    change "created service account $sa_name (secret saved to $OUT)"
  else
    # IAMKit has no route to change a service account's permissions.
    diff=$(changes "$(jq -c --arg id "$sa" '.[] | select(.id == $id) | .permissions // []' <<<"$accounts")" "$sa_perms")
    [[ -z "$diff" ]] || log "WARNING: service account $sa_name permissions differ from the spec ($diff): revoke it in IAMKit and re-run (new secret)"
    if [[ -z "$IAMKIT_SERVICE_ACCOUNT_SECRET" ]]; then
      log "WARNING: service account $sa_name exists but its secret is not in $OUT: revoke it in IAMKit and re-run"
    else log "service account $sa_name"; fi
  fi
  service_lines+=("IAMKIT_SERVICE_ACCOUNT_ID=$sa" "IAMKIT_SERVICE_ACCOUNT_SECRET=$IAMKIT_SERVICE_ACCOUNT_SECRET")
fi

if [[ -n "${OWNER_EMAIL:-}" ]]; then
  step "7. First user $OWNER_EMAIL"
  user=$(get "$e/users?search=$(jq -rn --arg v "$OWNER_EMAIL" '$v|@uri')&limit=100" |
    jq -r --arg m "$OWNER_EMAIL" '[(if type == "object" then .items else . end)[] | select(.email | ascii_downcase == ($m | ascii_downcase))][0].id // empty')
  if [[ -z "$user" && -n "${OWNER_PASSWORD:-}" ]]; then
    user=$(jq -n --arg m "$OWNER_EMAIL" --arg pw "$OWNER_PASSWORD" --arg n "${OWNER_NAME:-${OWNER_EMAIL%@*}}" \
      '{name:$n,email:$m,password:$pw}' | write POST "$e/users" | jq -er .id); change "created user"
  fi
  if [[ -z "$user" ]]; then
    log "user not found and OWNER_PASSWORD unset: skipped (invite them instead)"
  else
    members=$(all "$e/organizations/$organization/members")
    if [[ -z "$(jq -r --arg u "$user" '[.[] | select((.user_id // .id) == $u)][0] // empty' <<<"$members")" ]]; then
      jq -n --arg o "$organization" --arg u "$user" '{organization_id:$o,user_id:$u}' | write POST "$e/memberships" >/dev/null; change "added to $ORG_NAME"
    fi
    if [[ -n "$first_role" ]]; then
      held=$(all "$e/role-assignments?organization_id=$organization&user_id=$user&role_id=$first_role" | jq length)
      if [[ "$held" == 0 ]]; then
        jq -n --arg o "$organization" --arg u "$user" --arg r "$first_role" '{organization_id:$o,user_id:$u,role_id:$r}' |
          write POST "$e/role-assignments" >/dev/null
        change "assigned the first role"
      fi
    fi
  fi
fi

mcp_lines=()
if [[ -n "$MCP_PRESETS" ]]; then
  step "8. MCP protected resource (assistants: $MCP_PRESETS)"
  if [[ "$AUDIENCE" != "$AUDIENCE_WANTED" ]]; then
    # The protected resource's URI is the resource's audience, immutable.
    log "WARNING: resource audience is $AUDIENCE but the MCP endpoint is $AUDIENCE_WANTED: step skipped."
    log "         Recreate the resource (local dev: make reset && make seed; elsewhere delete the resource in IAMKit and re-run)."
  else
    # The application <-> resource link of step 3 is what lets the
    # application's clients ask for this resource.
    default_scopes="$prefix:projects:read,$prefix:projects:write,$prefix:deployments:read,$prefix:deployments:create"
    mcp_scopes=$(jq -cn --arg s "${MCP_SCOPES:-$default_scopes}" '$s | split(",") | map(gsub("^\\s+|\\s+$"; "")) | map(select(. != "")) | unique')
    outside=$(jq -cn --argjson s "$mcp_scopes" --argjson w "$wanted" '$s - $w')
    [[ "$outside" == "[]" ]] || { echo "MCP_SCOPES not in the catalog: $outside" >&2; exit 1; }
    # idle/max TTL: how long an assistant stays signed in without / with refresh.
    want=$(jq -cn --argjson s "$mcp_scopes" --argjson t "$MCP_TOKEN_TTL" \
      '{default_scopes:$s, access_token_ttl_seconds:$t, consent_mode:"per_user", allow_client_metadata:true, allow_dynamic_registration:false, client_admission:"allowlist"}')

    # a. The protected resource of (application, resource).
    protected=$(all "$e/protected-resources")
    pr=$(pick "$protected" ".application_id == $(q "$application") and .resource_id == $(q "$resource")")
    if [[ -z "$pr" ]]; then
      created=$(jq -n --arg a "$application" --arg r "$resource" --argjson w "$want" \
        '{application_id:$a, resource_id:$r, name:"Odicea MCP", default_scopes:$w.default_scopes,
          consent_mode:$w.consent_mode, access_token_ttl_seconds:$w.access_token_ttl_seconds,
          idle_ttl_seconds:604800, max_ttl_seconds:2592000}' | write POST "$e/protected-resources")
      pr=$(jq -er .id <<<"$created"); change "created protected resource $pr (audience $AUDIENCE, access token ${MCP_TOKEN_TTL}s)"
      # Creation takes only some fields; the rest (admission, metadata
      # documents, registration) are converged below, before anyone can ask.
      have=$(jq -c 'if .id == "dry-run" then {} else . end' <<<"$created")
    else
      have=$(jq -c --arg id "$pr" '.[] | select(.id == $id)' <<<"$protected")
    fi
    # Converge: only what differs is sent (default_scopes compared as a set).
    diff=$(jq -cn --argjson h "$have" --argjson w "$want" '
      ($h | .default_scopes = ((.default_scopes // []) | sort)) as $h
      | $w | with_entries(select(.value != $h[.key]))')
    if [[ "$diff" != "{}" ]]; then
      write PATCH "$e/protected-resources/$pr" <<<"$diff" >/dev/null
      change "protected resource: $(jq -r 'to_entries | map("\(.key)=\(.value | tostring)") | join(" ")' <<<"$diff")"
    else log "protected resource $pr, unchanged"; fi
    mcp_lines+=("IAMKIT_PROTECTED_RESOURCE_ID=$pr")

    # b. The assistants IT admits. Metadata-document presets go to the
    #    allowlist; template presets are registered public clients.
    catalog=$(get "$e/oauth-client-presets" | jq -c 'if type == "object" then .items else . end')
    allowlist=$(all "$e/oauth-client-allowlist?protected_resource_id=$pr")
    registered=$(all "$e/oauth-clients?application_id=$application")
    while IFS= read -r preset; do
      [[ -n "$preset" ]] || continue
      mechanism=$(jq -r --arg k "$preset" '[.[] | select(.key == $k)][0].mechanism // empty' <<<"$catalog")
      # A dry run of a brand-new environment has no catalog to read: assume a document.
      [[ -n "$mechanism" || "$catalog" != "[]" ]] || mechanism=metadata_document
      [[ -n "$mechanism" ]] || { echo "unknown MCP preset '$preset' (known: $(jq -r 'map(.key) | join(", ")' <<<"$catalog"))" >&2; exit 1; }
      if [[ "$mechanism" == template ]]; then
        client=$(pick "$registered" ".preset == $(q "$preset") and .resource_id == $(q "$resource")")
        if [[ -z "$client" ]]; then
          client=$(jq -n --arg p "$preset" '{preset:$p}' | write POST "$e/protected-resources/$pr/client-templates" | jq -er .client.client_id) || client=dry-run
          change "created $preset client $client"
        else log "$preset client $client"; fi
        mcp_lines+=("IAMKIT_MCP_CLIENT_$(tr '[:lower:]' '[:upper:]' <<<"$preset")_ID=$client")
      else
        # An entry of the whole environment (no protected resource) admits it too.
        if [[ -z "$(jq -r --arg k "$preset" '[.[] | select(.preset == $k)][0].id // empty' <<<"$allowlist")" ]]; then
          jq -n --arg p "$preset" --arg r "$pr" '{preset:$p, protected_resource_id:$r}' | write POST "$e/oauth-client-presets" >/dev/null
          change "admitted $preset"
        else log "$preset admitted"; fi
      fi
    done < <(tr ',' '\n' <<<"$MCP_PRESETS")
    # Taking an assistant out of MCP_PRESETS does not revoke it (users may be
    # signed in with it): say so, and how.
    wanted_presets=$(jq -cn --arg p "$MCP_PRESETS" '$p | split(",") | map(select(. != ""))')
    while IFS= read -r line; do
      [[ -n "$line" ]] && log "WARNING: $line is still admitted but not in MCP_PRESETS: revoke it in IAMKit to stop it"
    done < <(jq -r --argjson w "$wanted_presets" '.[] | select(.preset != null and (.preset | IN($w[]) | not)) | "\(.preset) (allowlist entry \(.id))"' <<<"$allowlist"
             jq -r --argjson w "$wanted_presets" --arg r "$resource" '.[] | select(.preset != null and .resource_id == $r and (.preset | IN($w[]) | not)) | "\(.preset) (client \(.id))"' <<<"$registered"; true)
    # c. The introspection client. IAMKit has no client names, so it is a
    #    confidential client of this resource that only holds the token
    #    exchange grant (nothing signs in with it). Its secret exists only at
    #    creation: only the client whose secret OUT keeps is reused; without
    #    it a new one is created and the others are listed to disable.
    exchange="urn:ietf:params:oauth:grant-type:token-exchange"
    candidates=$(jq -c --arg r "$resource" --arg g "$exchange" \
      '[.[] | select(.resource_id == $r and .public == false and .active == true and .grant_types == [$g])]' <<<"$registered")
    client=""
    if [[ -n "$IAMKIT_INTROSPECTION_CLIENT_SECRET" ]]; then
      client=$(jq -r --arg id "$IAMKIT_INTROSPECTION_CLIENT_ID" 'map(select(.id == $id))[0].id // empty' <<<"$candidates")
    fi
    if [[ -z "$client" ]]; then
      created=$(jq -n --arg a "$application" --arg r "$resource" --arg g "$exchange" \
        '{application_id:$a, resource_id:$r, public:false, hosted_login:false, grant_types:[$g]}' | write POST "$e/oauth-clients")
      client=$(jq -er .client_id <<<"$created"); IAMKIT_INTROSPECTION_CLIENT_SECRET=$(jq -r '.client_secret // ""' <<<"$created")
      change "created introspection client $client (secret saved to $OUT)"
    else log "introspection client $client"; fi
    while IFS= read -r orphan; do
      [[ -n "$orphan" && "$orphan" != "$client" ]] || continue
      log "WARNING: introspection client $orphan has no saved secret: disable it in IAMKit (DELETE $e/oauth-clients/$orphan)"
    done < <(jq -r '.[].id' <<<"$candidates")
    IAMKIT_INTROSPECTION_CLIENT_ID=$client
  fi
fi
# The introspection client's id and secret are written together or not at
# all (the API refuses half of one), and survive a run that skips step 8
# (the secret exists nowhere else).
if [[ -n "$IAMKIT_INTROSPECTION_CLIENT_ID" && -n "$IAMKIT_INTROSPECTION_CLIENT_SECRET" ]]; then
  mcp_lines+=("IAMKIT_INTROSPECTION_CLIENT_ID=$IAMKIT_INTROSPECTION_CLIENT_ID" "IAMKIT_INTROSPECTION_CLIENT_SECRET=$IAMKIT_INTROSPECTION_CLIENT_SECRET")
fi

# 9. The access client. Apps protected with an Odicea login send the browser
#    to <API>/access/authorize; the API signs the user in with this
#    confidential client (authorization code + PKCE) and redirects back to
#    <API>/access/callback. Like the introspection client its secret exists
#    only at creation: only the client whose secret OUT keeps is reused.
ACCESS_API_URL=${ACCESS_API_URL:-}
if [[ -z "$ACCESS_API_URL" && "$AUDIENCE_WANTED" == */mcp ]]; then ACCESS_API_URL=${AUDIENCE_WANTED%/mcp}; fi
ACCESS_API_URL=${ACCESS_API_URL%/}
if [[ -n "$ACCESS_API_URL" ]]; then
  step "9. Access client (redirect $ACCESS_API_URL/access/callback)"
  access_body=$(jq -cn --arg r "$ACCESS_API_URL/access/callback" '{redirect_uris:[$r], hosted_login:true}')
  registered=$(all "$e/oauth-clients?application_id=$application")
  candidates=$(jq -c --arg r "$resource" \
    '[.[] | select(.resource_id == $r and .public == false and .active == true and .grant_types == ["authorization_code"])]' <<<"$registered")
  client=""
  if [[ -n "$IAMKIT_ACCESS_CLIENT_SECRET" ]]; then
    client=$(jq -r --arg id "$IAMKIT_ACCESS_CLIENT_ID" 'map(select(.id == $id))[0].id // empty' <<<"$candidates")
  fi
  if [[ -z "$client" ]]; then
    created=$(jq -c --arg a "$application" --arg r "$resource" \
      '. + {application_id:$a, resource_id:$r, public:false, grant_types:["authorization_code"]}' <<<"$access_body" | write POST "$e/oauth-clients")
    client=$(jq -er .client_id <<<"$created"); IAMKIT_ACCESS_CLIENT_SECRET=$(jq -r '.client_secret // ""' <<<"$created")
    change "created access client $client (secret saved to $OUT)"
  else
    current=$(jq -c --arg id "$client" '.[] | select(.id == $id) | {redirect_uris, hosted_login}' <<<"$registered")
    if [[ "$(jq -S . <<<"$current")" != "$(jq -S . <<<"$access_body")" ]]; then
      write PATCH "$e/oauth-clients/$client" <<<"$access_body" >/dev/null; change "synced access client $client"
    else log "access client $client"; fi
  fi
  while IFS= read -r orphan; do
    [[ -n "$orphan" && "$orphan" != "$client" ]] || continue
    log "WARNING: access client $orphan has no saved secret: disable it in IAMKit (DELETE $e/oauth-clients/$orphan)"
  done < <(jq -r '.[].id' <<<"$candidates")
  IAMKIT_ACCESS_CLIENT_ID=$client
else
  step "9. Access client skipped: set ACCESS_API_URL (the API's public URL) or an AUDIENCE ending in /mcp."
fi
# Written together or not at all (the API refuses half of one).
if [[ -n "$IAMKIT_ACCESS_CLIENT_ID" && -n "$IAMKIT_ACCESS_CLIENT_SECRET" ]]; then
  mcp_lines+=("IAMKIT_ACCESS_CLIENT_ID=$IAMKIT_ACCESS_CLIENT_ID" "IAMKIT_ACCESS_CLIENT_SECRET=$IAMKIT_ACCESS_CLIENT_SECRET")
fi

if [[ "$(jq length <<<"$stale")" != 0 ]]; then
  if (( PRUNE )); then step "Pruned (no longer in $SPEC): $(jq -r 'join(", ")' <<<"$stale")"
  else step "Kept (no longer in $SPEC; remove with --prune once no deployed code checks them): $(jq -r 'join(", ")' <<<"$stale")"; fi
fi
if (( DRY_RUN )); then step "Dry run: nothing written."; exit 0; fi

issuer=$(curl --fail --silent --show-error "$IAMKIT_URL/.well-known/openid-configuration" | jq -er .issuer)
umask 077
{
  echo "# written by iamkit-provision.sh — keep out of git (contains secrets)"
  echo "IAMKIT_URL=$IAMKIT_URL"
  echo "IAMKIT_ISSUER=$issuer"
  echo "IAMKIT_ENVIRONMENT_ID=$environment"
  echo "IAMKIT_ORGANIZATION_ID=$organization"
  echo "IAMKIT_APPLICATION_ID=$application"
  echo "IAMKIT_RESOURCE_ID=$resource"
  echo "IAMKIT_AUDIENCE=$AUDIENCE"
  echo "IAMKIT_WEB_CLIENT_ID=$web"
  for l in "${role_lines[@]+"${role_lines[@]}"}" "${service_lines[@]+"${service_lines[@]}"}" "${mcp_lines[@]+"${mcp_lines[@]}"}"; do echo "$l"; done
} >"$OUT"
chmod 600 "$OUT"
step "Done -> $OUT"
