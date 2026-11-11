# Architecture

How Odicea's code is organized: the processes, the packages and what each may import, how a request and a deployment travel through them, and which tests keep the layout honest.

This page is for contributors. It gives the shape; the rules that must not break, and the test that enforces each, are in [AGENTS.md](../AGENTS.md). If this page and AGENTS.md disagree, AGENTS.md and the code win.

## Processes

Odicea is one Go module that builds four binaries. Three make up the control plane and one runs in each customer cluster.

| Binary | Entry point | Does |
| :--- | :--- | :--- |
| `odyssey-api` | `cmd/odyssey-api` → `bootstrap.Run` | The HTTP API, the MCP endpoint and the sign-in for protected apps (port 8095). |
| `odyssey-worker` | `cmd/odyssey-worker` → `bootstrap.RunWorker` | Delivers the event outbox to subscribers and runs workflows and scheduled tasks. Health on 8096. Any number of replicas. Subcommands `deliveries dead` and `deliveries retry <subscriber>`. |
| `odyssey-gateway` | `cmd/odyssey-gateway` → `bootstrap.RunGateway` | The mutual-TLS gRPC server agents dial (8443, health 8098). The only holder of the fleet CA's private key. `ca init` creates the CA. |
| `odyssey-agent` | `cmd/odyssey-agent` → `agent.Run` | Runs in a cluster: reconciles `OdysseyApp`, `OdysseyResource` and `OdysseyBuild`, runs the sign-in gate and the source server. Also `builder prepare` and `builder build`, which a build pod runs. |

The console (`frontend/`) is a separate single-page app that talks to the API. The API, worker and gateway share one Postgres database; the agent shares only the protocol in `proto/fleet/v1` with them.

```text
 console ──► odyssey-api ──┐
 assistant ─► (MCP)        ├──► Postgres ◄── odyssey-worker (outbox, workflows)
                           │        ▲
                           │   odyssey-gateway ◄══ mTLS, dialled out ══ odyssey-agent
                           │                                              │
                           └─ no connection to the cluster ────  Kubernetes objects
```

## Package layout

Every package is in one of four roles. The arrows go one way: a package may import only the roles to its right.

| Role | Packages | May import |
| :--- | :--- | :--- |
| Foundation | `errx`, `identity`, `query`, `text`, `config`, `txn`, `event`, `blob`, `permission`, `httpx`, `pgerr`, `migrate`, `pgtest`, `workflow`, `mcpx`, `assistant`, `actor`, `gate`, `channel` | other foundation packages only, never a domain |
| Domain | `project`, `source`, `build`, `release`, `fleet`, `resource`, `catalog`, `ai`, `policy`, `approval`, `audit`, `inventory`, `access`, `account` | foundation; other domains only through their own narrow ports, never a service or an adapter |
| Composition root | `bootstrap`, `server` | everything. Only `bootstrap` imports `*module` packages |
| Client | `cmd/*`, `agent/*`, `frontend/` | `agent/` never imports `internal/`, and `internal/` never imports `agent/` or Kubernetes libraries. Shared code that both planes run lives in `pkg/` |

## What each domain owns

| Domain | Owns |
| :--- | :--- |
| `project` | Apps, their environments, their tier and their parts. The golden sample for the shape of a domain. |
| `source` | Where code comes from: the GitHub App, repository connections, pushes and commit statuses. |
| `build` | Uploaded sources, builds and their logs and scans. |
| `release` | Releases, deployments and the deploy workflow; variables, secrets and access mode of an environment (the "settings"); what an environment serves. |
| `fleet` | Clusters, enrolment tokens, the certificate authority, the gRPC gateway, placement of environments, egress policy. |
| `resource` | Databases, caches, files and connections to existing resources, over the engines registered in `pkg/resourcekind`. |
| `catalog` | Recipes (immutable versions) and each organization's offerings. |
| `ai` | An app's own key at the AI gateway. |
| `policy` | Tiers, IT's rules (CEL) and their evaluation. |
| `approval` | Requests held by an `approval` rule, their decisions and expiry. |
| `audit` | The hash-chained activity trail. A subscriber of every event. |
| `inventory` | Read-only inventory and the attention list, composed from other domains' queries. Owns no table. |
| `access` | The sign-in of protected apps: authorize, callback, ticket signing and the public key set. |
| `account` | `GET /api/v1/me` and the connected assistants. Stateless. |

