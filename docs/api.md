# Odyssey API — HTTP contract

The rules every endpoint of `odyssey-api` follows. Code comments cite these
sections (`docs/api.md §N`); keep the numbering stable.

The exact shapes live in [`api/openapi.yaml`](../api/openapi.yaml) (OpenAPI
3.1, written by hand). It is enforced, not just documented:
`internal/bootstrap/openapi_test.go` fails when a mounted route is missing
from it (or vice versa), when an operation's `x-permissions` differ from what
the route enforces, or when a real response does not validate. The console's
types are generated from it (`make api-types`; `make web-check` fails while
they are stale). Change an endpoint → edit the spec in the same change.

## 1. Basics

- Base path `/api/v1`. JSON only. Fields are `snake_case`. Paths are plural,
  lowercase, with `-`.
- Ids are UUID strings.
- **The tenant comes only from the token** (`httpx.Organization`). No route
  takes an organization id from the path, the query or the body. Another
  tenant's id answers **404**, never 403.
- Authentication: `Authorization: Bearer <IAMKit access token>`. Each route
  requires permissions from `iam/permissions.json` (`odyssey:<resource>:<action>`).
- Unauthenticated: `GET /health` and `GET /api/config`, which returns the
  dashboard's sign-in settings (`iam_url`, `client_id`, `environment_id`);
  and the few public routes of §11 (`GET /github/setup`, `POST
  /webhooks/github`, `PUT /uploads/:ticket`, `GET /skills/odicea.tar.gz`, and the access
  sign-in `GET /.well-known/odicea-access-keys`, `GET /access/authorize`, `GET /access/callback`).

| Operation | Success | Body |
|---|---|---|
| `GET /things` | 200 | `{items, page}` |
| `GET /things/:id` | 200 | resource |
| `POST /things` | **201** + `Location` | full resource |
| `PATCH /things/:id` | **200** | full resource after the change |
| `DELETE /things/:id` | 204 | none |
| `POST /things/:id/<verb>` | 200, or 201 if it creates a resource | result |
| Starts background work (a deployment) | **202** + `Location` of the thing to poll | that thing, pending |

## 2. Statuses and error types

One `errx.Type` per status. Nothing else decides a status.

| Status | `type` | Meaning |
|---|---|---|
| 400 | `VALIDATION` | malformed body, unknown field, bad enum/uuid/limit, bad query param |
| 401 | `AUTHORIZATION` (`code` `UNAUTHORIZED`) | no valid token |
| 403 | `AUTHORIZATION` (`code` `FORBIDDEN`) | valid token, missing permission |
| 404 | `NOT_FOUND` | missing **or another tenant's** id; unknown route |
| 405 | `VALIDATION` (`code` `METHOD_NOT_ALLOWED`) | method not allowed |
| 409 | `CONFLICT` | unique conflict, serialization failure (retry) |
| 413 | `VALIDATION` (`code` `PAYLOAD_TOO_LARGE`) | body too large |
| 415 | `VALIDATION` (`code` `UNSUPPORTED_MEDIA_TYPE`) | not JSON, or compressed body |
| 422 | `BUSINESS` | valid input that breaks a rule (e.g. `PROJECT_ARCHIVED`) |
| 500 | `INTERNAL` | anything unexpected (generic message) |
| 502 / 503 | `EXTERNAL` | dependency down |

Domain codes in use: `PROJECT_ARCHIVED` (422).

## 3. Error body

One shape for every error, including the framework's own 404/405/413:

```json
{"error":{"type":"VALIDATION","code":"VALIDATION","message":"slug must be 3-40 characters: …",
          "status_code":400,"details":{"field":"slug"},"request_id":"6f1c…"}}
```

- Clients branch on `type`, a closed set. `code` can be more specific, and new
  codes are additive. `message` is for humans and must never be parsed.
- 5xx: the message is always generic and details are dropped. The cause is
  only in the log next to `request_id`.

## 4. Lists

- Every list returns the envelope `{"items":[…],"page":{"total":N,"limit":N,"offset":N}}`.
- Pagination limits:
  - `limit` is 1–100, default 20. Values above 100 are clamped.
  - `offset` is 0–10000.
  - A non-numeric or out-of-range value is **400**, not ignored.
