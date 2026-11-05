# AI for apps (cut 2d-ai)

An app can use the organization's AI gateway (**freerouter**) with a key of its
own. The control plane makes the key, hands it to the cluster **sealed** (it
never passes through a person, a log or the database) and the agent gives the
app the variables the official OpenAI and Anthropic libraries read on their own. The app's
code needs no configuration.

## What the app receives

While the AI is on, the agent sets four variables on the app (they win over
anything the app defines):

| Variable | Value |
|---|---|
| `OPENAI_API_KEY` | the app's key, from the app's Secret `<app>-secrets` (key `ODICEA_AI_KEY`) through `secretKeyRef` |
| `OPENAI_BASE_URL` | `<gateway>/v1` |
| `ANTHROPIC_API_KEY` | the same key (same `secretKeyRef`) |
| `ANTHROPIC_BASE_URL` | `<gateway>` (the Anthropic library adds `/v1/messages`) |

`<gateway>` is `ODYSSEY_FREEROUTER_APP_URL` (default `ODYSSEY_FREEROUTER_URL`):
the address **apps** reach, which can differ from the one the control plane
uses. Models are listed at `<gateway>/v1/models`. Code examples for apps:
`skills/odicea/references/ia.md`.

The four names are **reserved** while the AI is on: enabling it with one already
defined (plain variable, secret, resource variable) is 409 `VARIABLE_TAKEN`, and
so is defining one later (`PATCH …/env`, secrets, requests, connecting a
resource). `ODICEA_` is the platform's prefix, so nobody can set
`ODICEA_AI_KEY` either. Turn the AI off and the names are free again.

## Configuration

| Variable | Meaning |
|---|---|
| `ODYSSEY_FREEROUTER_URL` | freerouter's API; where the control plane makes and revokes keys |
| `ODYSSEY_FREEROUTER_KEY` / `_KEY_FILE` | the platform's service key at freerouter (`Authorization: Bearer ik_svc_…`) |
| `ODYSSEY_FREEROUTER_APP_URL` | the address apps use (default: the URL) |

URL and key go together or not at all. Without them the AI is **unavailable**:
`GET …/ai` says so (`available: false`, `message`) and turning it on is 422
`AI_UNAVAILABLE`. The same happens when the environment's cluster does not list
the capability `odicea.ai` (an agent from before 2d-ai), and 422
`SECRETS_UNAVAILABLE` when it announced no secret key.

## How it works

Keys live in `ai.keys` (`creating → active → revoking → revoke_failed`; one active key per
environment). Each key has a **deterministic name at the gateway**,
`odicea-<environment>-<key id>`, so it can always be found again without a
stored id. freerouter: `POST /api/v1/service-accounts {name, permissions:
["freerouter:gateway:invoke"], expires_in: "never"}` → `{id, secret}`;
`GET` lists (a bare array, filtered by name prefix on our side); `DELETE /:id`
revokes (204; 404 for an unknown id, treated as already revoked).

Enabling (`ai.enable`, durable workflow; rotating is the same with `ai.rotate`):

1. **`create_key`** revokes any leftover with the same name (a previous attempt
   that crashed after the gateway answered), makes the key, seals it to the
   cluster's key for `ODICEA_AI_KEY` at the next version and returns only the
   sealed bytes. The key in clear exists only in this step's memory.
2. **`apply`**, one transaction: hands the sealed key to the cluster, records
   the AI in the settings (a new revision, `Settings.AI`), marks the key
   `active` and publishes the event. A config deployment applies it.
3. **`settle`** waits until the cluster runs the revision.
4. **`revoke_old`** (rotation) revokes the keys the new one replaced.

A crash anywhere repeats the step with the same effect: a key made but not yet
saved is found by its name and revoked before the retry makes another, so **no
orphan key is left at the gateway**. If the workflow fails for good, the
cleanup revokes what that attempt made.

Disabling changes the settings **in the request's transaction** (the app stops
using the AI at once and the names are free), marks the keys `revoking` and
starts `ai.disable`: wait until the app runs without the AI, then revoke every
key of the environment by prefix and drop the rows. Rotating keeps the old key
valid until the app runs with the new one, so there is no downtime.

### A revocation that fails

