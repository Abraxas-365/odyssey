# Rule input

Everything a rule's condition can read, the actions Odicea checks, how verdicts combine, what happens when a rule cannot be evaluated, and the built-in rules.

A rule is a [CEL](https://cel.dev) expression that must give `true` or `false`. Odicea evaluates every enabled rule over one input object each time someone tries a governed action. To write rules step by step, see [Write a rule](../guides/it/rules.md). The full contract is in [`policy.md`](../policy.md).

## Rule fields

| Field | Meaning |
|---|---|
| `name` | Required, shown in the console and in refusal messages. |
| `description` | Optional sentence ("En palabras" in the console). |
| `expression` | The CEL condition. Required, at most 2000 characters, must give a boolean. |
| `effect` | `deny`, `approval` or `allow`. |
| `approvers` | Permission that may decide the request when the verdict is `approval`. Defaults to `odyssey:approvals:decide`. |
| `enabled` | New rules are on unless said otherwise. |

An organization can have at most 100 rules (`POLICY_TOO_MANY`, 422). An expression that does not compile is refused with `POLICY_INVALID` (422, field `expression`) when you save it or try it. Managing rules needs the permission `odyssey:policy:manage`. See [Permissions](permissions.md).

## Input variables

A rule reads these variables. What an action does not carry has the zero value (empty text, `false`, `0`, empty list), so a rule never fails because a field is missing.

| Variable | Type | Value |
|---|---|---|
| `action` | string | One of the [governed actions](#governed-actions). |
| `project.tier` | string | `sandbox`, `internal` or `production`. |
| `project.slug` | string | The app's short name. |
| `env.name` | string | The environment. Empty for `set_services`, which is about the whole app. |
| `access.mode` | string | `team`, `organization` or `public`. Only set for `set_access`. |
| `actor.id` | string | Who acts. A publication triggered by a GitHub push is `github:<login>`. |
| `actor.permissions` | list of string | Permissions of the authenticated principal. Empty for GitHub pushes. |
| `resource.kind` | string | `postgres`, `redis`, `files` or `object-storage`. For the resource actions. |
| `resource.recipe` | string | Catalog recipe that resolved the resource, such as `postgres-produccion`. For `create_resource` and `change_resource`. |
| `resource.recipe_version` | int | Version of that recipe. |
| `resource.engine` | string | `builtin` or `existing`. |
| `resource.params` | map of string to int | Every recipe parameter with defaults filled in (today `storage_gi`). For a change, the result. |
| `resource.out_of_range` | list of string | Parameters outside the offering's pre-approved range. |
| `scan.known` | bool | An image vulnerability scan exists and was completed. |
| `scan.checked` | bool | The scan was looked at, whatever came of it. |
| `scan.critical`, `scan.high` | int | Findings by severity. Meaningful only when `scan.known` is `true`. |
| `services` | list of `{name, kind, route, public}` | What the action brings: the version a deploy or rollback puts online, or the new list of `set_services`. `kind` is `web` or `worker`; `route` is empty for an internal service or a worker; `public` is `true` for a web service with a route. |
| `changed` | list of string | Names of the services that change against what the environment serves. |
| `topology_changed` | bool | A service appears, goes away, or changes kind, folder, route or prefix stripping. |

### Who `actor` is

- Over the API, `actor.permissions` are the token's claims.
- Over MCP they are the permissions the user consented to for that assistant, not everything the user could do.
- For a deployment that runs later (an upload, a repository import), the actor is the person who asked, with the permissions they held at that moment.
- For a GitHub push, `actor.id` is `github:<login>` and `actor.permissions` is empty. `!('odyssey:projects:tier' in actor.permissions)` therefore matches every push. To single pushes out, use `actor.id.startsWith('github:')`.

### Services and topology

An app of one service has one entry, `web` at `/`, so `services.all(s, s.kind == 'web')` reads the same for a single-service app and for one with several parts.

- `changed` is relative to the version the environment serves. For `set_services` it names the services added, removed or modified.
- `topology_changed` is always `true` for `set_services`; submitting the same list again is not judged.
- A deployment from source is judged twice (see [Scan inputs](#scan-inputs)). Before the build, `changed` names only services that appear, go away or are laid out differently. After the build it also names services whose code changed.

```cel
// no workers in sandbox
action == 'deploy' && project.tier == 'sandbox' && services.exists(s, s.kind == 'worker')

// a new public route in production waits for IT
action in ['deploy', 'rollback'] && project.tier == 'production' && topology_changed && services.exists(s, s.public)

// the api service changes only with IT
action == 'deploy' && 'api' in changed
```

### Scan inputs

A deployment from source is judged at two points:

| Judgment | When | `scan.checked` | `scan.known` |
|---|---|---|---|
| `authorize` | Before the build | `false` | `false` |
| `authorize_scan` | After the build, scan done | `true` | `true` |
| `authorize_scan` | After the build, scan failed, never arrived or the cluster has no `odicea.scan` | `true` | `false` |

After the build, Odicea waits up to 10 minutes for the scan. `scan.known` implies `scan.checked`.

- A rule about the numbers should say `scan.known && scan.critical > 0`. An unknown scan is not a clean one.
- A rule that requires a scan should say `scan.checked && !scan.known`. Plain `!scan.known` also matches the first judgment, before anything is built.
- An approval that `authorize` already asked is not asked again at `authorize_scan`.
- Publishing an image as it is (`deploy_image`) and config-only deployments are never scanned. Only the first judgment applies to them.
- A version of several services is judged as one. The scan is the sum of all its images, and unknown if any image has no usable scan.

## Governed actions

| `action` | Console label ("Probar") | When it is judged |
|---|---|---|
| `deploy` | Publicar una versión | First step of the deployment, before the build, and again after it (`authorize_scan`). Applies to images, folders and Git commits. |
| `rollback` | Volver a una versión anterior | First step of the deployment. |
| `set_access` | Cambiar quién puede abrirla | Before the setting is stored. |
| `connect_resource` | Conectar una base de datos o archivos | Before connecting. |
| `delete_resource` | Borrar una base de datos o archivos | Before deleting. |
| `create_resource` | Crear una base de datos, caché o archivos | Before anything is provisioned. Creating with a connection is one judgment. |
| `change_resource` | Agrandar una base de datos o archivos | Before changing. |
| `enable_ai` | Activar la IA | Before turning it on. |
| `set_services` | Cambiar los servicios de una app | Before the new list is stored. |

Some things are deliberately not judged:

- `set_secret` is a valid action name, but nothing asks about it. Secret values never reach the rules.
- Creating a resource without connecting it to an app.
- Rotating the AI key. Only turning the AI on is judged.
- No-ops, such as asking for the access mode the app already has.
- The platform's own `auto_rollback`, when a failed deployment puts back the version that was serving.
- Config-only deployments (a settings change).

Resource actions also meet a catalog floor: a create or change outside the offering's pre-approved range is held by the synthetic rule `catalog:preapproved`, whatever your rules say. A `deny` still wins. See [Catalog](../guides/it/catalog.md).

## Verdicts and precedence

Odicea evaluates every enabled rule, then decides:

1. Any matching `deny` rule: the action is **denied**.
2. Otherwise, any matching `approval` rule: the action is **held for approval**.
3. Otherwise it is **allowed**. This includes the case where no rule matches, or no rules exist.

An `allow` rule never overrides a `deny` or an `approval`. It exists to be explicit and to show up in a dry run. Disabled rules are never evaluated.

| Verdict | API answer | Message |
|---|---|---|
| deny | 403, code `POLICY_DENIED` | La regla de TI «…» no permite esto. |
| approval | 202 `{status: "pending_approval", request_id, message}`. A deployment is accepted and then becomes `waiting_approval`. | Le pedimos a TI que lo apruebe. |

Over MCP a denial is a tool error whose `next_step` tells the assistant to show the message and not look for a workaround. An approval is a `pending_approval` result with a `request_id`. See [Approvals](../guides/it/approvals.md).

When an approval rule matches, the people who may decide are those holding the rule's `approvers` permission. If several rules match, any of their approver permissions is enough.

A GitHub push that a rule denies fails at once with `DEPLOY_REFUSED` and the rule's message. A push that a rule holds for approval creates a deployment that waits (`waiting_approval`).

## Fail-closed behavior

A rule that cannot be evaluated counts at its own strength, so a rule meant to stop something still stops it.

| Rule effect | Cannot be evaluated | Counts as |
|---|---|---|
| `deny` | Runtime error, cost over the limit, or no longer compiles | deny |
| `approval` | Same | approval |
| `allow` | Same | Ignored |

"Cannot be evaluated" means a runtime error (a type error, division by zero), more than 10,000 CEL steps, more than 100 ms, or a stored expression that no longer compiles. The reason appears in the console's "Probar" panel. If the rule store itself fails, the action fails with a 500 or 502. It is never allowed.

## Built-in rules

Every organization gets six built-in rules. They can be switched on and off. They cannot be edited or deleted (`POLICY_BUILTIN`, 422): to change one, switch it off and write your own.

| Name (as shown in the console) | Effect | Default | Condition |
|---|---|---|---|
| Publicar en producción requiere aprobación de TI | approval | On | `action == 'deploy' && project.tier == 'production'` |
| Hacer pública una app requiere aprobación de TI | approval | On | `action == 'set_access' && access.mode == 'public'` |
| Producción no publica con vulnerabilidades críticas | deny | On | `action == 'deploy' && project.tier == 'production' && scan.known && scan.critical > 0` |
| Sandbox no puede ser pública | deny | On | `action == 'set_access' && project.tier == 'sandbox' && access.mode == 'public'` |
| Producción exige revisión de vulnerabilidades | approval | **Off** | `action == 'deploy' && project.tier == 'production' && scan.checked && !scan.known` |
| Cambiar los servicios de producción requiere aprobación de TI | approval | **Off** | `action == 'set_services' && project.tier == 'production'` |

Their internal ids are `production_deploy_approval`, `public_access_approval`, `production_critical_vulnerabilities`, `sandbox_not_public`, `production_requires_scan` and `production_services_approval`, in that order.

> [!WARNING]
> "Producción no publica con vulnerabilidades críticas" does not stop an image that was never scanned, or whose scan failed. A builder controls the image and can make its scan fail on purpose. To close that gap, switch on "Producción exige revisión de vulnerabilidades". It is judged only after the build, so it does not ask for a second approval before anything is built. It does not apply to images published as they are, which have no build to scan. Govern those with "Publicar en producción requiere aprobación de TI".

The organization's rules are created the first time they are listed or evaluated. An organization that existed before a built-in was added receives it in its default state, so the two rules that are off stay off.

Rollbacks have no built-in rule. Going back to a version that was already online is the undo of a bad deploy, so slowing it down is the wrong default. You can still write a rule for it. If a rule holds a rollback in production, it waits like any deployment and a newer deployment or rollback replaces it.

## Trying a rule

`POST /api/v1/policy/evaluate` is a dry run. It evaluates the saved rules over an input you supply, optionally with one unsaved candidate rule as if it were enabled. Nothing is recorded or stopped. The console's "Probar" panel uses it. The response lists every rule with `enabled`, `matched` and, when it could not be evaluated, `error`.

Compiled expressions are cached by rule id and version. A saved edit bumps the version, so a stale program is never used. Evaluation limits: cost 10,000, timeout 100 ms, parser recursion depth 32.

## See also

- [Write a rule](../guides/it/rules.md)
- [Approvals](../guides/it/approvals.md)
- [Permissions](permissions.md)
- [`policy.md`](../policy.md), the full contract
- [`scan.md`](../scan.md), how images are scanned
