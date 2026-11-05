# Reglas de TI — policy (cuts 3a, 3b and 3c)

IT decides what may happen to the organization's apps **before** it happens.
Two inputs: the app's **tier** and IT's **rules**, written in CEL. The `policy`
domain answers one question: *what does the organization's rule set say about
this action?* Everything that governs an action (release, resource, ai) asks
that question through a port it owns; none of them imports `policy`.

Phase 3a is the verdict and the refusal. Phase 3b (`docs/approvals.md`) turns
"needs approval" into a request that someone from IT decides (`202` instead of
`409`); 3c (`docs/scan.md`) feeds the vulnerability scan at the deployment's
`authorize_scan` step.

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

Every organization has five. They can be switched off (and on), **not edited or
deleted** (422 `POLICY_BUILTIN`): to change one, switch it off and write
another. Each has a stable key (`builtin`), unique per organization. Four are
born **on**; `production_requires_scan` is born **off** (see below).

| key | name | when | effect | born |
|---|---|---|---|---|
| `production_deploy_approval` | Publicar en producción requiere aprobación de TI | `action == 'deploy' && project.tier == 'production'` | approval | on |
| `public_access_approval` | Hacer pública una app requiere aprobación de TI | `action == 'set_access' && access.mode == 'public'` | approval | on |
| `production_critical_vulnerabilities` | Producción no publica con vulnerabilidades críticas | `action == 'deploy' && project.tier == 'production' && scan.known && scan.critical > 0` | deny | on |
| `sandbox_not_public` | Sandbox no puede ser pública | `action == 'set_access' && project.tier == 'sandbox' && access.mode == 'public'` | deny | on |
| `production_requires_scan` | Producción exige revisión de vulnerabilidades | `action == 'deploy' && project.tier == 'production' && scan.checked && !scan.known` | approval | **off** |

**`production_critical_vulnerabilities` alone can be bypassed, and
`production_requires_scan` is what closes it.** Whoever builds the image
controls what is in the build context: a Dockerfile (or the files it copies)
can make the scan fail on purpose (an unreadable layer, a huge or malformed
filesystem, anything that makes Trivy give up or time out). A scan that fails
is `scan.known == false`, and `scan.known && scan.critical > 0` does not match
it, so the image goes live **unreviewed**. We cannot fix that in the scanner
(a failed scan is indistinguishable from a deliberately broken one), so it is a
policy decision, and the safe production setup is: **enable
`production_requires_scan`**. Then a production deployment whose scan failed,
never came or cannot be done (cluster without the capability) is held for IT's
approval at `authorize_scan` instead of going live; with it on, the image is
either reviewed by the scanner or by a person. It is off by default because an
organization that has not decided how it handles unscannable images would have
every production deploy of a cluster without the scanner wait for approval the
day it is upgraded (`docs/scan.md`).

The rule only judges at **`authorize_scan`** (after the build), never at
`authorize` (before it): `scan.checked` is `false` at the first and `true` at
the second (see "The input"), so enabling it does not ask for a second approval
of every production deployment before it even builds. `deploy_image` and
config-only deployments are not scanned and are not judged at `authorize_scan`,
so this rule does not apply to them either (an image published as it was has
no build to scan; govern it with `production_deploy_approval`).

An organization that existed before a built-in was added gets it on its next
read, in its default state: an existing organization receives
`production_requires_scan` **disabled**; nothing changes for it until IT
switches it on.

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
| `scan` | `known`, `checked`, `critical`, `high` |

`actor.permissions` is what the caller's token holds: for REST the token's
claims; for MCP the **connection's** permissions (the ones the user consented
to for that assistant, not everything the user could do). So
`!('odyssey:access:public' in actor.permissions)` means what it says on both.

