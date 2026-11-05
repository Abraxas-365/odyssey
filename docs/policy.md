# Reglas de TI — policy (cut 3a)

IT decides what may happen to the organization's apps **before** it happens.
Two inputs: the app's **tier** and IT's **rules**, written in CEL. The `policy`
domain answers one question: *what does the organization's rule set say about
this action?* Everything that governs an action (release, resource, ai) asks
that question through a port it owns; none of them imports `policy`.

Phase 3a is the verdict and the refusal. Phase 3b turns "needs approval" into
a request that someone from IT decides (`202` instead of `409`), and 3b/3c feed
the vulnerability scan; the seams for both are marked below.

## Tiers

`project.tier` is `sandbox` | `internal` | `production` (default `internal`,
also for apps that existed before 3a). Console names: Sandbox, Interna,
Producción.

- Changed with `PUT /api/v1/projects/:id/tier` `{tier}` (docs/api.md §11), only
  by who holds `odyssey:projects:tier` (the TI role). An app's owner cannot
  lower their own app's bar.
- Not part of `PATCH /projects/:id` and not a create field: a new app is
  always `internal`.
- Event `project.tier_changed` (`{from, to}`), no-op when unchanged.
- An archived app refuses the change (`PROJECT_ARCHIVED`).

## Rules

A rule is `{name, description, expression, effect, approvers, enabled}`.

| field | meaning |
|---|---|
| `expression` | CEL over the input below; must give `bool`. ≤ 2000 characters. |
| `effect` | `deny`, `approval` or `allow` |
| `approvers` | the permission whose holders decide an approval (default `odyssey:approvals:decide`; used from 3b) |
| `description` | the rule as a sentence anyone reads (the console shows it) |
| `enabled` | off rules are stored but never evaluated |

Managing them needs `odyssey:policy:manage` (`GET/POST /api/v1/policy/rules`,
`PUT/DELETE /api/v1/policy/rules/:id`, `POST /api/v1/policy/evaluate`).
`PUT` replaces the whole rule and bumps its `version`. Events
`policy.rule_created|updated|deleted`; all carry facts and IDs only.

Saving compiles and type-checks the expression: a rule that does not compile,
reads an unknown field or does not give `bool` is **422 `POLICY_INVALID`**
(`field: expression`, message: the compiler's, in the rule author's terms).
A bad rule never gets stored.

### Built-in rules

Every organization has four. They can be switched off, **not edited or
deleted** (422 `POLICY_BUILTIN`): to change one, switch it off and write
another. Each has a stable key (`builtin`), unique per organization.

| key | name | when | effect |
|---|---|---|---|
| `production_deploy_approval` | Publicar en producción requiere aprobación de TI | `action == 'deploy' && project.tier == 'production'` | approval |
| `public_access_approval` | Hacer pública una app requiere aprobación de TI | `action == 'set_access' && access.mode == 'public'` | approval |
| `production_critical_vulnerabilities` | Producción no publica con vulnerabilidades críticas | `action == 'deploy' && project.tier == 'production' && scan.known && scan.critical > 0` | deny |
| `sandbox_not_public` | Sandbox no puede ser pública | `action == 'set_access' && project.tier == 'sandbox' && access.mode == 'public'` | deny |

**Seeding is lazy and idempotent.** There is no migration that knows the
organizations (they live in IAMKit), so the first list/evaluate of an
organization inserts the rules it lacks (`INSERT … ON CONFLICT (organization,
builtin) DO NOTHING`; concurrent first requests converge). The service
remembers which organizations are seeded for the process's life; a deleted
row (it cannot be deleted through the API) comes back after a restart. An
organization that only ever *evaluates* is seeded too, because evaluation
reads the list.

## The input

What a rule can read (`docs/api.md` `PolicyInput`). What the action does not
have is the zero value (empty text, `false`, `0`, empty list): a rule never
fails for a missing field.