- `search` is at most 200 characters.
- Enum filters are validated. A repeated param means OR (`?status=a&status=b`).
- Every list documents its default order and breaks ties by `id`.

## 5. Text

Text fields are trimmed and bounded: names 200 characters, descriptions 2000.
Control characters other than `\n` and `\t` are rejected, so a NUL never
reaches Postgres as a 500.

## 6. Request bodies

- Decoding is strict:
  - `Content-Type: application/json`, else 415.
  - At most 1 MiB, else 413.
  - **Unknown fields → 400.**
  - One JSON value with nothing after it.
  - No `Content-Encoding`, else 415.
- **PATCH** fields work as follows:
  - Absent means unchanged, a value sets the field, and `null` clears it.
  - Only nullable fields accept `null`; `null` elsewhere is 400.
  - A patch that changes nothing returns 200 without writing.
- Fields the server owns (`created_by`, `organization_id`, …) come from the
  token, never from the body.
- **Multipart** (`multipart/form-data`) is accepted only where an endpoint
  says so (deploy from source). Parts are read in order and streamed, never
  buffered whole: a small JSON part first (strict as above, ≤ 256 KiB), then
  the file. A body over the endpoint's limit is 413, without reading it
  into memory.
- **Raw archive** (`application/gzip`, or `application/octet-stream`) is
  accepted only by `PUT /uploads/:ticket` (§11). It streams like multipart:
  never buffered, 413 beyond the limit (by `Content-Length`, or by counting
  when chunked), and the connection closes after the response. Any JSON route
  refuses it with 415.

## 7. Response fields

- Timestamps are RFC 3339 UTC and their names end in `_at`. Calendar dates
  are `YYYY-MM-DD`.
- Optional fields are always present (`null`), never omitted.
- Enums are lowercase strings. Clients must tolerate new values.

## 8. Compatibility

Additive changes are not breaking: new endpoints, optional request fields,
response fields, enum values and error codes. Removing, renaming or retyping
a field, or changing a success status, needs `/v2` or `Deprecation` +
`Sunset` headers.

## 9. Limits and timeouts

- Request body limit: 1 MiB (JSON). A deploy from source accepts a source
  of up to 100 MiB compressed (500 MiB and 20 000 entries unpacked).
- Uploaded sources: up to `ODYSSEY_SOURCE_QUOTA_GIB` GiB compressed per
  organization (default 10). When an upload would exceed it, the oldest
  sources already built are dropped early (their images and releases stay;
  event `source.expired`); when none can be dropped, 422 `SOURCE_QUOTA`.
  Unbuilt sources expire after 24 h, built ones after 7 days.
- Server timeouts: read 30 s, write 60 s, idle 120 s.
- Database sessions: `statement_timeout` 30 s, `lock_timeout` 10 s,
  `idle_in_transaction_session_timeout` 60 s, `TimeZone` UTC. A value set in
  `DATABASE_URL` wins.
- Migrations run under an advisory lock with the timeouts lifted.

## 10. Headers

- Every response carries `X-Request-ID`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer` and `Cache-Control: no-store`, plus HSTS when
  the request is TLS.
- A client `X-Request-ID` is kept only if it is a UUID or at most 64
  characters of `[A-Za-z0-9_-]`.
- CORS is closed unless `CORS_ORIGINS` is set.

## 11. Endpoints

### Projects (`odyssey:projects:read` / `odyssey:projects:write`)

A project groups everything a team ships: environments, services, builds,
releases and resources. Projects are archived, never deleted.

| Method | Path | Permission | Success |
|---|---|---|---|
| `GET` | `/api/v1/projects` | read | 200 `{items, page}` |
| `POST` | `/api/v1/projects` | write | 201 + `Location`, project |
| `GET` | `/api/v1/projects/:id` | read | 200 project |
| `PATCH` | `/api/v1/projects/:id` | write | 200 project |
| `POST` | `/api/v1/projects/:id/archive` | write | 200 project (idempotent) |

The project resource:

```json
{"id":"…","organization_id":"…","name":"Portal de Ventas","slug":"portal-de-ventas",
 "description":"","status":"active","created_by":"…","updated_by":"…",
 "created_at":"2026-10-26T12:00:00Z","updated_at":"2026-10-26T12:00:00Z","archived_at":null}