## Inside a domain

`internal/project` is the sample to copy; see AGENTS.md for the table of what each file does.

```text
internal/project/
├── id.go                    typed ID, a generic over a tag
├── ports.go                 Commands, Queries, Repository: interfaces only
├── project.go               entities, inputs with Validate(), business errors
├── events.go                event types and payloads (import no other domain)
├── projectsvc/              orchestration: validate, IDs, transactions
├── adapters/
│   ├── projectpg/           SQL, migrations/NNN_*.up.sql, its own Postgres schema
│   ├── projecthttp/         net/http routes (httpx.Router), permission per route
│   └── projectmcp/          MCP tools
└── projectmodule/           New(Deps) → Module: wires it all
```

- **The domain root** has the vocabulary: types, ports and errors. It imports no transport and no SQL.
- **The service** (`*svc`) is the only place that orchestrates. It calls `Validate()` on its inputs, takes the organization from the caller, opens a transaction with `txn.Runner` and publishes the domain's events inside it.
- **Adapters** implement ports for one technology. An adapter that lets a domain talk to another is named after the pair, for example `releasefleet` is how `release` reaches `fleet`, and only `bootstrap` wires it.
- **The module** assembles the pieces and exposes routes, tools, workflows and subscriptions to the composition root.

## Composition root

`internal/bootstrap` builds everything and is the only package that sees all of it. `Assemble` creates the platform: it opens the database, builds each module with the adapters other modules need, registers workflows and event subscriptions, and seals the event bus. `New` adds the HTTP server on top for the API. `RunWorker` runs the same platform without HTTP and with the outbox dispatcher and the workflow engine. `Gateway` serves the gRPC protocol.

Each domain declares what it needs from others as its own small port. The adapter that satisfies it, such as `releasebuild` or `policyrelease`, lives with the domain that needs it and is wired only here. That keeps two domains from knowing each other and is why the dependency rules can be checked by a test.

## How a request travels

```text
HTTP request
  → httpx: authenticate the token (IAMKit), put the actor on the context
  → <domain>http: RequirePermissions(permission.X), decode, call the service
  → <domain>svc: Validate, ask IT's rules if governed, open a transaction
      → repository: SQL filtered by organization_id (txn.Q joins the transaction)
      → publish the event into the outbox, same transaction
  → response: errx type mapped to the status in docs/api.md
```

The organization is read from the token and nowhere else. A row of another organization is "not found". A governed action (publish, change access, create storage, enable AI, change parts) asks its domain's `Policy` port first. The answer can be a refusal, a held request (HTTP 202, MCP `pending_approval`) or a go-ahead. See [api.md](api.md).

## Events and workflows

- **Events.** A write publishes its event in the same transaction as the change (a transactional outbox in Postgres). The worker delivers each event to subscribers at least once, so subscribers are idempotent. Only `bootstrap` subscribes. `audit` is a subscriber of every event, which is why "every write publishes an event" is also "every write is audited". Every event type has a golden file in `testdata/events/`.
- **Workflows.** Long work is a named sequence of steps stored in Postgres and run by the worker through River, which only `workflowpg` imports. A step can retry, poll, or park until a signal arrives (an approval). A deployment is `release.deploy`; a build is `build.run`; others cover AI keys, archiving and the periodic sweeps. Steps are positional, so adding one in the middle needs care with runs in flight.
- **Migrations.** Each module owns a Postgres schema and its embedded `NNN_*.up.sql` files. A migration that has been applied is never edited: its checksum is verified at boot.

