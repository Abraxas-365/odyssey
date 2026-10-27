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
  dashboard's sign-in settings (`iam_url`, `client_id`, `environment_id`).

| Operation | Success | Body |
|---|---|---|
| `GET /things` | 200 | `{items, page}` |
| `GET /things/:id` | 200 | resource |
| `POST /things` | **201** + `Location` | full resource |
| `PATCH /things/:id` | **200** | full resource after the change |
| `DELETE /things/:id` | 204 | none |
| `POST /things/:id/<verb>` | 200, or 201 if it creates a resource | result |

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

- Request body limit: 1 MiB.
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
