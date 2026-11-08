# Aprobaciones — approvals (cut 3b)

An `approval` rule (`docs/policy.md`) does not refuse an action: it **holds**
it until someone from IT decides. The `approval` domain owns that request: its
lifecycle, who may decide, and the events. The governed domains (release,
resource, ai, project) never import it; each opens requests through its own
`Approvals` port, which an adapter in `approval/adapters/approval{release,
resource,ai,project}` implements (the same pattern as `policy`).

## The request

| field | meaning |
|---|---|
| `action`, `project_id`, `environment_id` | what is held and where (`deploy`, `rollback`, `set_access`, `connect_resource`, `delete_resource`, `create_resource`, `change_resource`, `enable_ai`, `set_services`); `environment_id` is empty for an action about the whole app (`set_services`) |
| `summary` | the action in one plain Spanish sentence |
| `payload` | the domain's own description of the action, as it re-runs |
| `requested_by` (+ `requested_system`) and the requester's permissions | who asked, and what their token could do **when they asked** (a snapshot) |
| `reason`, `rules` | why it was held: the rules' names |
| `approvers` | permissions that can decide; **any one** suffices (default `odyssey:approvals:decide`) |
| `status` | `pending` → `approved` / `rejected` / `expired` / `cancelled` |
| `decided_by`, `decided_at`, `decision_note` | the decision |
| `expires_at` | 7 days after it was asked |
| `consumed_at` | when the approval was used: **one use** |
| `execution_error` | an approved action that could not run, and why |
| `run_id`, `run_step` | the workflow step that waits for it (deployments); unique, so asking twice is one request |

Asking again for the same thing while it is pending (same person, same
action, same place) returns the request that exists.

## Who decides

- Needs **any one** of the request's `approvers` permissions (403 otherwise);
  the permissions are read from the token, never from the body.
- **Never the requester** — even holding the permission (403 `APPROVAL_SELF`).
- Only a **pending** request can be decided (409 `APPROVAL_DECIDED`).
- The requester can **cancel** their own pending request
  (`POST /approvals/:id/cancel`); nobody else can.
- A request is visible to its requester and to the people who could decide it;
  anything else, and any other organization's, is 404.

## Direct actions

`set_access`, `connect_resource`, `delete_resource`, `create_resource`,
`change_resource`, `enable_ai`, `set_services` (a create with `connect` is ONE
`create_resource` request whose rules include the connection's; a create or
change outside the catalog's pre-approved range is held by the synthetic rule
`catalog:preapproved`; its payload is v2 — `schema_version` 2 with the pinned
`recipe@version` and parameters — and a v1 payload fails with
`APPROVAL_OUTDATED`): the service answers a
typed **outcome** (`gate.Outcome`), not an error — HTTP **202**
`{status:"pending_approval", request_id, message}`; MCP the same result with a
`next_step`. Nothing changes yet.

When someone approves, a handler in the composition root (bootstrap; the module
subscribes `approval.approved`) re-runs the action **as the requester**:

- **Actor = the requester's snapshot** (user and the permissions they had when
  they asked). Approving therefore never widens what the requester could do; a
  requester who has since lost the permission is not rescued by the approval
  — the re-run fails and says why.
- The re-run carries an **approval mark** (`gate.WithApproval`). The governed
  service skips IT's rules **only after redeeming the approval** through its
  port: it must be approved, unused, the same organization, action, place and
  payload. Redeeming consumes it atomically (409 `APPROVAL_NOT_USABLE`
  otherwise), so a mark cannot be replayed or used for something else. If the
  action ends without redeeming (the rules were changed and now allow it, or it
  was a no-op), the reactor **consumes** the approval itself after the action
  succeeds (idempotent; `approval.consumed` once), so an approved request is
  never left unused.
- A failure is recorded on the request (`execution_error`) and published
  (`approval.execution_failed`); nothing retries it. The requester asks again.

### Staleness of the requester snapshot

The re-run trusts the authorization the requester had **when they asked** (user
+ permissions, kept up to 7 days, the request's TTL). Odyssey has no port to
ask the identity provider whether a user is still active (IAMKit is
authentication only, and not touched here), so a user deactivated in between is
not detected: the approval re-runs as their snapshot. What does protect: the
re-run **re-evaluates the policy** (the approval only satisfies the `approval`
verdict it was opened for), so a **deny** rule added later wins and the request
ends with `execution_error`; and the action's own checks (project exists, mode
valid, permissions the service itself requires) run again. If IT needs to stop
it, they **reject** or the requester **cancels** the pending request.

## Deployments

`release.deploy` starts with an `authorize` step, **before** the build:

1. It judges the deployment's action (`deploy` / `rollback`) **as the
   deployment's requester**: the actor snapshot stored in the run's input (a
   GitHub push is the system actor `github:<login>`).