| variable | fields |
|---|---|
| `action` | `deploy` · `set_access` · `connect_resource` · `delete_resource` · `enable_ai` · `rollback` (`set_secret` is accepted but not enforced in 3a) |
| `project` | `tier`, `slug` |
| `env` | `name` |
| `access` | `mode` (`team` · `organization` · `public`; only for `set_access`) |
| `actor` | `id`, `permissions` (list) — those of the **authenticated principal** (deferred work: see "Who asks, when the work is deferred") |
| `resource` | `kind` (`postgres` · `redis` · `files`, for connect/delete) |
| `scan` | `known`, `critical`, `high` |

`actor.permissions` is what the caller's token holds: for REST the token's
claims; for MCP the **connection's** permissions (the ones the user consented
to for that assistant, not everything the user could do). So
`!('odyssey:access:public' in actor.permissions)` means what it says on both.

`scan.known` is `false` until a scan exists. Deploy is judged **at its start,
before the build**, so in 3a `scan.known` is always `false` and
`production_critical_vulnerabilities` cannot match yet. Rules about scans
should say `scan.known && …` (an unknown scan is not a clean one). 3b/3c move
the deploy check into a workflow `authorize` step after the scan, with the
real numbers.

## Evaluation

All **enabled** rules are evaluated over the input, then:

1. any matching `deny` → **deny** (the matching deny rules);
2. else any matching `approval` → **approval** (those rules; `approvers` is
   the union of their approver permissions);
3. else **allow** (any matching `allow`, or no match at all).

`allow` rules exist to be explicit and to show up in a dry run; they never
override a `deny` or an `approval`. No rules, or none matching: **allow**.

`Decision{effect, rules[{id,name}], approvers[]}`. `POST /policy/evaluate` also
returns `results[]`: every rule with `enabled`, `matched` and, if it could not
be evaluated, `error`. It is a dry run: nothing is recorded, nothing is stopped,
it uses the rules saved today and the permissions given in `actor` (empty
unless said otherwise).

### Failure semantics: fail closed

A rule that cannot be **evaluated** must not open the door. It counts at **its
own strength**, so a rule that was meant to stop something still stops it:

| the rule's effect | when it cannot be evaluated | result |
|---|---|---|
| `deny` | runtime error, cost over the limit, no longer compiles | counts as **deny** |
| `approval` | same | counts as **approval** |
| `allow` | same | **ignored** (an allow never opens anything, so there is nothing to fail closed on) |

"Cannot be evaluated" is: a runtime error (type, division by zero…), a cost
over the limit (10 000 CEL steps) or 100 ms, or a stored expression that no
longer compiles (the language changed). The reason is in `results[].error` of
the dry run. Such a rule is never skipped in favor of an allow.

| failure | result |
|---|---|
| the rule store itself fails | the **action fails** (500 / 502), it is not allowed |

The error is logged (`policy rule could not be evaluated`) and shown in the
console's "Probar". Compiled programs are cached by `(rule id, version)`; a
saved edit bumps the version, so a stale program is never used.

### Dry run, and trying a rule before saving it

`POST /policy/evaluate` takes the input fields (`action`, `project`, `env`,
`access`, `actor`, `resource`, `scan`) and, optionally, a **`rule`**: a draft
(`name`, `expression`, `effect`, `approvers`; `replaces` is the id of a saved
rule it stands in for). The candidate is evaluated **with the saved rules, as if
it were enabled, without saving it**: the console's "Probar" in the create/edit
dialog runs the expression being typed. A candidate that does not compile is the
same 422 `POLICY_INVALID` as saving it. `results[]` lists the candidate too (its
`id` is a throwaway one). Nothing is stored and no event is published.

## What the rules do not govern (yet, and why)

On purpose; for most of it TI can still add a rule:

- **Rollback** is not governed by a *built-in*. It is an enforced action
  (`rollback`), so TI can write a rule for it (e.g. "only TI rolls back"), but
  none comes by default: going back to a version that was already approved is
  the undo of a bad deploy, and slowing it down is the wrong default.
- **`set_secret`** is a valid action name but **nothing asks about it**:
  setting an environment secret/variable (`SetEnv`) is not judged. Secret values
  never reach the rules' input, so a rule would have nothing to look at;
  revisit with the secrets work.
- **Creating a resource without connecting it** is not judged: an unconnected
  resource touches no app. Creating one *with* a connection to an app, and
  connecting an existing one, is `connect_resource` and is judged **before
  anything is created or applied**.