`scan.known` is `true` only when the image's vulnerability scan exists and was
done (`critical`/`high` mean something only then). `scan.checked` says the scan
has been *looked at*, whatever came of it. Deploy is judged twice (the
workflow's steps, same verdicts and the same approval machinery):

1. `authorize`, **before the build**: `scan.checked` and `scan.known` are
   `false` (nothing exists yet), so a scan rule cannot match here.
2. `authorize_scan` (3c), **right after the build**, with the real numbers of
   the build's scan (`docs/scan.md`) and `scan.checked == true`. It polls up to
   10 minutes (`release.ScanWait`) for the scan; if it does not arrive, could
   not be done or the cluster does not scan, the deployment goes on judged with
   `scan.checked == true` and `scan.known == false`.

| situation | `checked` | `known` |
|---|---|---|
| first judgment (`authorize`) | false | false |
| after the build, scan done | true | true |
| after the build, scan failed / timed out / cluster without scanner | true | false |

`known` implies `checked`. That is the difference a rule that **requires** a
scan needs: `!scan.known` alone would also match the first judgment (and ask
for approval before the build); `scan.checked && !scan.known` matches only "we
looked and there is nothing usable". A dry run (`POST /policy/evaluate`) takes
the same `scan.checked`; `scan: {known: true}` counts as checked.

An image published as it was (`deploy_image`) and a config-only deployment are
never scanned: only step 1 judges them. Rules about the numbers should say
`scan.known && …` (an unknown scan is not a clean one: see the warning on
`production_critical_vulnerabilities` above). An approval that step 1
already asked is not asked again at step 2; only rules that did not ask before
do.

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
  the undo of a bad deploy, and slowing it down is the wrong default. If a TI
  rule does hold a rollback in production, it becomes a `waiting_approval`
  deployment that **holds the environment's slot**: a newer deployment or
  rollback supersedes it (cancelling its request) and the serving version stays
  as it is meanwhile. Rollbacks stay judged by the rules, not by code: TI who do
  not want them held switch the rule off (`docs/approvals.md` "Deployments").
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
- **Scan-based rules judge the build's scan.** The built-in
  `production_critical_vulnerabilities` denies a production deploy whose scan
  found critical vulnerabilities, at `authorize_scan` (after the build). It
  cannot stop what was never scanned: `deploy_image`, a cluster without the
  `odicea.scan` capability, a scan that failed or timed out all read as
  `scan.known == false`, which that rule does not match (an unscanned image
  is allowed, and shown as "sin escaneo" in the inventory and the app page).
  **That includes a scan that failed on purpose**: a builder controls the image
  and can make the scan fail, so this rule alone is bypassable. To require a
  scan, enable the built-in `production_requires_scan` (off by default; see
  "Built-in rules"), or write your own `scan.checked && !scan.known` rule —
  not plain `!scan.known`, which also matches the first judgment, before the
  build.

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

A refusal there is **definitive**: the deploy step does not retry it. The push
fails at once with `DEPLOY_REFUSED` and the rule's message as its detail
(«La regla de TI «…» no permite esto.»), shown in the console and in the
assistant's `publish_now` / push status text, and the imported source is
dropped. A rule that asks for **approval** is not a refusal: the deployment
the push creates is a system actor's, and it waits for IT at its own
`authorize` step (`waiting_approval`) instead of failing.

## Enforcement

Each governed domain owns a small `Policy` port and one `authorize` method
(the seam). A provider-side adapter under `policy/adapters/policy{release,
resource,ai}` implements the port over `policy.Ask`; only `bootstrap` wires
it. The actor comes from the context (`internal/actor`), set from the
authenticated principal by `httpx` (REST) and `mcpx` (MCP).

| action | where | when |
|---|---|---|
| `deploy` | release: publish an image, a folder, a git commit | the deployment's workflow, **first step** (`authorize`), before anything is built; a cheap deny-only pre-check also runs when it is asked for |
| `rollback` | release: restore an older version | same: the workflow's `authorize` step (and the deny-only pre-check) |
| `set_access` | release: change who can open the app | before the setting is stored |
| `connect_resource` | release: connect an existing resource to an app | before connecting |
| `delete_resource` | resource: delete | before deleting |
| `enable_ai` | ai: turn the app's AI on | before turning on |

A **deny** is an error, with the rule's own name in the message; an
**approval** is not an error:

| effect | answer | `code` | message |
|---|---|---|---|
| deny | 403 `AUTHORIZATION` | `POLICY_DENIED` | La regla de TI «…» no permite esto. |
| approval | **202** `{status:"pending_approval", request_id, message}` (a deployment: accepted, then `waiting_approval`) | — | Le pedimos a TI que lo apruebe. |

`details.rules` lists the rule ids of a denial. Over MCP a denial is a tool
error whose `next_step` tells the assistant to show IT's message as it is and
**not retry or look for a workaround**; an approval is a `pending_approval`
result with `request_id` and a `next_step` (wait for IT; `get_request` follows it).

**3b seam.** `authorize` in `releasesvc`/`resourcesvc`/`aisvc` is the only place
the approval branch lives: an `approval` verdict is turned into a `gate.Hold`
and handed to the domain's own `Approvals` port (implemented by
`approval/adapters/approval{release,resource,ai}`), which opens the request
and returns `gate.Pending`; the service answers it as a typed outcome
(`gate.Outcome`), never an error. When someone with the `approvers` permission
approves, the action runs again as the requester (see `docs/approvals.md`).

## Architecture rules

Tested in `internal/bootstrap` (`policy_architecture_test.go`; the approval
side in `approval_architecture_test.go`):

- only `policy/adapters/policycel` imports cel-go: the domain and service know
  the `Engine` port, not the language;
- release, resource and ai never import `policy` (they use their own port);
- `policy/adapters/*` and `policymodule` are imported only by `bootstrap`.