2. `deny` → the deployment fails (`POLICY_DENIED`); `allow` → it continues.
3. `approval` → opens a request (idempotent by run + step), sets the
   deployment **`waiting_approval`** ("Esperando aprobación de TI") and parks the
   run with `WaitFor("approval", 7d)`.
4. Approve → the engine is signalled and the run continues (the approval is
   consumed). Reject / expire / cancel → the deployment is **cancelled** with
   `reason` `approval_rejected` / `approval_expired` / `approval_cancelled`.
5. A newer deployment of the environment that replaces a waiting one cancels
   its request.
6. If the step runs again (a retry) and the rules now **allow** or **deny** the
   deployment, the request this run opened is **cancelled** (published as
   `approval.cancelled`) so nobody is asked about something that no longer
   needs it.
7. **Upgrading.** A run that started before 3b has no actor in its input (it
   was judged deny-only when it was asked). Judged now as nobody it would be
   held or refused for something the old rules accepted, so `authorize` lets
   it through and logs a warning. Only runs in flight during the upgrade are
   affected.

**A waiting deployment holds the environment's slot.** An environment has one
unfinished deployment at a time, so a deployment `waiting_approval` is the
active one: the next deploy **or rollback** supersedes it (cancelling it and its
request). A **rollback in production** that an approval rule holds is therefore
also a waiting deployment, and a later rollback replaces it. Rollbacks stay
judged by the rules like any action: IT who do not want them held disable the
built-in or the rule (none governs `rollback` by default, `docs/policy.md`).

The Deploy / DeployUploaded / DeploySource / Rollback calls no longer judge the
action at the start; they keep a **cheap deny-only pre-check** so an obvious
denial answers 403 right away instead of becoming a failed deployment. **Not
judged**: config deployments (a settings change; `set_access` is judged where
it is asked) and auto-rollback (the platform restoring the version that worked).

> **3c.** After the build there is a second gate, `authorize_scan`, with the
> scan's numbers (`scan.known`, `scan.critical`, …); see `docs/scan.md`. It uses
> the same `authorize` machinery: a deny fails the deployment, an approval is
> held at `step = authorize_scan` (the run parks on its own signal, and
> `approval_scan` acts on the decision), and a rule that already asked at
> `authorize` is not asked again.

## Expiry

A workflow schedule (`approval.expire`, every 5 minutes) marks the pending
requests past `expires_at` as `expired` (publishing `approval.expired`); a
waiting deployment is then cancelled with `approval_expired`.

## Events

`approval.requested`, `approval.approved`, `approval.rejected`,
`approval.expired`, `approval.cancelled`, `approval.consumed`,
`approval.execution_failed` (goldens in `internal/approval/testdata/events`).
Only the composition root subscribes (`approval.signal_runs` signals the waiting
run; `approval.execute_approved` re-runs a direct action).

## API and MCP

REST: `GET /api/v1/approvals?status=&mine=&decidable=`, `GET /approvals/:id`,
`POST /approvals/:id/approve|reject` (`{note}`), `POST /approvals/:id/cancel`
(`docs/api.md` §11). MCP: the held tool returns `pending_approval`;
`get_request` / `list_requests` follow it (read-only: an assistant never
decides; their `console_url` is `/bandeja` or `/bandeja?id=<id>`).

**Confirm is not approval.** `mcpx.Confirm` asks *the user* (not IT) before a
hard-to-undo action, and it comes **first**, before the rules are consulted:
`rollback` of a **production** app and `set_access public`. In order: the
input's `confirm: true`; else MCP elicitation if the client supports it (accept
goes on, decline/cancel is 409 `CONFIRMATION_DECLINED`); else 409
`CONFIRMATION_REQUIRED` and the assistant asks the user and repeats with
`confirm: true` (Odicea's MCP endpoint is stateless, so most clients take the
last path). A confirmed action may then still be held by an `approval` rule.
There is **no MCP `delete_resource` tool** (deleting a resource is console/REST
only), so confirm does not apply to it; REST has no confirm step at all (the
console asks in its own dialogs).

## Console

A bell in the top bar counts what the viewer can decide; **Bandeja**
(`/bandeja`) lists requests (pending / decided; "Mis solicitudes" with
Cancelar) and IT approves or rejects with a note. `/bandeja?id=<request id>`
opens one request on top of the lists (the link `get_request` gives the
assistant). Direct actions that answer
`pending_approval` show a calm notice ("Le pedimos a TI que lo apruebe").

## Architecture rules

Tested in `internal/bootstrap/approval_architecture_test.go`:

- release, resource, ai and source never import `approval`;
- `approval` knows the governed domains only in its bridges
  (`approvalrelease`, `approvalresource`, `approvalai`) and `approvalmodule`;
- `approval/adapters/*` and `approvalmodule` are imported only by `bootstrap`;
- the governed services do not call `approvals.Hold` inside a transaction
  (the deployment's `authorize` step is the one place that does, to be atomic
  with the status).