Disabling never depends on the gateway answering. The settings change first
(the app is already without the AI), then `ai.disable` revokes the keys **it
marked** (`Input.Keys`, so a key a later enable made is never touched). If the
gateway cannot be reached (or none is connected: the sweep does nothing then,
and disabling says so at once) the workflow retries (`Options.Attempts`, 8) and
then its `OnFailure` marks those keys **`revoke_failed`** with a bounded reason
(`ai.FailureReason`), in the same transaction, and publishes `ai.revoke_failed`
for the audit trail. The view then has `status: revoke_failed`, `warning` ("No
se pudo revocar la llave en el gateway de IA; TI debe revisarlo.") and
`revoke_error`. Recovery:

- the console's **Reintentar** (a new `DELETE`) moves them back to `revoking` and
  starts a new `ai.disable`;
- the sweep `ai.revoke_sweep` (`workflow.Registry.Every`, every 5 minutes, one
  replica per tick) does the same for every environment that has such keys, so
  an outage that ends fixes itself. It backs off: each failure counts
  (`revoke_attempts`) and sets `next_retry_at = now + min(5 min × 2^(attempts-1),
  6 h)`; the sweep only takes keys that are due (never-waited first, then the
  longest due), so a long outage neither re-runs a workflow every tick nor
  starves other environments. `ai.revoke_failed` is published only on the
  **first** failure (attempts 0→1); a sweep retry that fails again is silent, and
  every sweep retry publishes `ai.revocation_retried` (system actor) because it
  changes the keys' status. A person's **Reintentar** starts the backoff over.
- the sweep **does not need the app**: revoking only needs the gateway ids the
  rows hold. If the project or environment no longer exists, the run (`Input.Gone`)
  skips waiting for the app, revokes at the gateway and drops the rows.

**Turning the AI on while an old key waits for revocation is allowed.** The new
key is independent (its own name at the gateway, its own row, one active key per
environment still holds), the old one is already useless to the app, and
refusing would make TI fix the gateway before the team can use the AI again.
The warning stays on the view until the old key is revoked.

Every change of an environment's AI takes `ai.Repository.LockEnvironment`
(`TestAIChangesAreSerialised`); asking for what is already in force changes
nothing; a change while another one runs is 409 `AI_BUSY`; rotating without the
AI on is 409 `AI_NOT_ENABLED`.

## API

`docs/api.md` §11. Permissions: read `projects:read`, changes `projects:write`.

## Events

`ai.enabled`, `ai.rotated`, `ai.disabled` (`key_id`, `project_id`,
`environment_id`; never the key), `ai.revoke_failed` (`project_id`,
`environment_id`, `reason`; first failure only) and `ai.revocation_retried`
(`project_id`, `environment_id`, `attempts`). They reach the audit trail.

## Limits

- **Consumption is not shown.** freerouter attributes usage to a user or
  organization, not to a service key, so the console cannot say what an app
  spent. **Pendiente en freerouter**: usage per service account.
- One key per app environment; the key does not expire (its life is the
  platform's: rotate or disable).
- **`ListKeys` is an unpaginated, client-side filter.** freerouter's
  `GET /api/v1/service-accounts` returns every service account and the adapter
  filters by the `odicea-<environment>-` prefix. Fine for a few hundred
  accounts; **Pendiente en freerouter**: a `name` (prefix) filter and
  pagination. The adapter's cost grows with the whole gateway, not with the
  environment.

## Reaching the gateway under egress

The gateway is just a URL, and in `internet` mode an internal address (a freerouter
on the company network, `host.k3d.internal` → 192.168.65.254 in the local k3d) is in a
range the mode keeps closed. So the agent opens it **for the apps that use AI,
and only for them** (no "TI must list it" step):

- the pods of an app with `spec.ai` carry `odyssey.dev/ai: "true"` (set by the
  agent's pod template; the app's own labels cannot set it);
- the NetworkPolicy **`odicea-ai`** of the namespace selects those pods and allows
  egress to the gateway of each such app: the **resolved IPs** of the
  `spec.ai.baseURL` host (`/32`, `/128`) on its port (the URL's, else 80/443 by
  scheme). For a host `name.namespace.svc[.cluster.local]` (an in-cluster Service)
  it allows the pods of that namespace instead (`kubernetes.io/metadata.name`);
- it is rewritten when an app arrives, changes or drops its AI (`desired.Syncer`
  fences before the pod can start) and every 2 minutes (a gateway that moves is
  followed). A host that does not resolve **keeps the policy as it was** and shows
  in the egress state as a failure to retry; the policy is **removed** when no app
  of the namespace has AI.

Policies are additive: `odicea-ai` adds to `odicea-apps`, whatever the mode
(also `restricted`). Pods without AI, and every other namespace, still cannot
reach it. Details: `docs/egress.md`.