For the deployment steps see [Life of a version](explanation/life-of-a-version.md).

## The agent and the protocol

The gateway and the agent speak `proto/fleet/v1`. Fields are only added; a rename would be a new package. The calls are `Enroll` (token for certificate), `Renew`, `Recover`, `Connect` (the long-lived tunnel in which the control plane sends the desired state and the agent reports what it sees) and `FetchSource` (a build's source, streamed). The desired state arrives as an `OdysseyApp`, a controller turns it into a namespace, deployment, service, ingress and network policies, and the observed phase goes back up the tunnel. See [CRDs](reference/crds.md) and [Capabilities](reference/capabilities.md).

Code that both planes run, or that apps import, lives in `pkg/`: `srcarchive` (the rules for source archives), `secretbox` (sealing of secrets), `resourcekind` and `resourcespec` (the contract of each storage kind), `detect` (finding an app's parts in a folder) and `odiceaauth` (the library an app uses to verify the sign-in assertion).

## Architecture tests

The layout is a test. These run in `go test ./...`, mostly in `internal/bootstrap`, and the full list with the rule each enforces is in AGENTS.md.

| Test | Keeps |
| :--- | :--- |
| `TestLayerDependencies` | Foundation imports no domain; domains and services import no SQL or transport; only `bootstrap` imports modules. |
| `TestPortsAreInterfaces` | `ports.go` has interfaces only. |
| `TestIAMKitStaysAtTheEdge` | Domains know typed IDs, never identity-provider claims. |
| `TestServicesValidateInputs` | Services validate what they receive. |
| `TestNoPermissionLiterals` | `odyssey:...` strings live only in `internal/permission`. |
| `TestGeneratedFilesAreFresh` | Generated permissions match `iam/permissions.json`. |
| `TestOnlyTheCompositionRootSubscribes` | Events are wired in one place. |
| `TestRiverStaysInTheAdapter` | The workflow engine is replaceable. |
| `TestOnlyThePromoteStepMovesTheCurrentRelease` | Only the deploy workflow's last step changes what is served. |
| `TestApprovalsAreNotHeldInsideATransaction` | A request for approval is never opened inside a transaction, except at the one place a deployment holds it atomically with its status. |
| `TestRoutesMatchTheOpenAPISpec` | `api/openapi.yaml` is exact. |
| `TestMCPToolsContract` | The MCP tool list is a golden file. |

> [!WARNING]
> Never weaken an architecture test to make a change pass. Fix the code, or, if the rule is wrong, change the rule and AGENTS.md in the same pull request, and add a fixture to `TestArchitectureRulesCatchViolations`.

## Multi-process design

The API holds no state in memory that others need. Any number of API and worker replicas can run against one database. The worker's outbox dispatcher and workflow engine coordinate through Postgres. Gateway replicas coordinate through Postgres as well: a message for a cluster reaches whichever replica holds its tunnel. The one single-replica component is the sign-in gate in each cluster, whose used-ticket memory is per pod.

## Tenancy

The organization is a typed ID taken from the authenticated token, passed right after the context to every port, and filtered in every SQL statement. The one place an environment is looked up without it (to answer a sign-in for a protected app) resolves the tenant from Odicea's own records, not from the request. Object storage keys are prefixed by organization (`blob.TenantKey`).

## Where to go next

- [AGENTS.md](../AGENTS.md): invariants, commands, file map.
- [Contributing](contributing/README.md): set up, checks and conventions.
- [Concepts](explanation/concepts.md) and [Security model](explanation/security-model.md).
- The design contracts: [api.md](api.md), [policy.md](policy.md), [approvals.md](approvals.md), [access.md](access.md), [secrets.md](secrets.md), [egress.md](egress.md), [scan.md](scan.md), [catalog.md](catalog.md), [resources.md](resources.md), [ai.md](ai.md), [inventory.md](inventory.md).