- **AI key rotation** is not judged: `enable_ai` judges turning the AI on, and
  rotating a key changes no exposure.
- **No-ops are not judged.** Asking for the access mode the app already has, or
  enabling an AI that is already on, answers as before without asking the
  rules: a rule that matches the target value does not refuse a request that
  changes nothing.
- **Scan-based rules are inactive.** The built-in
  `production_critical_vulnerabilities` and any `scan.*` rule **cannot match
  until the scan exists (cut 3c)**: today the deploy is judged at its start
  with `scan.known == false`. Do not count on them to stop a vulnerable image;
  they are listed so TI sees what is coming.

## Who asks, when the work is deferred

Some deploys run later, in a worker or from an upload, with no request of their
own. They are judged as the **actor that asked**, not as nobody:

| path | actor the rules see |
|---|---|
| REST / MCP request | the token's user and permissions (as above) |
| `deploy_folder` (MCP) → upload ticket → `PUT /uploads/:ticket` | the **person who asked for the ticket**, with the permissions their token held **when they asked** (stored on the ticket, `release.upload_tickets.permissions`; the upload request itself has no token) |
| "Publicar ahora" / connecting a repository (git import workflow) | the user who pressed it, with their token's permissions at that moment (kept in the run's input) |
| a **push to GitHub** (webhook) or the poll | a **system actor** `github:<login of the sender>` (`github` alone for the poll): `actor.id` is that name and **`actor.permissions` is empty** |

Permissions are a **snapshot at request time**, not re-read when the work runs
(a ticket lives 10 minutes, an import runs within minutes): they are only used
to evaluate rules, never to grant anything. **Rules see no permissions for a
push**: `!('odyssey:projects:tier' in actor.permissions)` matches every
push-triggered deploy. To treat pushes apart, match on
`actor.id.startsWith('github:')`.

A refusal there is **definitive**: the deploy step does not retry it (an
approval's 409 is a `Conflict`, which the workflow otherwise retries). The push
fails at once with `DEPLOY_REFUSED` and the rule's message as its detail
(«La regla de TI «…» no permite esto.»), shown in the console and in the
assistant's `publish_now` / push status text, and the imported source is
dropped. 3b turns the approval case into a request that waits.

## Enforcement

Each governed domain owns a small `Policy` port and one `authorize` method
(the seam). A provider-side adapter under `policy/adapters/policy{release,
resource,ai}` implements the port over `policy.Ask`; only `bootstrap` wires
it. The actor comes from the context (`internal/actor`), set from the
authenticated principal by `httpx` (REST) and `mcpx` (MCP).

| action | where | when |
|---|---|---|
| `deploy` | release: publish an image, a folder, a git commit | at the start, before anything is stored, uploaded or built |
| `rollback` | release: restore an older version | before it goes online |
| `set_access` | release: change who can open the app | before the setting is stored |
| `connect_resource` | release: connect an existing resource to an app | before connecting |
| `delete_resource` | resource: delete | before deleting |
| `enable_ai` | ai: turn the app's AI on | before turning on |

Refusals are errors, with the rule's own name in the message:

| effect | status | `code` | message |
|---|---|---|---|
| deny | 403 `AUTHORIZATION` | `POLICY_DENIED` | La regla de TI «…» no permite esto. |
| approval | 409 `CONFLICT` | `APPROVAL_REQUIRED` | Esta acción necesita aprobación de TI (regla «…»). |

`details.rules` lists the rule ids (`details.approvers` for an approval). Over
MCP both are tool errors whose `next_step` tells the assistant to show IT's
message as it is and **not retry or look for a workaround**.

**3b seam.** `authorize` in `releasesvc`/`resourcesvc`/`aisvc` is the only place
the approval branch lives. 3b replaces it (and only it) with "create the
approval request, answer 202" and re-runs the action when someone with the
`approvers` permission decides.

## Architecture rules

Tested in `internal/bootstrap` (`policy_architecture_test.go`):

- only `policy/adapters/policycel` imports cel-go: the domain and service know
  the `Engine` port, not the language;
- release, resource and ai never import `policy` (they use their own port);
- `policy/adapters/*` and `policymodule` are imported only by `bootstrap`.