```

Rules:

- **Create** takes `{name, slug?, description?}`.
  - `slug` is a DNS label: 3–40 characters of `[a-z0-9-]`, starting with a
    letter, not ending with `-`, no `--`. It becomes part of hostnames.
  - When `slug` is absent it is derived from `name` (accents folded).
  - The slug is unique per organization, forever: a duplicate is 409.
- **Patch** takes `{name?, description?}`. The slug is immutable.
- **Archived** projects refuse changes with 422 `PROJECT_ARCHIVED`.
- **List filters**: `status` (`active` | `archived`, repeatable, default
  `active`) and `search` (name or slug).
- **List order**: `lower(name)`, then `id`.

### Environments (`odyssey:projects:read`)

Every app is born with one environment, `production` (kind `production`),
created in the same transaction. `staging` and `preview` kinds arrive with
GitHub sources.

| Method | Path | Permission | Success |
|---|---|---|---|
| `GET` | `/api/v1/projects/:id/environments` | read | 200 `{items}` (filter `kind`, repeatable) |
| `GET` | `/api/v1/projects/:id/environments/:environment` | read | 200 environment |

### Deployments (`odyssey:deployments:read` / `:create` / `:rollback`)

A **release** is an image pinned by digest plus its configuration; it never
changes. A **deployment** is one attempt to make a release the one an
environment serves. The console calls both "versión".

| Method | Path | Permission | Success |
|---|---|---|---|
| `GET` | `/api/v1/projects/:id/environments/:environment/current` | read | 200 `{environment_id, url, release, deployment, active, applied_settings_revision}` |
| `GET` | `/api/v1/projects/:id/environments/:environment/deployments` | read | 200 `{items, page}`, newest first (filter `status`) |
| `POST` | `/api/v1/projects/:id/environments/:environment/deployments` | create | **202** + `Location`, deployment |
| `GET` | `/api/v1/projects/:id/environments/:environment/deployments/:deployment` | read | 200 deployment with `steps` |
| `POST` | `/api/v1/projects/:id/environments/:environment/deployments/:deployment/rollback` | rollback | **202** + `Location` of the new deployment |
| `GET` | `/api/v1/projects/:id/environments/:environment/settings` | projects:read | 200 `{revision, access:{mode}, access_login_available, access_protection_available}` |
| `PUT` | `/api/v1/projects/:id/environments/:environment/access` | projects:write (+ `odyssey:access:public` for `public`) | **202** `{settings, deployment\|null, access_login_available}` + `Location` when a deployment starts |
| `GET` | `/api/v1/projects/:id/releases/:release` | read | 200 release |
| `GET` | `/api/v1/projects/:id/builds/:build` | read | 200 build |
| `GET` | `/api/v1/projects/:id/builds/:build/logs` | read | 200 `{lines, next, finished}` (`after`, `limit` ≤ 1000) |

- **`requested_via`** (on every deployment) says where the request came from.
  The server derives it; no request body can set it: `console`,
  `assistant:<preset>` (`claude_code`, `codex`, `vscode`, `antigravity`, or
  `other` for an MCP client with no preset; read from the request's OAuth
  client), `github` (nobody asked: a push or the poll of a connected
  repository) or `platform` (`auto_rollback`). It follows the requester, not
  where the code lives: `publish_now` / connecting a repository carries the
  client of the request that queued the push (`source.pushes.requested_client`),
  so it is `assistant:<preset>` from an assistant and `console` from the
  console. `""` for deployments made before it existed. It is not an
  authorization input, only attribution.
- **Deploy** takes `{image, port?, health_path?, replicas?, env?}`. `image` must
  be `registry/repository@sha256:<64 hex>`: tags are refused (400, field
  `image`). Defaults: `port` 8080 (the agent also sets `$PORT` to it),
  `health_path` `/`, `replicas` 1 — the same for every body that carries a
  release config (deploy from source, connect a repository). `env` holds non-secret
  values; `ODYSSEY_*` names are reserved. Its values never reach events or
  the audit trail (only the names do).
- **Deploy from source** sends the same endpoint `multipart/form-data` (§6):
  part `config` = `{port?, health_path?, replicas?, env?}` (no `image`), then
  part `source` = a `.tar.gz` of the app's folder. The archive is checked
  before anything starts: no absolute paths, `..`, links or devices, within
  the limits of §9; otherwise 400 `SOURCE_INVALID` (field `source`). The
  deployment then starts at the `build` step (status `building`,
  `release_id` null, `build_id` and `source` set); the release is created
  when the image is built. The build runs in the environment's cluster,
  whose agent fetches the source through the tunnel. Its log is readable
  while it runs (`/builds/:build/logs`, poll with `after`). Uploaded sources
  are deleted after 7 days (24 h if never built); the image outlives them.
- **Lifecycle**: `[building →] pending → applying → verifying → ready |
  failed | cancelled`, through the steps `build` (from source only),
  `apply`, `wait_ready` (≤ 5 min for the health check) and `promote`. Only
  `promote` changes what the environment serves, so a version that fails
  never replaces the one online.
- **One at a time**: a new deployment cancels the one in progress
  (`cancelled`, event `deployment.cancelled`), and its build if it is still
  building.
- **Automatic restore**: when a deployment fails and another release was
  online, the platform starts a deployment of it with reason `auto_rollback`
  (`rollback_of` = the failed one). A failed `auto_rollback` is not retried.
  A failed build restores nothing: the cluster never changed.
- **Who can open it** ("Quién puede abrirla", cut 1e; contract of the gate in
  `docs/access.md`). The environment's **settings** are an append-only history
  of revisions (0 = the defaults, access `team`); `PUT …/access {mode}` with
  `team` | `organization` | `public` (`public` also needs
  `odyssey:access:public`, else 403) stores the next revision (the mode already
  in force adds none) and publishes `access.changed`. Settings are resolved
  **when a deployment applies**, not when it starts: the apply step reads the
  latest revision and records it on the deployment (`settings_revision`), so
  the last change wins during a deployment. If settings changed after that
  step, the deployment that goes online is followed by a deployment with
  reason `config`; a PUT with a release online and nothing in progress starts
  that deployment at once (the same release, so the console says «Cambió quién
  puede abrirla»), and with no release online only stores the settings. A
  rollback keeps the current settings. A `config` deployment that fails is not
  repeated on its own and nothing is restored. Refused at `apply`, the cluster
  never got the mode (it keeps the previous one) and asking for the mode again
  retries it; failed *after* apply (the app is not healthy), the cluster keeps
  the new mode (fail-closed, see `docs/access.md`) and `applied_settings_revision`
  says so. A protected mode on a
  cluster whose agent lacks the `odicea.access` capability fails at `apply`
  with `APPLY_FAILED` and the message «Este clúster no puede proteger apps con
  inicio de sesión. Pide a TI que lo habilite.». `access_login_available` is
  false when this control plane has no signing key or login client
  (`/access/*` answers 404): protected apps cannot be opened.
  `access_protection_available` is false when the environment's cluster cannot
  protect apps yet (no `odicea.access`; an unplaced environment is judged by the
  organization's default cluster, false with none): only `public` can be applied
  there. Environments that were online before this feature got a settings row
  `public` (revision 1, no author) in migration 008: they were served open, and
  the record says so; new environments default to `team`.
  `current.applied_settings_revision` is the settings revision the cluster was
  last told, set by the apply step the moment it hands the app over; a `config`
  deployment that fails *after* that does not reopen the app on its own (the
  cluster keeps the mode it was told), and the console shows the change as not
  applied while the latest revision is higher.
- **Errors** (`error.code`): `HEALTH_CHECK_FAILED` (it runs but its health
  path does not answer), `IMAGE_PULL_FAILED` (the cluster cannot download the
  image), `APP_CRASHED` (it exits on start), `NOT_READY` (timeout),
  `APPLY_FAILED` (the cluster refused it), `PROMOTE_FAILED`. From the build:
  `UNSUPPORTED_APP` (no known way to build or start it), `BUILD_FAILED`,
  `BUILD_TIMEOUT` (25 min), `SOURCE_INVALID`, `SOURCE_EXPIRED`,
  `PUSH_FAILED`, `NO_BUILDER`, `NO_CLUSTER`, `BUILD_INTERNAL`. `message` is
  plain Spanish for whoever deploys; `detail` holds the cluster's own words
  (often English, `""` when it said nothing) for technical readers.
- **Rollback** ("Volver a esta") takes no body. It deploys the release of a
  deployment that went live (`ready`), without building again; otherwise 422
  `NOT_ROLLBACKABLE`. 422
  `ALREADY_CURRENT` if that release is the one online and nothing is in
  progress. Archived apps refuse both with 422 `PROJECT_ARCHIVED`.
- **Events**: `release.created`, `deployment.started`, `deployment.ready`,
  `deployment.failed`, `deployment.cancelled` (subject `deployment/<id>`);
  `source.uploaded`, `build.started`, `build.succeeded`, `build.failed`,
  `build.cancelled`, `source.expired`.
- **Where it runs** (`ODYSSEY_CLUSTER=fleet`): an environment's first
  deployment pins it to the organization's first-enrolled active cluster; it stays
  there. With none, the deployment fails with `APPLY_FAILED` and a message
  that tells TI to connect one; a revoked cluster fails the same way (until
  TI moves its apps: see Clusters).

### Clusters (`odyssey:clusters:read` / `odyssey:clusters:manage`)

The organization's Kubernetes clusters (console: Infraestructura →
Clústeres). The platform never holds cluster credentials: each cluster's
agent dials out to the gateway over mutual TLS, so it needs no inbound port.

| Method | Path | Permission | Success |
|---|---|---|---|
| `GET` | `/api/v1/clusters` | read | 200 `{items, page}` by name, revoked last (filter `status`) |
| `POST` | `/api/v1/clusters` | manage | **201** + `Location`, `{cluster, enrollment}` |
| `GET` | `/api/v1/clusters/:id` | read | 200 cluster |
| `POST` | `/api/v1/clusters/:id/enrollment-token` | manage | 200 enrollment token |
| `POST` | `/api/v1/clusters/:id/revoke` | manage | 200 cluster (`revoked`) |
| `POST` | `/api/v1/clusters/:id/move` | manage | 200 `{environment_ids, apps}` |

- **Statuses**: `pending` (registered, waiting for its agent) → `active`
  (enrolled) → `revoked` (for good: its certificate stops working at once and
  its tunnel closes). `connected` is separate: the agent was heard from in
  the last 90 s. `apps` counts the apps placed on it.
- **Enrolment token** (`{token, expires_at, gateway, ca_pin}`): shown only in
  the response that creates it (`Cache-Control: no-store`; only its hash is
  kept). One use, 24 h. A new one replaces the pending one; issuing one for an
  active cluster lets its agent enrol again (lost key), after which its
  previous certificate stops working. A revoked cluster refuses with 422
  `CLUSTER_REVOKED`. `ca_pin` (`sha256:<hex>` of the fleet CA's public key)
  is the only CA the agent trusts.
- **Names** are unique among the organization's clusters not revoked (409).
- **Revoke** is idempotent (revoking a revoked cluster answers 200 and
  records nothing, except a move if `move_to` is given). The cluster and its
  history stay readable. Its apps keep running there, but their deployments
  fail (`CLUSTER_REVOKED`) until TI moves them: nothing moves on
  its own, because the other cluster may be on another network or far from
  the apps' data. The body is optional: `{"move_to": id}` (another active
  cluster) also moves them, in the same transaction; a bad target (422
  `INVALID_MOVE_TARGET`) refuses the revocation too.
- **Move** (`{"to": id}`) sends the apps of a revoked cluster to another
  active one, whose agent deploys them from scratch (what the old cluster
  reported is dropped). 422 `CLUSTER_NOT_REVOKED` for a cluster still
  running them, `INVALID_MOVE_TARGET` for a `to` that is not another active
  cluster. Moving an empty cluster changes nothing and records nothing.
- Without a fleet CA configured (`FLEET_CA_CERT`), creating a cluster or a
  token answers 422 `FLEET_DISABLED`.
- **Events**: `cluster.created`, `cluster.token_issued`, `cluster.enrolled`,
  `cluster.certificate_renewed`, `cluster.connected`, `cluster.revoked`,
  `cluster.apps_moved`, `cluster.app_applied` (subject `cluster/<id>`).
  Heartbeats and app status reports are observations: they update the
  cluster but record no event; neither does an agent recovering a
  certificate whose answer it lost (gRPC `Recover`: same key, nothing new is
  signed; certificates issued before this existed become recoverable at
  their next renewal).
- **Gateway (agents' side)**: certificates live 30 days and are renewed
  inside the tunnel at two thirds of their life. The calls without a
  certificate (`Enroll`, `Recover`) are rate-limited per client IP (a burst
  of 5, then one every 5 s) and then globally. **A load balancer in front of
  the gateway must pass TCP through (TLS ends at the gateway: it checks the
  client certificate) and preserve the client IP** (no SNAT; e.g. a network
  LB with client IP preservation): otherwise every agent shares one IP and
  one per-IP budget.

### Audit (`odyssey:audit:read`)

The organization's activity trail: one entry per domain event, written by the
`audit` subscriber, append-only and hash-chained per organization (each
entry's `hash` covers its content and the previous entry's `hash`).

| Method | Path | Permission | Success |
|---|---|---|---|
| `GET` | `/api/v1/audit` | read | 200 `{items, page}`, newest first |
| `GET` | `/api/v1/audit/verify` | read | 200 `{valid, entries, broken_at}` |

- **List filters**: `subject` (`project/<id>`), `type` (repeatable),
  `actor_id`, `correlation_id`, `from` / `to` (a `YYYY-MM-DD` date in UTC —
  `to` includes that whole day — or an RFC 3339 instant, `to` exclusive) and
  `search` (summary).
- **Verify** re-hashes the whole chain; `broken_at` is the `seq` of the first
  entry that does not match (`null` when `valid`).

### GitHub webhooks (public: `POST /webhooks/github`)

Not under `/api/v1`: no token and no CORS. GitHub calls it with
`X-GitHub-Delivery`, `X-GitHub-Event` and `X-Hub-Signature-256` (HMAC-SHA256
of the exact body with the App's webhook secret); the signature is the only
proof and is checked first (`401`). Everything understood answers `202
{"status":"accepted"}`, including a repeated delivery id (remembered 7 days) and
events that are ignored (`ping`, `created`, other repositories or branches).
`push` queues the branch's new head once per commit (`trigger: webhook`);
`installation` / `installation_repositories` suspend, refresh or unlink the
installation. A missed webhook is caught by a 2-minute poll (`trigger:
reconcile`). `422 GITHUB_NOT_CONFIGURED` without a GitHub App.

### Upload tickets (public: `PUT /uploads/:ticket`)

Not under `/api/v1`: no token and no CORS. This is where the command that the
MCP tool `deploy_folder` returns uploads the folder
(`… | curl --fail-with-body -H 'Content-Type: application/gzip' -T - <url>`),
so the agent's own token never leaves its MCP client. The ticket in the path
(`odu_` + 43 URL-safe base64 characters; only its SHA-256 is stored) is the
only credential: it names the app, environment, user, MCP client and
configuration, is good for **one** upload within **10 minutes**, and is spent
the moment the request arrives, even if the archive is then refused (ask for
another).

The body is the folder as a raw `.tar.gz` (≤ 100 MiB; the same checks as
deploy from source, §11 Deployments). It starts the same deployment from
source, attributed to the user and the MCP client of the ticket, and answers
`202 {"deployment_id", "status", "next_step"}`; follow it with `get_deployment`
(MCP) or `GET …/deployments/:deployment`. Errors: `404` unknown or malformed
ticket; `409 UPLOAD_TICKET_USED` (`details.deployment_id` when the upload
started one); `422 UPLOAD_TICKET_EXPIRED`; `413` over the limit; `415` for a
Content-Type that is not `application/gzip` / `application/octet-stream`;
`400 SOURCE_INVALID` and the other errors of deploy from source.
Tickets are kept 24 h after they expire so a late upload still hears
"expired".

### Assistant (`odyssey:projects:read`: `GET /api/v1/assistant`)

"Conecta tu asistente" in the console. No secrets, the same for everyone with
`projects:read`. `200 {available, mcp_url, skill: {url, version, name},
clients: [{key, name, install, skill_install, notes}]}`: `available` is
`false` and `clients` empty when the MCP endpoint is not enabled. `mcp_url` is
the MCP audience (`IAMKIT_AUDIENCE`); `clients` follow `ODYSSEY_MCP_CLIENTS`
(keys `claude_code`, `codex`, `vscode`, `antigravity`; unknown keys are
ignored; `antigravity` is offered only when `IAMKIT_MCP_CLIENT_ANTIGRAVITY_ID`
is set, since it cannot register itself and its JSON snippet carries
`oauth.clientId`). `install` registers the MCP server `odicea` (a command, or the JSON
for the client's MCP config); `skill_install` downloads and unpacks the skill
into the client's skills folder.

### Skill download (public: `GET /skills/odicea.tar.gz`)

Not under `/api/v1`: no token and no CORS. The `odicea` skill (what teaches an
assistant to prepare, publish and diagnose apps; `skills/odicea/`) as a
deterministic `application/gzip` archive with one top directory, `odicea/`
(`SKILL.md`, `references/`). Built once at startup from the files compiled
into the binary. `ETag` is the version (a quoted hash of the archive; the same
bytes always give the same version); `If-None-Match` naming it (a list, `*` and
weak `W/"…"` validators included) answers `304`;
`Cache-Control: public, max-age=300`.

### Access sign-in (public: `GET /.well-known/odicea-access-keys`, `GET /access/authorize`, `GET /access/callback`)

Not under `/api/v1`: no token and no CORS. The sign-in of apps protected with an
Odicea login (`team` / `organization`; the whole contract with the gate in
`docs/access.md`). Served only when `ODYSSEY_ACCESS_SIGNING_KEY[_FILE]` and
`IAMKIT_ACCESS_CLIENT_ID/_SECRET` are set; otherwise all three answer `404`
`ACCESS_NOT_CONFIGURED` (and the settings report `access_login_available: false`).

- `GET /.well-known/odicea-access-keys`: the JWKS (`kty OKP`, `crv Ed25519`,
  `use sig`, `alg EdDSA`, `kid` = first 16 characters of
  base64url(sha256(public key))): the signing key, then the previous ones.
- `GET /access/authorize?env&mode&return` (what the gate redirects to): finds
  the environment by id (the organization comes from our own records), requires
  `return` to be the environment's app: same scheme, host and port as its URL
  (an explicit `:443` / `:80` equals none), https unless a development host, and
  `mode` to be the one the cluster was last told (`team|organization`; a public
  app has no sign-in). Every refusal is one generic Spanish page (400) — the
  code is only in the server logs — so an unknown environment and a wrong host
  look the same;
  `302` to IAMKit's sign-in (authorization code + PKCE) and sets the sealed
  cookie `__odicea_flow` (HttpOnly, SameSite=Lax, path `/access`, 10 min). It
  writes nothing to the database.
- `GET /access/callback?code&state`: checks the cookie and `state`, exchanges
  the code, decides (`team`: the user's organization is the app's;
  `organization`: the user belongs to this installation's IAMKit environment)
  and answers `302` to `<scheme>://<app host>/_odicea/callback?ticket=<JWT>&return=<path>`
  (a 60-second EdDSA ticket), or a plain Spanish HTML page: `403` when the
  person may not open the app (or the app's access changed meanwhile), `400`
  (the same generic page as authorize) when the sign-in expired or is not valid.
  The ticket's `aud` is the app URL's `host[:port]`, as the gate computes it.
