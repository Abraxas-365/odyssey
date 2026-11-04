# AGENTS.md — Odyssey

Odyssey is a governed, enterprise Vercel-like platform. It runs on the customer's
own infra (on-prem/AWS/GCP/Azure). Business users ("vibecoders") connect a repo
and deploy, with security set by IT. Odyssey offers one MCP for coding agents
and uses IAMKit for platform and app auth.

This repo holds the **control-plane API** (`cmd/odyssey-api`), the **worker**
(`cmd/odyssey-worker`: outbox delivery, workflows, scheduled tasks), the
**gateway** (`cmd/odyssey-gateway`: where agents enrol and keep an mTLS gRPC
tunnel; the only process holding the fleet CA key), the **data-plane agent**
(`cmd/odyssey-agent`, `agent/`: runs in each cluster, dials out to the
gateway, applies the desired `OdysseyApp`s it is sent and turns them into
Deployment + Service + Ingress) and the **web console** (`frontend/`). Domain
modules today: `project` (the golden sample; apps and their environments),
`release` (releases, deployments and the `release.deploy` workflow behind the
`Cluster` port), `fleet` (clusters, enrolment, the fleet CA, desired state
and what agents report) and `audit` (an event subscriber). The worker's
`ODYSSEY_CLUSTER` picks the `Cluster` adapter: `fake` (default, in memory; an
image repository containing `unhealthy` / `reject` simulates a failed health
check / a refused apply) or `fleet` (`releasefleet`: the environment's
cluster, through `fleet.Dispatch`). The control plane holds no cluster
credentials and never talks to Kubernetes. The full
design (binaries, domains, build order) lives outside the repo, in
`~/.rness/plans/Personal/odicea-architecture.md` (the fleet:
`odicea-fase1a3-fleet.md`). The UI design is in
`odicea-mockup.html` and `odicea-product-ux-v3.md` there.

## Commands

