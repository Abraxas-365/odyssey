# MCP tools

The MCP endpoint lets a coding assistant create, publish and inspect apps on Odicea on behalf of a signed-in person. This page covers how the endpoint authenticates, the rules every tool follows and each tool with its arguments, required permissions and result.

## Connecting

| | |
| :--- | :--- |
| Endpoint | `POST /mcp` on the API. `GET` and `DELETE` answer `405` with `Allow: POST`: the server is stateless and has no stream or session. |
| Server name | `odicea` |
| Authentication | OAuth 2.0 with IAMKit as the authorization server. Odicea is an OAuth protected resource (RFC 9728) and publishes its metadata for assistants to discover IAMKit. |
| Token audience | The resource claim of the token must be `IAMKIT_AUDIENCE`, which is the endpoint's own URL: `https://<odicea>/mcp`. The API refuses to start with MCP on if that URL's path is not `/mcp`. |
| Token checks | Audience, environment, application and resource must match this deployment, and IAMKit's introspection must say the token is still live. Every request is checked; an active answer is cached for 15 seconds, so a revoked connection stops working within that time. |
| Failures | `401` with a `WWW-Authenticate` challenge that starts the assistant's OAuth flow; `503` with `Retry-After` when IAMKit cannot be asked. These are not the REST error body. |
| Request size | At most 1 MiB per request body. |
| Enabled when | `IAMKIT_INTROSPECTION_CLIENT_ID` and its secret are set. Otherwise `/mcp` is not mounted. See [Configuration](configuration.md#mcp-assistants). |

On connection the server sends instructions: the publishing flow, the rule that apps listen on `$PORT` and keep secrets out of code, that the upload link is single-use, and that every error carries a `next_step`.

### What an assistant may do

The permissions of a connection are the scopes the person consented to, intersected with the permissions they hold now. Every tool declares the permissions it needs, and **all** of them are required:

- The server lists only the tools the connection may use.
- Each call checks the permissions again, so a stale list never widens access. A missing permission answers `FORBIDDEN` (`insufficient permissions`).

`whoami` shows the effective permissions. The permission names are in [Permissions](permissions.md). Console link: the person manages their connections under "Conecta tu asistente".

### Conventions

- Every tool takes `app` as the app's slug or its id (`list_apps` shows them). Tools act on the app's `production` environment, except the resource and AI tools, which take an optional `environment` that defaults to `production`.
- Arguments are validated against the tool's JSON schema before the tool runs.
- Lists are paginated with `limit` and `offset`; the result carries `next_offset` while there is more. Defaults and maximums are in [Limits](limits.md#mcp-tools).
- Results carry a `next_step` sentence (in Spanish) that tells the assistant what to do next.
- Each tool is annotated for clients. Read tools are marked read-only and idempotent. Write tools are marked non-destructive, except `rollback`, which is marked destructive. Of the write tools, `set_services`, `request_secret` and `enable_ai` are also marked idempotent.

### Errors

A failed call is a tool result with `isError` set whose text is JSON:

```json
{
  "error": {
    "code": "NOT_FOUND",
    "message": "app not found",
    "field": "app",
    "next_step": "Usa list_apps para ver los slugs de tu organización.",
    "request_id": "…"
  }
}
```

`field`, `next_step` and `request_id` appear when there is one. Internal failures show a generic message and the `request_id` to give to IT. The codes are the same ones the REST API returns; see [Errors](errors.md).

### Held for approval (`pending_approval`)

When IT's rules require approval for an action, the tool does **not** fail and nothing has happened yet. These tools return `status: "pending_approval"` and a `request_id`:

`set_access`, `set_services`, `create_resource`, `create_database` and `enable_ai`.

The assistant must tell the person it is pending, not retry and not look for another way. `get_request` (or `list_requests`) follows it. A request expires if nobody decides within 7 days. See [Approvals](../guides/it/approvals.md).

Publishing tools behave differently: `deploy_folder`, `deploy_image` and `rollback` return the new version as usual, and if the rules hold it, that version has status `waiting_approval` (before building). `get_deployment` says so in its `next_step`. A newer publication of the app replaces a waiting one.

If a rule denies the action the tool fails with `POLICY_DENIED`. A legacy `APPROVAL_REQUIRED` refusal (409) also exists; its `next_step` tells the assistant to show the message to the person.

### Confirmation

Two actions are hard to undo, so the person must say yes before they happen:

- `rollback` of an app whose tier is `production`.
- `set_access` with `mode: "public"`.

The order is:

1. If the call has `confirm: true`, it goes ahead (the assistant states the person agreed).
2. Otherwise, if the client supports MCP elicitation, the person is asked in the client. Accepting goes ahead; declining or cancelling fails with `CONFIRMATION_DECLINED` (409).
3. Otherwise the call fails with `CONFIRMATION_REQUIRED` (409). Its `next_step` tells the assistant to ask the person and repeat the call with `confirm: true`.

Because the endpoint is stateless, most clients end at step 3. Confirmation comes before IT's rules: a confirmed action can still be held for approval.

### Secrets never travel through the assistant

`set_env` is for plain configuration. It rejects names containing `PASSWORD`, `PASSWD`, `SECRET`, `TOKEN`, `PRIVATE_KEY`, `API_KEY`, `APIKEY` or `CREDENTIAL` and points to `request_secret`. `request_secret` only declares the secret and returns a console link; a person types the value in the console, where it is sealed in the browser for the cluster. No tool returns or accepts a secret's value, and resource and AI tools never show passwords or keys.

## Apps

| Tool | Requires | Arguments | Returns |
| :--- | :--- | :--- | :--- |
| `whoami` | `odyssey:projects:read` | none | `organization_id`, `user_id`, `client`, `permissions`, `console_url`, `github_enabled`. |
| `list_apps` | `odyssey:projects:read` | `status` (`active`, default, or `archived`), `search`, `limit`, `offset` | `items` (`id`, `slug`, `name`, `description`, `status`, `console_url`, `created_at`, `needs_attention`), `total`, `limit`, `offset`, `next_offset`. |
| `create_app` | `odyssey:projects:write` | **`name`**, `description`, `slug` (derived from the name when absent) | `id`, `slug`, `name`, `console_url`, `next_step`. A repeated slug fails with a conflict. |
| `get_app` | `odyssey:projects:read`, `odyssey:deployments:read` | **`app`** | `state` (`online`, `publishing`, `failed`, `not_published`), `url`, `needs_attention`, `current`, `active`, `last_deployment`, `access`, `uses`, `services`, `repository`. |
| `get_attention` | `odyssey:projects:read` | none | `items` (`kind`, `scope`, `severity`, `sentence`, `console_url`, `app_id`, `request_id`, `at`), `all_in_order`, `unavailable`, `truncated`. |
| `list_services` | `odyssey:projects:read` | **`app`** | `items` (`name`, `kind`, `root`, `route`, `strip_prefix`), `single`, `next_step`. |
| `set_services` | `odyssey:projects:write`, `odyssey:projects:read` | **`app`**, **`items`**: the complete list of 1 to 8 parts, each with **`name`**, `kind` (`web`, default, or `worker`), `root`, `route`, `strip_prefix` | `items` as they are (or stay, if held), `next_step`; `status` and `request_id` when held. |
| `detect_services` | `odyssey:projects:write` | Either `paths` (file paths relative to the published folder) or `repository` with optional `branch`; optional `root` | `items` (`name`, `kind`, `root`, `route`, `strip_prefix`, `evidence`), `notes`, `truncated`, `next_step`. Proposes parts; saves nothing. |

Notes:

- `needs_attention` in `list_apps` is true when the last publication failed or a critical vulnerability is online. It is filled in for every app only for people with `odyssey:inventory:read`; others see it only on the apps they created, and the field is absent elsewhere (absent does not mean fine).
- `get_app` reports in `uses` what the app relies on: `access`, `identity` (`verifiable` is true when the app receives the signed `X-Odicea-Assertion` header), `resources`, `secrets`, `variables`, `ai`, `egress`, `unavailable` and `attention`. Each `attention` entry has a `kind`, a `who` (`owner`, `it` or `wait`) and a `message`.
- `get_attention` lists what the person must do (their apps, their requests) and, for IT, what IT watches (approvals to decide, vulnerable apps, failed publications, disconnected clusters). An empty list with an empty `unavailable` means everything is in order.
- A service name uses lowercase letters and digits, starts with a letter and has up to 20 characters. A cluster whose agent is too old rejects `set_services` with `SERVICES_UNAVAILABLE`; the app stays a single part.

## Publishing

| Tool | Requires | Arguments | Returns |
| :--- | :--- | :--- | :--- |
| `list_versions` | `odyssey:deployments:read`, `odyssey:projects:read` | **`app`**, `limit`, `offset` | `items` (newest first; each version has `deployment_id`, `status`, `step`, `reason`, `image`, `message`, repository, `branch`, `commit`, `title`, `author`, `created_by`, `requested_via`, `error`, `changed`, `created_at`, `finished_at`, `is_current`), `total`, `limit`, `offset`, `next_offset`. |
| `deploy_folder` | `odyssey:deployments:create`, `odyssey:projects:read` | **`app`**, `message` (what changed, up to 200 characters), `name`, `rebuild`, plus the run options below | `upload_url`, `expires_at`, `command`, `services` (only for apps with several parts), `next_step`. |
| `deploy_image` | `odyssey:deployments:create`, `odyssey:projects:read` | **`app`**, **`image`** (pinned by digest: `registry/repository@sha256:<64 hex>`), `message`, plus the run options below | The new version (`deployment_id`, `status`, …) and `next_step`. |
| `get_deployment` | `odyssey:deployments:read`, `odyssey:projects:read` | **`app`**, `deployment_id`, `wait_seconds` (0 to 45) | `status`, `step`, `steps`, `reason`, `done`, `url`, `image`, `repository`, `commit`, `failure`, `changed`, `rebuild`, `builds`, `created_at`, `finished_at`, `console_url`, `next_step`. |
| `get_build_logs` | `odyssey:deployments:read`, `odyssey:projects:read` | **`app`**, `deployment_id`, `after`, `limit`, `service` | `build_id`, `build_status`, `service`, `log`, `next`, `finished`, `truncated`, `failure`. |
| `rollback` | `odyssey:deployments:rollback`, `odyssey:projects:read` | **`app`**, `deployment_id`, `confirm` | The new version and `next_step`. Asks for [confirmation](#confirmation) when the app is in production. |

The run options of `deploy_folder` and `deploy_image` are `port` (default 8080, also passed to the app as `$PORT`), `health_path` (default `/`), `replicas` (1 to 10, default 1) and `env` (plain variables only).

Notes:

- `deploy_folder` does **not** take files. It issues a single-use upload link valid for 10 minutes and a shell `command`. **The assistant runs the command itself**, in the app's folder: it tars the folder (honouring `.gitignore`, leaving out `.git`, `node_modules` and `.env*`), compresses it and uploads it. The link must not be shown to the person or stored. Size limits are in [Limits](limits.md#uploads-and-sources). For an app with several parts the command runs in the folder that contains all of them.
- Only the parts whose code changed are built; `rebuild: true` builds all of them again.
- `get_deployment` without `deployment_id` shows the publication in progress or, if none, the one that is online. With `wait_seconds` it waits for the publication to finish before answering; repeat it while `done` is false. A value outside 0 to 45 is rejected.
- `status` is one of `building`, `waiting_approval`, `pending`, `applying`, `verifying`, `ready`, `failed` or `cancelled`. `reason` is `deploy`, `rollback`, `auto_rollback` or `config` (the same version published again after a change of access, variables or resources). `steps` use the names `build`, `apply`, `wait_ready` and `promote`.
- `get_build_logs` returns the last 200 lines by default and at most 1000. Pass the `next` of the previous answer as `after` to keep reading. It fails when the publication has no build (an image, a configuration change or a rollback).
- `rollback` without `deployment_id` goes back to the newest ready version that is not the one serving, looking through up to 10 pages of 100 versions. It fails with a business error if there is none, and `list_versions` shows candidates.

## Access, variables and secrets

| Tool | Requires | Arguments | Returns |
| :--- | :--- | :--- | :--- |
| `set_access` | `odyssey:projects:write`, `odyssey:projects:read` | **`app`**, **`mode`** (`team`, `organization` or `public`), `confirm` | `access` (`mode`, `pending`), `version` when the change started a publication, `next_step`; `status` and `request_id` when held. |
| `set_env` | `odyssey:projects:write`, `odyssey:projects:read` | **`app`**, `set` (name to value), `unset` (names), `services` | `variables`, `service_variables`, `secrets` (names only), `version`, `warnings`, `next_step`. |
| `request_secret` | `odyssey:projects:write`, `odyssey:projects:read` | **`app`**, **`name`**, `note`, `services` | `name`, `status`, `console_url`, `services`, `next_step`. Never a value. |

Notes:

- `mode: "public"` needs the extra permission `odyssey:access:public`, which the tool does not list: without it the call fails with `FORBIDDEN`. It also needs [confirmation](#confirmation). `team` is the default for an app, `organization` opens it to the whole company.
- When the app is already online, `set_access` and `set_env` publish the same version again with the new setting (`reason` `config`) and return it in `version`. Otherwise the setting is stored and applies to the next publication.
- `request_secret` status is `waiting_for_value` (a person must load it at `console_url`), `loaded`, `reload` (the cluster's key changed: load it again), `unavailable` (the cluster cannot store this kind of secret: ask IT) or `external` (it comes from the company's secret store). Once a person loads the value the app is published again by itself.
- `services` limits which parts of a multi-part app receive the setting. Omitting it keeps the current scope; a cluster whose agent is too old fails with `SERVICE_SETTINGS_UNAVAILABLE`.

## GitHub

| Tool | Requires | Arguments | Returns |
| :--- | :--- | :--- | :--- |
| `list_repositories` | `odyssey:projects:write` | `search` (text in `owner/name`), `limit`, `offset` | `items` (`id`, `full_name`, `default_branch`, `private`), `total`, `limit`, `offset`, `next_offset`. |
| `connect_repository` | `odyssey:projects:write`, `odyssey:deployments:create` | **`app`**, **`repository`** (`owner/name` or numeric id), `branch`, `root`, plus the run options of `deploy_folder` | `repository`, `branch`, `root`, `deployment_id` when the first publication started, `next_step`. |
| `publish_now` | `odyssey:deployments:create`, `odyssey:projects:read` | **`app`**, `rebuild` | `repository`, `branch`, `commit`, `deployment_id`, `next_step`. |

Notes:

- `list_repositories` shows the repositories of the accounts where Odicea's GitHub app is installed. With no account connected, `next_step` carries the console link to install it, which needs a browser.
- `connect_repository` makes every push to the branch publish the app and publishes the latest commit right away. It waits up to 10 seconds for the publication to start. For a monorepo, propose the parts with `detect_services`, save them with `set_services` and connect without `root`.
- `branch` defaults to the repository's main branch and `root` is the app's folder inside the repository, empty for its root.
- Without a GitHub installation the tools are still listed but cannot reach any repository; `whoami` reports `github_enabled`.

## Databases, volumes and AI

| Tool | Requires | Arguments | Returns |
| :--- | :--- | :--- | :--- |
| `list_catalog` | `odyssey:projects:read` | **`app`**, `environment` | `tier`, `recipes` (per kind: `recipe`, `params` with limits and pre-approved range, `variables`, `approval` of `instant`, `needs_approval` or `denied`, `rules`), `unavailable` (kinds that cannot be asked for, with the reason), `can_create`, `console_url`, `next_step`. |
| `create_resource` | `odyssey:projects:read`, `odyssey:projects:write` | **`app`**, `environment`, `kind` (`postgres`, default, `redis` or `files`), `name` (default `db`), `recipe`, `params` (for example `{"storage_gi": 20}`), `connect` (default true), `prefix`, `services` | `resource`, `config_deployment_id`, `console_url`, `next_step`; `status` and `request_id` when held. |
| `create_database` | `odyssey:projects:read`, `odyssey:projects:write` | **`app`**, `environment`, `kind`, `name`, `storage_gi`, `prefix`, `services` | Same as `create_resource`. The short form that uses the kind's default recipe. |
| `list_resources` | `odyssey:projects:read` | **`app`**, `environment` | `resources` (`name`, `kind`, `storage_gi`, `existing`, `status`, `message`, `connected`, `variables`, `services`), `available_kinds`, `next_step`. |
| `enable_ai` | `odyssey:projects:read`, `odyssey:projects:write` | **`app`**, `environment`, `services` | `status` (`enabling`, `enabled`, `off` or `pending_approval`), `request_id`, `base_url`, `variables`, `console_url`, `next_step`. |

Notes:

- Parameters inside a recipe's pre-approved range create the resource at once. Outside it, or when a rule says so, the request is held (`pending_approval`) and nothing is created.
- A connected resource gives the app its variables (for example `DATABASE_URL`) in a new version that starts by itself. `prefix` (uppercase, for example `ANALYTICS`) goes before the variable names so an app can use several. Values are never shown.
- Resource statuses are `preparing`, `ready`, `failed` and `deleting`. `existing` marks a resource the company already had: Odicea only connects it, and deleting it only disconnects it.
- A `files` volume of an app with several parts must name exactly one part in `services`.
- `enable_ai` makes a key for the app, hands it sealed to the cluster and publishes a new version that receives `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL`. The key is never shown. Calling it when AI is already on changes nothing.
- There is no MCP tool to delete a resource; that is console and REST only.

## Approval requests

| Tool | Requires | Arguments | Returns |
| :--- | :--- | :--- | :--- |
| `get_request` | `odyssey:projects:read` | **`request_id`** | `request_id`, `action`, `summary`, `status`, `reason`, `decision_note`, `decided_at`, `expires_at`, `created_at`, `done`, `execution_error`, `console_url`, `next_step`. |
| `list_requests` | `odyssey:projects:read` | `status`, `limit`, `offset` | `items` (same fields as `get_request`), `total`, `limit`, `offset`, `next_offset`, `console_url`, `next_step`. |

- `action` is `deploy`, `rollback`, `set_access`, `connect_resource`, `delete_resource` or `enable_ai`. `status` is `pending`, `approved`, `rejected`, `expired` or `cancelled`; `list_requests` accepts any of them to filter.
- `list_requests` lists only the person's own requests, newest first.
- These tools are read-only: an assistant never decides a request. People decide in the console under "Bandeja".
- If a request was approved but the action could not run, `execution_error` says why and the assistant may ask again.

## See also

- [Connect an assistant](../guides/connect-an-assistant.md)
- [Approvals](../guides/it/approvals.md)
- [Permissions](permissions.md)
- [Limits](limits.md)
- [Errors](errors.md)
- [Security model](../explanation/security-model.md)