| Task | Command | ~Time |
|---|---|---|
| Build | `make build` (→ `bin/odyssey-{api,worker,gateway,agent}`) | 5s |
| Unit + arch tests (DB tests skip) | `make test` | 5s |
| All tests vs compose Postgres | `make test-db` (`PKG=./internal/project/...`) | 10s |
| Local CI gate (gofmt, vet, test) | `make check` (CI also runs `-race` vs Postgres: `.github/workflows/ci.yml`) | 10s |
| Run the worker on the host | `make worker` (health on 8096; `CLUSTER=fleet` deploys to the enrolled clusters) | 2s |
| Run the gateway on the host (fleet CA from `make setup` in `secrets/fleet/`) | `make gateway` (agents on 8443, health on 8098) | 2s |
| Local cluster (k3d `odyssey`, CRDs, zot registry; kubeconfig in `.dev-secrets/`) | `make cluster` / `make cluster-down` | 30s |
| Connect it (registers it through the API, token → the agent's Secret; needs `make run`) | `make agent-enroll NAME=local` | 5s |
| Agent on the host (does not build) / inside the cluster (builds; applies `deploy/k3d/agent`) (pick one) | `make agent` / `make agent-image` | 2s / 2-5m |
| Fleet e2e: gateway + agent + releasefleet on a throwaway k3s (Docker) / on k3d (stop other agents first) | `make test-kube` / `make test-kube-k3d` | 90s / 45s |
| Real builds e2e on k3d (needs `make cluster`; pauses the in-cluster agent; run alone) | `make test-build` | 5-10m |
| Regenerate permissions, fleet protocol, agent deepcopy, CRD + RBAC | `make gen` (after editing `iam/permissions.json`, `proto/`, `agent/api` or `+kubebuilder` markers; needs `protoc`) | 5s |
| Regenerate console wire types | `make api-types` (after editing `api/openapi.yaml`) | 2s |
| First local run (secrets, IAMKit, seed, API) | `make dev` | 2-4m |
| Push manifest changes to local IAMKit | `make iam-sync` (`DRY_RUN=1`, `PRUNE=1`) | 5s |
| Token / end-to-end check (projects + publish, auto-restore, "Volver a esta", audit; needs API + worker) | `make token` / `make smoke` (`SMOKE_CLUSTER=fleet` with the worker on `CLUSTER=fleet`, the gateway and an enrolled agent: real whoami, the URL must answer) | 60s / 90s |
| Console dev server (needs API + IAMKit) | `make web` → http://localhost:5178 | 2s |
| Console gate (types, oxlint, vitest, build) | `make web-check` | 15s |

Local registry: zot at `zot.odyssey-registry.svc.cluster.local:5000` (HTTP, no
auth). Build pods push to that name; the node's containerd cannot resolve
cluster DNS, so `deploy/k3d/registries.yaml` mirrors it to the Service's fixed
ClusterIP. `make cluster` installs that file in an existing node with
`k3d cluster stop/start` (never a bare `docker restart`: it loses
`host.k3d.internal`, and the agent can't reach the gateway). Build pods can
reach only DNS, the agent's :8099, zot and public addresses.

Local ports: API 8095, worker health 8096, agent health 8097, gateway 8443
(health 8098), IAMKit 8085, Postgres 5438, web 5178, apps (k3d ingress) 8088 →
`http://<app>.apps.odyssey.localhost:8088`.

## File map

```
cmd/odyssey-api/          entry point → bootstrap.Run
cmd/odyssey-worker/       entry point → bootstrap.RunWorker
cmd/odyssey-gateway/      entry point → bootstrap.RunGateway (+ `ca init`)
cmd/odyssey-agent/        entry point → agent.Run (and `builder prepare|build` in a build's pod)
proto/fleet/v1/           the gRPC contract between the planes (fleet.proto + generated code)
pkg/srcarchive/           source .tar.gz rules (verify while streaming, safe extract), shared by the API and the builder
agent/                    DATA PLANE (never imports internal/): manager (agent.go), builds wiring (builds.go)
agent/api/v1alpha1/       OdysseyApp + OdysseyBuild CRD types (+ generated deepcopy)
agent/controllers/app/    OdysseyApp → Deployment/Service/Ingress; Observe = the pure phase function
agent/controllers/build/  OdysseyBuild → token Secret + Job (Job = pure builder); Observe = Job/Pod → phase
agent/builder/            what a build's pod runs: prepare (fetch, verify, Dockerfile or Railpack plan), build (buildctl push); imports only pkg/
agent/builds/             tunnel ↔ OdysseyBuilds: start/cancel/status, follow the pod's log (seq = stream<<32|line)
agent/source/             :8099 source server for build pods (per-build token) relaying FetchSource
agent/access/             the sign-in gate (`odyssey-agent access`, its own Deployment `odyssey-access` behind Traefik forwardAuth): cookie + ticket (EdDSA JWT via JWKS) verification; imports only pkg/ and the stdlib/jwt/client-go. Contract: docs/access.md
agent/desired/            desired app (proto) → namespace + OdysseyApp; status/capabilities → proto
agent/tunnel/             enrolment, mTLS tunnel to the gateway (pinned CA), renewal, reports; builds.go: build queue, final status after its log
deploy/k3d/              local cluster extras: registry/ (zot in ns odyssey-registry, Service ClusterIP 10.43.200.200), registries.yaml (k3s mirror so the node pulls the registry by its in-cluster name), agent/ (overlay of agent/install: ODYSSEY_REGISTRY + `builds-registry` NetworkPolicy)
agent/install/            kustomize: crd/ + rbac/role.yaml (generated), namespace, builds.yaml (odyssey-builds ns, source Service, NetworkPolicy), access.yaml (the gate: Deployment replicas 1, Service, Role, NetworkPolicy), deployment
internal/bootstrap/       composition root (gateway.go: the gateway), migrations list, ARCHITECTURE TESTS
internal/fleet/           clusters, tokens, desired apps; fleetca (CA), fleetpg, fleetgrpc (gateway), fleethttp
internal/server/          Fiber app: middleware, /health, /api/config, /api/v1; assistant.go: GET /api/v1/assistant + public GET /skills/odicea.tar.gz
internal/assistant/       foundation: the assistants (claude_code, codex, vscode, antigravity) — install/skill commands for the console, and Preset (OAuth client → assistant key) for `requested_via`
skills/                   the `odicea` skill (SKILL.md + references/, go:embed) → deterministic tar.gz served at /skills/odicea.tar.gz; its tool names are checked against the MCP tools
internal/<domain>/        domain types + ports.go; <domain>svc, <domain>module, adapters/<domain>{http,pg}
internal/event/           event envelope + Bus; adapters/outboxpg: transactional outbox + dispatcher
internal/workflow/        durable workflow port + Registry; adapters/workflowpg: River-backed engine
internal/blob/            object storage port (Store, TenantKey); adapters/gocloudblob: Go CDK (file://, mem://, s3://, azblob://)
internal/build/           sources (uploaded tar.gz) + builds; archive/ (API wording of pkg/srcarchive); adapters buildpg, buildhttp (status, logs), buildfake (runner)
internal/access/          the sign-in of apps protected with an Odicea login (public routes /access/authorize, /access/callback, /.well-known/odicea-access-keys): accesssvc (return-URL checks, sealed flow cookie, decision, ticket), adapters accesshttp, accessiam (IAMKit code + PKCE exchange), accessjwt (EdDSA ticket signer; the JWT library's second home). Resolves the tenant from `release.AccessLookup`, never from the request
internal/audit/           ordered subscriber of every event → hash-chained trail, GET /api/v1/audit
internal/{errx,identity,query,text,txn,pgerr,migrate,pgtest,config,httpx,permission}/  foundation
iam/permissions.json      the IAMKit permission manifest (source of internal/permission)
api/openapi.yaml          HTTP contract, exact shapes (OpenAPI 3.1, hand-written, test-enforced)
docs/api.md               HTTP contract rules; code cites its sections (§N)
scripts/                  IAMKit provisioning, token, smoke
frontend/                 web console (Vite + React + shadcn base-nova + Tailwind v4); see frontend/README.md
```

## Frontend (`frontend/`)

- Use `npx -y npm@11` for installs. npm 10.9 crashes on this dependency tree.
- The design system lives at `/design` (no login needed). Update it whenever
  you add or change a component in `src/components/odyssey/`.
- Pages compose `@/components/odyssey`. Raw shadcn `ui/*` is fine for forms
  and dialogs. Don't hard-code colours: use a `tone` or a token class.
- Copy is Spanish and plain (UX v3): App, Versión, "Volver a esta". Technical
  mode (`usePreferences().technical`) only **adds** detail.
- Wire types come from `api/openapi.yaml`: `src/lib/openapi.gen.ts` is
  generated (`make api-types`, never edit it) and `src/lib/types.ts` only
  aliases it. `npm run check` fails while the generated file is stale.
- `src/lib/permissions.ts` must stay a subset of `iam/permissions.json`. A
  vitest test enforces this.
- Base-ui `Button` with `render={<Link/>}` or `<a>` needs `nativeButton={false}`.

## Golden sample: `internal/project`

Copy this module's shape for every new domain (`source`, `build`, `release`, `fleet`, …):

| File | Pattern |
|---|---|
| `project/id.go` | typed ID: `type ID = identity.ID[tag]` with `IDName()` |
| `project/ports.go` | interfaces only: `Commands`, `Queries`, `Repository`; org right after ctx |
| `project/project.go` | entities, `Create`/`Update`/`Filter` with `Validate()`, business errors |
| `project/projectsvc/service.go` | orchestration: validate, generate IDs, `txn.Runner`, `FindForUpdate` |
| `project/adapters/projectpg/` | SQL on `txn.Q`, `pgerr.MapWith`, `const Schema`, embedded `migrations/NNN_*.up.sql` |
| `project/adapters/projecthttp/` | routes + `httpx.RequirePermissions(permission.X)`, 201+Location, full resource |
| `project/projectmodule/module.go` | assembler `New(Deps) Module`; register in `bootstrap.NewServer` + `Migrations()` |

## Rules (enforced by tests: break the build, don't weaken the test)

| Rule | Enforced by |
|---|---|
| Foundation imports no domain. Domain/svc never import SQL or transport. Svc → other domains only via ports. Only bootstrap imports `*module`. | `TestLayerDependencies` |
| `ports.go` holds interfaces only | `TestPortsAreInterfaces` |
| `*svc` packages export no const/var | `TestServicesExportNoConstants` |
| The IAMKit SDK is imported only by adapters, `httpx` and bootstrap | `TestIAMKitStaysAtTheEdge` |
| Svc methods call `.Validate()` on validatable params; `Validate` cascades to fields | `TestServicesValidateInputs` |
| No `"odyssey:..."` literals outside `internal/permission`; every permission is used; roles stay in the catalog | `TestNoPermissionLiterals`, `TestNoOrphanPermissions`, `TestRolesStayInTheCatalog` |
| `permission_gen.go` matches `iam/permissions.json` | `TestGeneratedFilesAreFresh` |
| Only bootstrap subscribes to events; event contracts (`<domain>/events.go`) import no other domain; repos join the tx (`txn.Q`); a svc that writes also publishes; every event type has a golden contract in `testdata/events/` | `TestOnlyTheCompositionRootSubscribes`, `TestEventContractsImportNoDomain`, `TestRepositoriesJoinTheTransaction`, `TestWritesPublishEvents`, `TestEventsHaveContracts`, `TestEventContracts` |
| River is imported only by `workflowpg` (and bootstrap); only bootstrap registers workflows | `TestRiverStaysInTheAdapter`, `TestOnlyTheCompositionRootRegistersWorkflows` |
| A foundation port's backends (`blob/adapters/*`, `event/adapters/*`, `workflow/adapters/*`) are opened only by bootstrap; `gocloud.dev` only inside `blob/adapters`; a domain with a service declares `Commands`, `Queries`, `Repository` and has a `<domain>module` | `TestBackendsStayInTheCompositionRoot`, `TestDomainsHaveTheGoldenShape` |
| `agent/` and `cmd/odyssey-agent` never import `internal/`; the control plane never imports `agent/` nor `k8s.io/*`/`sigs.k8s.io/*` (the planes talk only through `proto/`, which imports neither); `pkg/` (code both planes run, e.g. `srcarchive`) imports neither plane nor Kubernetes; `agent/builder` (a build pod's program) imports only `pkg/` — no Kubernetes, gRPC or protobuf — and is the only importer of Railpack; `agent/access` (the sign-in gate) meets the control plane only through the ticket contract (docs/access.md): it imports only `pkg/`, no proto/gRPC, no controller-runtime and no other `agent/` package; in `internal/`, `proto/` and protobuf only in adapters, gRPC only in adapters and bootstrap (adapter and bootstrap tests may start an agent) | `TestPlanesStayApart` |
| What an environment serves changes only in the deploy workflow's `promote` step (`releasesvc.promote` → `Repository.Promote`, the only SQL writing `current_release_id`) | `TestOnlyThePromoteStepMovesTheCurrentRelease` |
| An upload ticket is spent in one place: `ConsumeUploadTicket` / `FindUploadTicketByHash` (by token hash, cross-org) are called only by `release/releasesvc`; `Commands.RedeemUpload` only by the public route in `releasehttp` (tests aside), never by an MCP tool | `TestUploadTicketsAreSpentInOnePlace` |
| Every route is in `api/openapi.yaml` and vice versa; `x-permissions` = what the route enforces; responses match the schemas | `TestRoutesMatchTheOpenAPISpec`, `TestOperationPermissionsMatchTheRoutes`, `checked(...)` in `TestServerRoundTrip` |
| `go-github` and `golang-jwt` only in `source/adapters/sourcegithub` (and `golang-jwt` also in `access/adapters/accessjwt`); unauthenticated routes only via `RegisterPublic` of `sourcehttp` (`GET /github/setup`, `POST /webhooks/github`), `releasehttp` (`PUT /uploads/:ticket`), `accesshttp` (`GET /.well-known/odicea-access-keys`, `GET /access/authorize`, `GET /access/callback`) and `server` itself (`GET /skills/odicea.tar.gz`), each one declared per adapter, mounted only by `server`; `release` never imports `source`, and `source` names `release.Commands` only in `sourcerelease`/`sourcemodule` (its `Deployer` port); the cross-org lookups webhooks/poll need (`FindInstallationByExternal`, `ListConnectionsByRepository`, `ListConnectionsToReconcile`) are called only by `sourcesvc`, which re-checks the installation's organization | `TestGitHubStaysInItsAdapter`, `TestPublicRoutesAreDeclared`, `TestReleaseDoesNotKnowSources`, `TestCrossOrgQueriesStayChecked` |
| An environment is found without its organization in one place: `Repository.FindStateByEnvironment` only from `release/releasesvc` (`FindAccessTarget`), and `AccessLookup.FindAccessTarget` only from `access/accesssvc` — the access sign-in's tenant comes from our records, not the request | `TestEnvironmentLookupsWithoutTenantStayInOnePlace` |
| A svc's `Observe*` repo writes skip the publish rule only in observer domains (`fleet`: heartbeats; `source`: webhook deliveries, polled heads) | `TestWritesPublishEvents` (`observers`) |

When you adopt a new pattern (blob, telemetry, …), add an arch rule
**and** a fixture in `TestArchitectureRulesCatchViolations`.

Other rules:

- **Tenancy**: the organization comes only from the token. Every repo statement
  filters by `organization_id`, and another tenant's row is `NotFound`.
- **Errors**: return `errx` types, never set HTTP statuses by hand. Map DB
  errors with `pgerr`. The status table is `docs/api.md` §2.
- **Migrations**: add new files only, never edit applied ones. Each module owns
  a Postgres schema.
- **Events**: a write publishes its event inside the same `tx.Do` (outbox:
  committed together or not at all). Payload changes are additive within a
  version; bump `vN` and add a golden file otherwise. Subscribers are wired in
  bootstrap and must be idempotent (delivery is at-least-once). A handler's
  writes commit with its delivery; on error they roll back. After
  `MaxAttempts` a delivery is dead: an ordered subscriber (audit) then holds
  back that organization. Inspect with `odyssey-worker deliveries dead`, fix,
  then `odyssey-worker deliveries retry <subscriber>`.
- **Workflows**: steps run outside a transaction and may repeat (retries,
  crashes): make them idempotent. Long waits use `WaitFor`/`Poll`/`After`,
  never `time.Sleep`. Delays are precise to ~5s. One `Signal` wakes one
  `WaitFor`; extra signals stay pending. `Engine.Start` must run inside
  `tx.Do`. `OnFailure` runs in the failing transaction; if it errors, only it
  is retried (the step isn't), so make it idempotent. A `Fail(err)` cause
  unwraps to `err` (`errors.As` sees its `errx` code). A module that defines
  **and** starts workflows gets a `workflow.Deferred` engine from bootstrap
  (the engine is built after the registry is sealed).
- **Deployments** (`release`): one at a time per environment (a new one
  cancels the one in progress); each carries a `revision` the cluster uses to
  ignore older ones. A failure with another release online starts an
  `auto_rollback` deployment of it, never a loop. Releases are immutable:
  "Volver a esta" redeploys an old release.
- **Agent** (`agent/`): `status.observedRevision` says which spec revision the
  status describes. The status judges the rollout: once `ready` or `failed`
  it is final for that revision (the control plane already acted on it).
  The agent's cache sees only objects labelled managed-by `odyssey-agent`;
  read anything else (host owners, objects in the way) through
  `Reconciler.API`. A host belongs to the oldest ingress serving it. Rollouts keep the previous version serving (`maxUnavailable
  0`). Failure reasons (`image_pull`, `crash`, `unhealthy`, `config`) map to
  `IMAGE_PULL_FAILED`, `APP_CRASHED`, `HEALTH_CHECK_FAILED`, `APPLY_FAILED` in
  `release.Unready`. A CRD change is additive within `v1alpha1`; run
  `make gen` and re-apply `agent/install/crd`.
- **Assistants & skill**: `skills/odicea/` must name only real MCP tools
  (`TestSkillNamesRealTools`) and its `SKILL.md` frontmatter is `name` +
  one-line `description`; the archive is deterministic (its hash is the
  version). `ODYSSEY_MCP_CLIENTS` picks the assistants the console offers;
  `IAMKIT_MCP_CLIENT_ANTIGRAVITY_ID` lets `assistant.Preset` recognise
  Antigravity (and, with the id, puts `oauth.clientId` in its snippet; without
  it Antigravity is not offered). `Deployment.requested_via` is derived by the
  server and follows the requester: `release.ViaFor` from the event client
  (`source.pushes.requested_client` carries it for `publish_now`), `github`
  when a push has no requester; never read from a request.
- **Access** (`access` on an app: public|team|organization; docs/access.md):
  `odyssey-agent access` is the gate. Agent env: `ODYSSEY_ACCESS_ENABLED`,
  `ODYSSEY_ACCESS_VERIFY_URL`, `ODYSSEY_ACCESS_SECRET`. Gate env:
  `ODYSSEY_ACCESS_ISSUER`, `ODYSSEY_ACCESS_JWKS_URL`,
  `ODYSSEY_ACCESS_INSECURE_ISSUER`, `ODYSSEY_ACCESS_HEALTH_LISTEN`. The agent
  lists `odicea.access` in `capabilities.apis` (`fleet.CapabilityAccess`) only
  when it can enforce; a protected app never stays exposed when protection
  cannot be applied (fail closed).
  Control plane: `release.Settings` (append-only per environment; revision 0 =
  defaults, `team`) holds the access mode; `PUT .../environments/{env}/access`
  (`setEnvironmentAccess`) writes one and publishes `access.changed`, and
  the next deploy resolves the settings *at apply* into `DesiredApp.Access`
  (every settings change is a new revision). `fleetsvc.Apply` refuses
  `team|organization` on a cluster without `odicea.access`
  (`ACCESS_UNAVAILABLE`, mapped to `APPLY_FAILED`). The sign-in is
  `internal/access`: on only with `ODYSSEY_ACCESS_SIGNING_KEY[_FILE]` (Ed25519
  seed, base64; `make access-key` makes the dev one) **and**
  `IAMKIT_ACCESS_CLIENT_ID/_SECRET` (`make seed`, step 9 of
  `provision-iam.sh`); `ODYSSEY_ACCESS_PREVIOUS_KEYS` keeps old keys verifying
  during a rotation; `ODYSSEY_ACCESS_ALLOW_HTTP=true` is dev only. Settings
  report `access_login_available`; `GET .../settings`, the MCP tool `set_access`
  and the console card sit on the same service.
- **New permission**: add it to `iam/permissions.json`, run `make gen`, use
  `permission.X`, then run `make iam-sync`.
- **Endpoint change**: update `api/openapi.yaml` (shapes + `x-permissions`)
  and `docs/api.md` §11 in the same change, then `make api-types`.
  `internal/bootstrap/openapi_test.go` fails on an undocumented route, a
  permission mismatch or a response that doesn't match the schema; route new
  calls in `server_test.go` through `checked(...)`.

## Boundaries

- **Always**: `make check` before finishing (plus `make web-check` for
  `frontend/` changes). Every repo method gets a `pgtest` repository test,
  including tenant isolation.
- **Ask**: changing the arch-test rules or foundation packages, a new binary,
  a new third-party dependency, or `--prune` against a shared IAMKit.
- **Never**: commit `.env`, `secrets/` or `.dev-secrets/`. Never read the tenant
  from the path or body. Never import `github.com/Abraxas-365/iamkit` from a
  domain or service.
- Write the company name as **inEdge**.
