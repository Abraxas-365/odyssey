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
and what agents report) and `audit` (an event subscriber).
`ODYSSEY_CLUSTER` (the API and the worker must agree) picks the `Cluster`
adapter: `fake` (default, in memory; an
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
| Recipe database settings on real CloudNativePG (throwaway k3d cluster, created and deleted) | `make test-cnpg` | 3m |
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
pkg/odiceaauth/           verifiable user identity for the apps Odicea runs (verifies the gate's X-Odicea-Assertion JWT via JWKS); stdlib only
agent/                    DATA PLANE (never imports internal/): manager (agent.go), builds wiring (builds.go)
agent/api/v1alpha1/       OdysseyApp + OdysseyBuild CRD types (+ generated deepcopy)
agent/controllers/app/    OdysseyApp → Deployment/Service/Ingress; Observe = the pure phase function
agent/controllers/build/  OdysseyBuild → token Secret + Job (Job = pure builder); Observe = Job/Pod → phase
agent/builder/            what a build's pod runs: prepare (fetch, verify, Dockerfile or Railpack plan), build (buildctl push); imports only pkg/
agent/builds/             tunnel ↔ OdysseyBuilds: start/cancel/status, follow the pod's log (seq = stream<<32|line)
agent/source/             :8099 source server for build pods (per-build token) relaying FetchSource
agent/access/             the sign-in gate (`odyssey-agent access`, its own Deployment `odyssey-access` behind Traefik forwardAuth): cookie + ticket (EdDSA JWT via JWKS) verification; imports only pkg/ and the stdlib/jwt/client-go. Contract: docs/access.md
agent/secretstore/        managed secrets: opens a sealed value (pkg/secretbox) into `<app>-secrets` if its version is newer. Contract: docs/secrets.md
agent/desired/            desired app (proto) → namespace + OdysseyApp; status/capabilities → proto
agent/egress/             cluster egress policy (2d): Policy (from the proto), the per-namespace NetworkPolicies (`odicea-apps` fences app pods, `odicea-resources` the databases/caches by label, `odicea-default` every other pod, `odicea-ai` opens the AI gateway to apps with AI) and the Enforcer (keeps the policy in ConfigMap `odyssey-cluster-policy`, lists the nodes to keep them closed, resolves the AI gateways, fences every environment namespace, resyncs, reports its state). Contract: docs/egress.md
agent/tunnel/             enrolment, mTLS tunnel to the gateway (pinned CA), renewal, reports; builds.go: build queue, final status after its log
deploy/k3d/              local cluster extras: registry/ (zot in ns odyssey-registry, Service ClusterIP 10.43.200.200), registries.yaml (k3s mirror so the node pulls the registry by its in-cluster name), agent/ (overlay of agent/install: ODYSSEY_REGISTRY + `builds-registry` NetworkPolicy)
agent/install/            kustomize: crd/ + rbac/role.yaml (generated), rbac.yaml (+ odyssey-agent-environment: secrets, NetworkPolicies and the write/delete verbs of volumes, CNPG clusters, deployments and services; bound only per environment namespace, never cluster-wide), policy.yaml (VAP confining that binding), namespace, builds.yaml (odyssey-builds ns, source Service, NetworkPolicy), access.yaml (the gate: Deployment replicas 1, Service, Role, NetworkPolicy), deployment
internal/bootstrap/       composition root (gateway.go: the gateway), migrations list, ARCHITECTURE TESTS
internal/fleet/           clusters, tokens, desired apps; fleetca (CA), fleetpg, fleetgrpc (gateway), fleethttp
internal/ai/              AI for apps (2d-ai): an app's own key at the AI gateway (freerouter), made and revoked by the `ai.enable|rotate|disable` workflows (`aisvc`), sealed to the cluster as managed secret `ODICEA_AI_KEY`; the agent sets OPENAI_*/ANTHROPIC_* from it. Adapters: aipg (`ai.keys`), aifreerouter (HTTP client), aifake, airelease (↔ `release.AI`, settings carry `AI`), aihttp, aimcp (`enable_ai`). Contract: docs/ai.md
internal/server/          the HTTP handler (net/http, httpx.Router over http.ServeMux): middleware, /health, /api/config, /api/v1; assistant.go: GET /api/v1/assistant + public GET /skills/odicea.tar.gz. One HTTP stack: no Fiber/fasthttp/echo/gin (httpstack_architecture_test)
internal/assistant/       foundation: the assistants (claude_code, codex, cursor, vscode, antigravity) — install/skill commands (and one-click install links) for the console, Capabilities (what each MCP tool lets an assistant do), and Preset (OAuth client → assistant key) for `requested_via`
internal/account/         stateless: GET /api/v1/me (role, permissions, connected assistants via IAMKit with the user's own token) and DELETE /api/v1/me/assistants/{id}
skills/                   the `odicea` skill (SKILL.md + references/, go:embed) → deterministic tar.gz served at /skills/odicea.tar.gz; its tool names are checked against the MCP tools
internal/<domain>/        domain types + ports.go; <domain>svc, <domain>module, adapters/<domain>{http,pg}
internal/event/           event envelope + Bus; adapters/outboxpg: transactional outbox + dispatcher
internal/workflow/        durable workflow port + Registry; adapters/workflowpg: River-backed engine
internal/blob/            object storage port (Store, TenantKey); adapters/gocloudblob: Go CDK (file://, mem://, s3://, azblob://)
internal/build/           sources (uploaded tar.gz) + builds; archive/ (API wording of pkg/srcarchive); adapters buildpg, buildhttp (status, logs), buildfake (runner)
internal/access/          the sign-in of apps protected with an Odicea login (public routes /access/authorize, /access/callback, /.well-known/odicea-access-keys): accesssvc (return-URL checks, sealed flow cookie, decision, ticket), adapters accesshttp, accessiam (IAMKit code + PKCE exchange), accessjwt (EdDSA ticket signer; the JWT library's second home). Resolves the tenant from `release.AccessLookup`, never from the request
internal/audit/           ordered subscriber of every event → hash-chained trail, GET /api/v1/audit
internal/policy/          IT's rules (3a): project tiers + CEL rules, `policy.Ask`/`Evaluator`; policysvc, adapters policypg, policyhttp, policycel (the only cel-go importer), policyrelease/policyresource/policyai (implement the governed domains' `Policy` ports; wired only in bootstrap). Contract: docs/policy.md
internal/approval/        approvals (3b): the request an `approval` rule holds (pending → approved/rejected/expired/cancelled, 7-day expiry, one use), `approval.Hold`/`Redeem`; approvalsvc, adapters approvalpg (own schema), approvalhttp, approvalmcp (get_request, list_requests), approvalrelease/approvalresource/approvalai (implement the governed domains' `Approvals` ports; the only approval packages that know them), approvalmodule (assembler: schedule `approval.expire`, subscriptions `approval.signal_runs` and `approval.execute_approved`). Contract: docs/approvals.md
internal/inventory/       inventory + "Necesita tu atención" (3c): read-only composition of the domains' Queries (own narrow ports); inventorysvc, adapters inventoryhttp (GET /inventory + CSV, GET /attention), inventorymcp (get_attention), inventorymodule. Owns no table, no events. Contract: docs/inventory.md
internal/{actor,gate}/    actor (who acts, from the authenticated principal, on the context) and gate (the words the governed domains and the policy/approval adapters share: actions, effects, `Request`/`Verdict`, `Hold`/`Pending`/`Outcome`, the approval mark `WithApproval`, POLICY_DENIED)
internal/{errx,identity,query,text,txn,pgerr,migrate,pgtest,config,httpx,permission}/  foundation
iam/permissions.json      the IAMKit permission manifest (source of internal/permission)
api/openapi.yaml          HTTP contract, exact shapes (OpenAPI 3.1, hand-written, test-enforced)
docs/api.md               HTTP contract rules; code cites its sections (§N)
docs/secrets.md           variables y secretos, data plane: cluster key, delivery, OdysseyApp.spec.secrets, gating
docs/egress.md            salida a internet por clúster (2d): modos, allow list, NetworkPolicies, qué debe saber TI
docs/policy.md            reglas de TI (3a): niveles de app, reglas en CEL, entrada, precedencia, reglas incorporadas, fallo cerrado, dónde se aplican
docs/approvals.md         aprobaciones (3b): la solicitud, quién decide, acciones directas (202, se repiten como el solicitante), paso authorize de los despliegues (waiting_approval), vencimiento, eventos, MCP, consola
docs/scan.md              escaneo de imágenes (3c): Job Trivy en el clúster, capacidad odicea.scan, build.scan, authorize_scan (espera 10 min), qué no se escanea
docs/inventory.md         inventario y "Necesita tu atención" (3c): filas por app/entorno, CSV, quién ve qué, degradación, límites
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
- The assistant (UX-5): "connected" comes only from `GET /me`, read in
  `features/assistant/state.ts` (`assistantState`); an unanswered `/me` is
  "unknown", never "not connected". The assistant is the header
  `AssistantChip`, not a tab nor a notification. TI pages live under
  `/administracion/*` (`adminSections`); the old addresses only redirect.
  `src/pages/app-pages.test.ts` enforces all three.

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
| `agent/` and `cmd/odyssey-agent` never import `internal/`; the control plane never imports `agent/` nor `k8s.io/*`/`sigs.k8s.io/*` (the planes talk only through `proto/`, which imports neither); `pkg/` (code both planes run, e.g. `srcarchive`) imports neither plane nor Kubernetes; `agent/builder` (a build pod's program) imports only `pkg/` — no Kubernetes, gRPC or protobuf — and is the only importer of Railpack; `agent/access` (the sign-in gate) meets the control plane only through the ticket contract (docs/access.md): it imports only `pkg/`, no proto/gRPC, no controller-runtime and no other `agent/` package; `pkg/odiceaauth` (imported by the apps Odicea runs) takes only the standard library (its tests may add only the package itself); in `internal/`, `proto/` and protobuf only in adapters, gRPC only in adapters and bootstrap (adapter and bootstrap tests may start an agent) | `TestPlanesStayApart` |
| What an environment serves changes only in the deploy workflow's `promote` step (`releasesvc.promote` → `Repository.Promote`, the only SQL writing `current_release_id`) | `TestOnlyThePromoteStepMovesTheCurrentRelease` |
| Deleting a resource and connecting one serialise on the settings lock: `resourcesvc` `Delete` → `WithConnectionsLocked` → `resourcerelease` → `releasesvc.WithResourceUseLocked`, and it, `ConnectResource` and `DisconnectResource` call `lockSettings` | `TestResourceUseIsSerialised` |
| A resource kind is a contract registered only in `pkg/resourcekind` (words, variables, mounted, holds data): the control plane (`internal/`) never branches on a kind (`case resourcekind.X`, `== / != resourcekind.X`); it reads the contract (`resourcekind.Of`). The agent's builtin engines are kind-specific by design, but its resource controller tells kinds apart only in the engine registry (`agent/controllers/resource/engines.go`: apply, remove, connection): no kind constant elsewhere in that package | `TestNoBranchOnResourceKind`, `TestResourceEnginesAreTheOnlyKindSwitch` (+ fixtures `TestEngineRegistryRuleCatchesViolations`) |
| The agent names the External Secrets Operator's API (`external-secrets.io`, `ClusterSecretStore`) only in `agent/externalsecret`: apps' secrets and existing resources speak one version of it and put values into its templates one safe way (`Literal`, `Escaped`) | `TestOnlyExternalSecretNamesESO` (+ fixtures `TestExternalSecretsRuleCatchesViolations`) |
| Organization recipes (engine `existing`, docs/catalog.md): IT registers a resource the company already has (`POST /catalog/recipes`, `catalog:manage`) with an inactive offering, and publishes later versions (`…/versions`, not retroactive); a connection that reads a secret store also needs `secrets:external` and a store an active cluster announces (`catalog.Fleet`, adapter `catalogfleet` over `fleet.Queries`, the only way catalog reaches the fleet); a `May*` flag (the caller holds a permission) is set only by an inbound adapter, from a permission check on the token (never a constant); events name the recipe@version and stores, never a spec or connection; the connection and the stores it reads are shown only to `catalog:manage`; a resource of such a recipe is `existing` (no size; deleting it disconnects it and says so, its data is the company's); the kinds' contracts reach the console only through `GET /catalog/kinds` (`catalog.Kinds`, built from `resourcekind.Contracts`): the console names no contract variable and builds a connection only in `src/lib/connection.ts` | `TestOnlyHTTPGrantsPermissions` (+ fixtures `TestPermissionFlagRuleCatchesViolations`), `TestEventsCarryNoResourceSpec` (+ fixtures `TestEventSpecRuleCatchesViolations`), `TestRegisterRecipe`, `TestRegisterRecipeStores`, `TestPublishVersion`, `TestEntryRedacted`, `TestSecretStores`, `TestAddOfferingAndTenantIsolation`, `TestRecipesOfTheOrganizationOverHTTP`, `TestDeletingAnExistingResourceSaysItIsDisconnected`, `TestKinds`, vitest `stage 2: resources the company already has` (`app-pages.test.ts`, with fixtures `the rules catch violations`), `connection.test.ts`, `catalog.test.tsx`, `resources-panel.test.tsx` |
| Resource catalog (docs/catalog.md): a recipe version is immutable and written only in `internal/catalog/recipes.go` (released versions are frozen in `testdata/versions.json`; an update adds a version; an organization's own versions, `org-…` in `catalog.recipe_versions`, are insert-only and read outside the catalog only through `catalog.Queries.Version`, never `catalog.Factory`); offerings are IT's, per organization, seeded lazily on the first read; `catalog` imports no domain but the `project` kernel, and others ask it through its ports; a resource is pinned to `recipe@version` with `params` as its truth (`storage_gi` kept in step by a CHECK); what can be created in a tier on a cluster is decided in one place, `catalog.Entry.Usable` (both `Available`/`Resolve` and the MCP's view), and `list_catalog`'s instant / needs_approval / denied is `resource.Queries.Offered`, judged by the same `resourcesvc.judge` as `Create` (with the connection, as `create_resource`'s default; writing nothing); an offering limited to teams (IAMKit groups) counts only the teams of who asks — on an approval, who asked, read again — and only for creating (`ResolveChange`/`Stranded` ignore teams); with no directory it is offered to nobody; only `release/adapters/releaseiam` calls IAMKit's `MachineToken` (others take its `Token`) | `TestRecipeVersionsAreWrittenOnlyInTheFactory`, `TestOnlyTheCatalogReadsTheFactoryAlone` (+ fixtures `TestFactoryLookupRuleCatchesViolations`), `TestPublishedVersionsNeverChange`, `TestVersionKnowsTheOrganizationsOwnRecipes`, `TestReleasedVersionsNeverChange`, `TestFactoryVersionsAreSound`, `TestFirstReadSeedsTheFactoryOfferingsOnce`, `TestUpdateOffering`, `TestCatalogOverHTTP`, `TestMigrationPinsEveryResourceToItsBasicRecipe`, `TestOfferedJudgesEachRecipe`, `TestOfferedLeavesOutWhatCannotBeCreated`, `TestOfferedJudgesAsCreateDoes`, `TestMCPCatalog`, `TestOfferingsForTeams`, `TestWithoutADirectoryOfferingsForTeamsAreNobodys`, `TestOfferedIsTheAskersCatalog`, `TestOfferingsForTeamsOverHTTP`, `TestOneOwnerOfTheServiceAccountSecret` (+ fixtures `TestMachineTokenRuleCatchesViolations`) |
| An app's AI key exists in clear only inside `aisvc.createKey` (no other `aisvc` function reads a provider key's `Secret`); the AI gateway (`CreateKey`/`RevokeKey`/`ListKeys`) is never called from `Enable`/`Rotate`/`Disable`/`Get`/`view`, only from workflow steps; `Enable`, `Rotate`, `Disable` and the `apply` step take `LockEnvironment` | `TestAIKeyStaysInCreateKey`, `TestAIGatewayIsOnlyCalledFromWorkflowSteps`, `TestAIChangesAreSerialised` |
| An upload ticket is spent in one place: `ConsumeUploadTicket` / `FindUploadTicketByHash` (by token hash, cross-org) are called only by `release/releasesvc`; `Commands.RedeemUpload` only by the public route in `releasehttp` (tests aside), never by an MCP tool | `TestUploadTicketsAreSpentInOnePlace` |
| Every route is in `api/openapi.yaml` and vice versa; `x-permissions` = what the route enforces; responses match the schemas | `TestRoutesMatchTheOpenAPISpec`, `TestOperationPermissionsMatchTheRoutes`, `checked(...)` in `TestServerRoundTrip` |
| `go-github` and `golang-jwt` only in `source/adapters/sourcegithub` (and `golang-jwt` also in `access/adapters/accessjwt`); unauthenticated routes only via `RegisterPublic` of `sourcehttp` (`GET /github/setup`, `POST /webhooks/github`), `releasehttp` (`PUT /uploads/:ticket`), `accesshttp` (`GET /.well-known/odicea-access-keys`, `GET /access/authorize`, `GET /access/callback`) and `server` itself (`GET /skills/odicea.tar.gz`), each one declared per adapter, mounted only by `server`; `release` never imports `source`, and `source` names `release.Commands` only in `sourcerelease`/`sourcemodule` (its `Deployer` port); the cross-org lookups webhooks/poll need (`FindInstallationByExternal`, `ListConnectionsByRepository`, `ListConnectionsToReconcile`) are called only by `sourcesvc`, which re-checks the installation's organization | `TestGitHubStaysInItsAdapter`, `TestPublicRoutesAreDeclared`, `TestReleaseDoesNotKnowSources`, `TestCrossOrgQueriesStayChecked` |
| An environment is found without its organization in one place: `Repository.FindStateByEnvironment` only from `release/releasesvc` (`FindAccessTarget`), and `AccessLookup.FindAccessTarget` only from `access/accesssvc` — the access sign-in's tenant comes from our records, not the request | `TestEnvironmentLookupsWithoutTenantStayInOnePlace` |
| A svc's `Observe*` repo writes skip the publish rule only in observer domains (`fleet`: heartbeats; `source`: webhook deliveries, polled heads) | `TestWritesPublishEvents` (`observers`) |
| «Lo que usa esta app» (2e) is a read model: `release.Queries.Uses` assembles it with the pure `release.NewUses` from release's own settings and ports (`Resources.Statuses`, `AIStatus`, `Cluster.Egress`); `release` never imports `ai` — only the `release/adapters/releaseai` adapter does. `Uses.Attention` is a closed list of `release.AttentionKind` (`secret_missing`, `secrets_unavailable`, `resource_not_ready`, `resource_failed`, `ai_error`, `ai_pending`, `config_pending`, `config_failed`, `egress_not_applied`), each with a `Who` (`owner`/`it`/`wait`) and a Spanish message; each source (secrets, resources, AI, egress) degrades alone — its section is nil and listed in `Uses.Unavailable`, only settings/state errors fail the call — and `Resources.Statuses` is asked only for the connected names; MCP `get_app` adds `uses` and `needs_attention` = last deploy failed or any attention with `who` ≠ `wait`, and never fails because of Uses | `TestReleaseDoesNotKnowAI` (+ fixtures in `TestReleaseAIRuleCatchesViolations`), `TestUsesAttention`, `TestUsesDegradesPerSource`, `TestUses`, `TestMCPToolsContract` |
| IT's rules (3a, docs/policy.md): the governed actions (`deploy`, `rollback`, `set_access`, `connect_resource`, `delete_resource`, `enable_ai`, `set_services`) ask `policy` only through the consumer's own `Policy` port and its one `authorize` seam (`releasesvc`, `resourcesvc`, `aisvc`, `projectsvc`; the approval branch of that seam is 3b's, below); `release`, `resource`, `ai` and `project` never import `policy` — only `policy/adapters/policy{release,resource,ai,project}` bridge them (wired in bootstrap); `cel-go` is imported only by `policy/adapters/policycel`; a rule that cannot be evaluated fails closed at its own strength (deny denies, approval asks, allow is ignored), never as allow; a no-op is not judged, and a change that appears only under the lock is judged before it is applied; the actor is the authenticated principal (`actor.From(ctx)`: REST claims, MCP connection permissions), never a request field; built-in rules can be switched off, never edited or deleted | `TestGovernedDomainsDoNotKnowPolicy`, `TestCELIsOnlyInItsAdapter`, `TestPolicyAdaptersAreWiredOnlyInBootstrap` (+ fixtures in `TestPolicyRulesCatchViolations`), `TestPrecedenceDenyThenApprovalThenAllow`, `TestARuleThatCannotBeEvaluatedFailsClosedAtItsOwnStrength`, `TestSetAccessJudgesAChangeThatAppearedUnderTheLock`, `TestBuiltinsCanBeSwitchedOffNotChangedNorDeleted`, `TestITRulesGovernTheActions`, `TestMCPToolsAnswerITsRules` |
| Approvals (3b, docs/approvals.md): an `approval` verdict is a typed outcome (`gate.Outcome`/`Pending`: HTTP 202 / MCP `pending_approval`), never an error; the governed domains open the request through their own `Approvals` port (`release`, `resource`, `ai`, `project`, `source` never import `approval`; `approval` knows them only in `approval/adapters/approval{release,resource,ai,project}` and `approvalmodule` — the root `project` package is a shared kernel (IDs) any approval package may name, never `project/...`; its adapters are wired only in bootstrap); the request is opened before a transaction, except the deployment's `authorize` step (atomic with `waiting_approval`); deciding needs ANY approver permission read from the token (`actor`), never the requester (403 `APPROVAL_SELF`), only a pending request (409 `APPROVAL_DECIDED`), and is tenant-scoped; an approved direct action re-runs AS THE REQUESTER (their permission snapshot) with the approval mark, and the mark skips IT's rules only after the service redeems the approval (approved, unused, same org/action/place/payload; consumed atomically); a failed re-run is recorded (`execution_error`) and published, never retried; a deployment is judged by the workflow's first step `authorize` as its requester (a push is the system actor), waits as `waiting_approval`, is cancelled on reject/expire/cancel, and a newer deployment cancels the waiting one's request; config deployments and auto-rollback are not judged; MCP `rollback` in production and `set_access public` need the user's yes (`mcpx.Confirm`: elicitation or `confirm: true`) | `TestGovernedDomainsDoNotKnowApproval`, `TestApprovalDoesNotKnowTheGovernedDomains`, `TestApprovalAdaptersAreWiredOnlyInBootstrap`, `TestApprovalsAreNotHeldInsideATransaction` (+ fixtures `TestApprovalRulesCatchViolations`, `TestApprovalHoldRuleCatchesViolations`), `TestEventContracts` (approval goldens), `TestApproveRejectRules`, `TestCancelIsTheRequestersOnly`, `TestRedeemNeedsTheSameActorActionPlaceAndPayloadAndWorksOnce`, `TestInsertFindAndTenantIsolation`, `TestApprovalRoutes`, `TestApprovalsEndToEnd`, `TestMCPToolsAnswerITsRules`, `TestMCPConfirmsWhatIsHardToUndo` |
| Image scan (3c, docs/scan.md): the agent scans every image it builds in a Trivy Job (capability `odicea.scan`, outcome read from the Job, never owned by the build) and the tunnel reports it (`ScanReport`); the build never waits for the scan — the `build.scan` workflow records `build.Scan` on it (a succeeded build always ends up with one, `Error` says why it could not be done) and publishes `build.scanned`; the deployment's `authorize_scan` step (after the build) polls for it up to `release.ScanWait` and judges IT's rules with `scan.*`, same verdicts and approval machinery as `authorize` (a rule that already asked is not asked again); an unknown scan (error, timeout, cluster without the capability, another digest, `deploy_image`) is `scan.known = false`, never clean (`scan.checked` tells "looked at, nothing usable" from "not judged yet"); a builder controls the image and can make its scan fail, so `production_critical_vulnerabilities` alone is bypassable and the built-in `production_requires_scan` (off by default, `scan.checked && !scan.known`, approval) closes it; the cluster runs ONE scan Job at a time (suspended Jobs released FIFO by `ScanQueue`, the deadline counts only the run), the Trivy DB is downloaded in init containers WITHOUT `--insecure` (Trivy has one global TLS switch), the report is streamed with a hard cap, and a scan that cannot start never blocks the build's cleanup | `TestScanGatesAProductionDeployment`, `TestAuthorizeScanFeedsTheScanAndDenies`, `TestAuthorizeScanMarksTheScanChecked`, `TestAuthorizeScanAllowsACleanImage`, `TestAuthorizeScanJudgesAnUnknownScanAsUnknown`, `TestAuthorizeScanWaitsForTheScan`, `TestAuthorizeScanHoldsAtItsOwnStepAndParksOnItsOwnSignal`, `TestAuthorizeScanDoesNotAskAgainWhatAuthorizeAsked`, `TestApprovalScanStepActsOnItsOwnDecision`, `TestAuthorizeScanSkipsWhatWasNotBuiltOrJudged`, `TestANewBuiltinReachesAnExistingOrganizationDisabled`, `TestEventContracts` (build.scanned golden), `TestScanJob`, `TestWarmupJob`, `TestObserveScan`, `TestReconcileStartsTheScanOnceBuildSucceeds`, `TestNoScanWhenOffOrTheBuildDidNotSucceed`, `TestScanQueueRunsOneAtATimeOldestFirst`, `TestScanQueueFailedJobFreesTheSlotAndOffDoesNothing`, `TestScanQueueWarmsTheDatabase`, `TestFinishedCleansUpWhenTheScanCannotStart`, `TestScanGivenUpIsRecordedOnTheBuild`, `TestSummarizeStreamsALargeReportAndCapsIt`, `TestScans`, `TestReportScans`, `TestListScansAreBatchedAndDegrade` |
| Account (`/me`, UX-5): the user's own bearer token is used in one place — `httpx.BearerToken` only in `account/adapters/accounthttp`, IAMKit's `ListConnectedApps`/`DisconnectApp` only in `account/adapters/accountiam`; `account` is stateless (IAMKit holds the connections) and an IAMKit failure is `assistant: null`, never "no conectado"; every MCP tool is worded in one `assistant.Capabilities` sentence (what `/me` says an assistant can/cannot do); in the console, "connected" is read from `/me` only in `features/assistant/state.ts`, the assistant is the header chip (not a tab nor a notification) and TI pages live under `/administracion` | `TestTheUsersTokenLeavesInOnePlace` (+ fixtures `TestBearerRulesCatchViolations`), `TestEveryMCPToolHasACapability`, `TestMe`, `TestMeWithoutIAMKitSaysNothing`, vitest `UX v4 §6.1: the assistant` (`app-pages.test.ts`), `console-layout.test.ts` |
| Provenance and the company log (UX-6): every event names the channel it came through — `event.Meta.Client` is set only at the edges (`event.WithMeta`/`WithCause` only in `httpx`, `mcpx`, `workflowpg` (a run keeps its starter's client in `workflow.runs.client`), `outboxpg`, `fleetgrpc` (`platform`), `sourcesvc` (webhook and poll: `github`), `releasesvc` (automatic restore: `platform`; upload ticket)); a REST token without a client is `api`, never empty — empty means "not recorded" and is shown as such, never guessed; the channel vocabulary (`console`, `github`, `platform`, `assistant:<key>`) is `internal/channel` (foundation; `release.ViaFor` and `audit` both use `channel.Of`, the trail's filter `channel.Filter` agrees with it) and no other package spells `"assistant:"` or `"mcp:"` (`assistant.MCPClientPrefix`); the trail's `channel` and `actor_name` are derived on read and never hashed; names come from the IAMKit directory (the same `releaseiam.Directory`, now also in the API) and a directory failure leaves names empty, never an error; in the console, the channel's words and the `assistant:` prefix are spelled only in `features/apps/attribution.ts` (no surface reads a raw `client`), every `/audit` read is in `lib/audit.ts` (asked only with `odyssey:audit:read`), and «Registro íntegro» is said only by `sealOf` from `/audit/verify` | `TestProvenanceIsSetAtTheEdges` (+ fixtures `TestProvenanceRulesCatchViolations`), `TestOf`, `TestFilterAgreesWithOf`, `TestViaFor`, `TestChannelsProjectsAndPeople`, `TestADirectoryThatFailsLeavesNamesEmpty`, `TestStepsKeepTheirRunsClient`, `TestCompanyLog`, vitest `UX v4 §5.5: the company log` (`app-pages.test.ts`), `registro.test.tsx` |
| Visual (UX-7): Inter for titles and text — `--font-heading` is the body face; JetBrains Mono only for code, URLs, slugs, IDs and SHAs, never for a title (`h1`–`h6`, `font-heading`) nor a figure (`text-stat/title/display/section`; numbers use `tabular-nums`); inside a panel lists sit flush and are split by separators — pages and features never box a list (`divide-y` with `border`) nor nest a panel in a panel | vitest `UX v4 §5.9: visual` (`app-pages.test.ts`, with fixtures `the rules catch violations`) |
| Inventory and attention (3c, docs/inventory.md): `internal/inventory` is read-only — it composes other domains' `Queries` through its own narrow ports (their root packages only, never a svc/adapter/repository), owns no table, opens no transaction, publishes and subscribes to nothing, and no domain imports it (only bootstrap); reads are batched (one query per kind of data, never one per app), per-environment `Uses` reads are bounded (`MaxApps`, `UsesWorkers`); every source degrades on its own into `Unavailable` — an empty list is "Todo en orden" only with nothing unavailable (a `cluster=` filter with the clusters unreadable says so instead of answering empty); `list_apps.needs_attention` is scoped like `/attention` (non-TI see only their own apps); the tenant is the token's; CSV cells that a spreadsheet could run as a formula are neutralized | `TestInventoryIsReadOnly`, `TestNoDomainKnowsTheInventory` (+ fixtures `TestInventoryRulesCatchViolations`), `TestInventoryAndAttention`, `TestAttentionForIT`, `TestClusterItemsAndDegradation`, `TestClusterFilterSaysWhenClustersAreUnavailable`, `TestNeedingAttentionIsScopedLikeAttention`, `TestAppsAboveTheCapAreTruncated`, `TestListAppsFlagsNeedsAttentionInOneBatch` |
| Multi-service builds are cancelled together (MS-3a): a deployment from source has one build per service, so in `release/releasesvc` only `cancelBuilds` (over `Deployment.BuildIDs`) calls `Builder.Cancel` — superseding, failing, archiving or rejecting a deployment stops all of its builds, never just `*d.Build` | `TestDeploymentBuildsAreCancelledTogether` (+ fixtures `TestBuildCancelRuleCatchesViolations`), `TestDeployServicesFromSource` |
| Reuse by content digest (MS-3b, docs/api.md §11 «Reuse»): whether a build reuses an image is decided in one place per step — candidates listed only by `buildsvc.reuse` (none on `rebuild`), the origin read only by `buildsvc.origin` and written only by `buildsvc.settle`, a scan copied only by `buildsvc.copiedScan` (from `scan`; digest-checked, ≤ `ScanReuseWithin`), a candidate kept only by `DesiredBuild.ApplyReuse` in `fleetsvc.DispatchBuild` (same builder identity and repository); `go-containerregistry` only in `agent/builder` | `TestReuseIsDecidedInOnePlace` (+ fixtures `TestReuseRuleCatchesViolations`), `TestReuseUnchangedServices` |
| Topology under IT's rules (MS-3c, docs/policy.md «Services»): a version is the whole topology — a rollback deploys the target's own release and the platform's restore the serving one, so services a version did not have are retired (the agent removes what the spec drops); IT's rules see the services an action brings in one vocabulary (`gate.Request.Services/Changed/TopologyChanged` → CEL `services`, `changed`, `topology_changed`), built only by `release.Compare`/`CompareLayout`/`GateServices` and `project.ServicesRequest`/`GateServices`, so an app of one service reads exactly like one of several; a deployment from source is judged first on its layout and, once built, on its version (`changed` then names code changes too); `set_services` is a governed action of `project` (the whole app: an approval with empty `environment_id`, re-run by `approvalproject`), judged only on a change and re-judged if the list moved before the lock; the built-in `production_services_approval` is born off | `TestRestoreTheWholeTopology`, `TestCompareNamesWhatChanges`, `TestCompareLayoutKnowsOnlyTheLayout`, `TestJudgedReadsOneServiceLikeSeveral`, `TestMatchesServices`, `TestSetServicesIsJudgedByITsRules`, `TestSetServicesDeniedChangesNothing`, `TestSetServicesHeldAndRedeemed`, `TestSetServicesFailsClosedWithoutRules`, `TestSetServicesRejudgesAChangeMadeMeanwhile`, `TestAppWideRequestHasNoEnvironment`, `TestGovernedDomainsDoNotKnowPolicy`/`TestGovernedDomainsDoNotKnowApproval` (+ fixtures with `project/projectsvc`) |
| Multi-service apps (MS-1, docs/api.md §11 «Servicios»): an app's services (`project.services`) have one writer — only `project/projectsvc` calls `Repository.ReplaceServices` (validated, inside its transaction, publishing `project.services_changed`); every other domain reads them through `project.Queries.Services`; a version's snapshot (`release.release_services`, `Release.Services`, `DesiredApp.Services`) and a deployment's (`release.deployment_builds`, `Deployment.Builds`) are made only in `release/releasesvc` through `release.ServicesOf` / `ImageServices` / `BuildsFor`, holding the project first so a concurrent change lands before or after, never half; a list other than one web service at `/` with all of the code is refused with `SERVICES_UNAVAILABLE` unless every cluster of the app's environments announces `odicea.services` (`fleet.Dispatch.CanRunServices`). MS-3a: a deployment from source builds each service from its own folder (`build.Commands.Derive`), all at once; its version exists only if every build succeeds (all or nothing: one failure cancels the rest), every build is cancelled together (`cancelBuilds`), the scan gate judges all the images (findings added up, unknown if any is — and then also on the known findings, the stricter verdict standing, so an unscanned service never hides another's criticals); a folder cut from an admitted upload is stored even over the quota; an image is only for apps of one service (`IMAGE_NEEDS_ONE_SERVICE`) | `TestServicesHaveOneWriter` (+ fixtures `TestServicesRuleCatchesViolations`), `TestServicesOverHTTP`, `TestDeployServicesFromSource`, `TestAuthorizeScanAddsUpTheServicesImages`, `TestAuthorizeScanDoesNotHideFindingsBehindAnUnscannedService`, `TestDeriveIsAdmittedWithItsUpload`, `TestServices`, `TestMigrationGivesEveryAppItsWebService`, `TestMigrationGivesEveryVersionItsWebService`, `TestSetServicesNeedsClustersThatRunThem` |
| Multi-service apps in the agent (MS-2, docs/access.md «Apps of several services», docs/egress.md): one `OdysseyApp` with `spec.services[]`; each service is a Deployment (and, for web, a Service + Ingress) named `<app>` for the service at `/` and `<app>-<service>` otherwise; Deployments and Services select by `odyssey.dev/workload` — `odyssey.dev/app` is membership (fence, secrets, observation), never a selector, except in `mutateService` while an app from before services migrates (Service on the app's label until the new workload is ready, then narrowed, the old Deployment kept for `legacyDrain` and then deleted); protected routes chain strip headers → forwardAuth → stripPrefix (the gate sees the URL the person opened); internal services and workers roll out first; one failing service fails the version; dropped services are removed only once the app is ready | `TestWorkloadsSelectByWorkload` (+ fixtures `TestWorkloadSelectorRuleCatchesViolations`), `agent/controllers/app/services_test.go`, `agent/desired/services_test.go`, `TestApplyRefusesServicesWithoutTheCapability`, `TestKubeServices` (k3d: no-downtime migration, `/api` vs `/apix`, sign-in returns to the opened URL, a dropped service goes, CRD rules) |
| Detecting services (MS-5, docs/api.md §11, after «Servicios»): a proposal, never a write — `pkg/detect` (standard library only, file names only, never contents) is imported only by the root `project` package, so the console, GitHub and the MCP get one proposal through `project.Detect`, shaped as the app's services (it passes `SetServices.Validate`) and saved only by a person or assistant through `PUT …/services`; `POST /services/detect`, `GET /github/…/repositories/:r/services` and the MCP `detect_services` (MS-7: a folder's `paths` or a `repository`) need `projects:write` and write nothing; the git tree comes from `GitProvider.Tree` (blobs only, capped at `project.MaxDetectPaths`, `truncated` said); a monorepo connects with `root` `""` and a connection's `root` composes with the services' folders | `TestServicesAreProposedInOnePlace` (+ fixtures `TestDetectRuleCatchesViolations`), `TestPlanesStayApart` (`pkg/detect` stdlib-only, fixtures in `TestPlaneRulesCatchViolations`), `pkg/detect` table tests, `TestDetectProposesValidServices`, `TestDetectServicesValidate`, `TestGitHubMonorepo`, `TestDetectServicesRoute`, `TestDetectServicesFromAFolder`, `TestDetectServicesFromARepository` |
| Services in the console (MS-6a, D3): an app of one service reads exactly as before — the word «servicio» reaches the screen only from `features/apps/services.ts`, `services-panel.tsx`, `service-states.tsx` and `service-scope.tsx` (plus two unrelated meanings: IT's rule vocabulary in `features/policy/rules.ts`, «Servicio externo» in `components/odyssey/resources.tsx`, an outside provider in `lib/api.ts`), and each of those components asks `isMulti` before drawing; what a version changed (`Deployment.changed`) is computed on read in `releasesvc.fillChanged` from the stored releases (one `ReleaseServices` batch per call, never per row, never stored); the per-service state reads `Cluster.Status` (the agent's last report), never the cluster live; MS-6b: detection before the first publication (create from GitHub, connect, upload) is offered only to an app still of one piece, through `ProposedParts`/`saveProposal` in `services-panel.tsx` (saved before connecting/publishing; a 202 or a failure stops the publication), and a setting's scope is chosen only with `ScopePicker` (`service-scope.tsx`, silent for one part or a cluster that cannot scope) | vitest `MS-6: services (D3)` (`app-pages.test.ts`, with fixtures `the rule catches violations`), `service-states.test.tsx`, `proposal.test.tsx`, `service-scope.test.tsx`, `scope.test.ts`, `TestChangedOnListIsOneBatch`, `TestChangedOnFindAndCurrent`, `TestServiceStates` (`releasesvc`), `TestReleaseServices`, `TestListServingServices` (pgtest, tenant isolation), `TestServiceStatesOverHTTP` |

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
- **Egress** (`fleet.Egress` per cluster, docs/egress.md): `PUT/GET
  /api/v1/clusters/{id}/egress`; mode `internet` (default) or `restricted` plus
  an `allow` list of CIDRs (`0.0.0.0/0` refused: it would reopen the private
  ranges). Each change bumps a generation and publishes
  `cluster.egress_changed`; the gateway pushes `ClusterPolicy` (first thing after
  `Hello`, and on the `<org> <cluster> policy` feed signal) and the agent
  answers `PolicyAck`, an observation (`ObservePolicy`, no event). The agent
  (`agent/egress`) lists `odicea.egress` and fences every environment namespace
  with the `odicea-apps`, `odicea-resources`, `odicea-default` (and, for apps
  with AI, `odicea-ai`) NetworkPolicies, before the
  namespace's first pod; no policy received = `internet`. Writing
  NetworkPolicies is in the `odyssey-agent-environment` role only (never
  cluster-wide); apps must keep reaching `odyssey-access:8099`. The CNI must
  enforce NetworkPolicy. The `internet` rule excepts the reserved ranges and
  every node's address; `PolicyAck.warnings` flags allow entries that include a
  node (shown in the egress view). Pods of resources carry `odyssey.dev/resource`
  (CNPG: `spec.inheritedMetadata`), pods of apps with AI `odyssey.dev/ai`.
- **Assistants & skill**: `skills/odicea/` must name only real MCP tools
  (`TestSkillNamesRealTools`) and its `SKILL.md` frontmatter is `name` +
  one-line `description`; the archive is deterministic (its hash is the
  version). `ODYSSEY_MCP_CLIENTS` picks the assistants the console offers;
  `IAMKIT_MCP_CLIENT_CURSOR_ID` / `IAMKIT_MCP_CLIENT_ANTIGRAVITY_ID`
  (`config.MCP.TemplateClients`, `assistant.Registered`) let
  `assistant.Preset` recognise those template clients (and put the id in
  their config; without it they are not offered). Every MCP tool is worded in
  one `assistant.Capabilities` sentence (`TestEveryMCPToolHasACapability`):
  `/me` tells a user what their assistant can and cannot do from it. `account`
  (`/me`) keeps nothing: connections are IAMKit's, read with the user's own
  token (`accountiam`), and an IAMKit failure is `assistant: null`, never "no
  conectado". `Deployment.requested_via` is derived by the
  server and follows the requester: `release.ViaFor` from the event client
  (`source.pushes.requested_client` carries it for `publish_now`), `github`
  when a push has no requester; never read from a request.
- **Access** (`access` on an app: public|team|organization; docs/access.md):
  `odyssey-agent access` is the gate. Agent env: `ODYSSEY_ACCESS_ENABLED`,
  `ODYSSEY_ACCESS_VERIFY_URL`, `ODYSSEY_ACCESS_SECRET`,
  `ODYSSEY_ACCESS_SIGNING_SECRET`, `ODYSSEY_ACCESS_JWKS_URL_INTERNAL`. Gate env:
  `ODYSSEY_ACCESS_ISSUER`, `ODYSSEY_ACCESS_JWKS_URL`,
  `ODYSSEY_ACCESS_INSECURE_ISSUER`, `ODYSSEY_ACCESS_HEALTH_LISTEN`,
  `ODYSSEY_ACCESS_KEYS_LISTEN`, `ODYSSEY_ACCESS_SIGNING_SECRET` (2c: the gate
  signs `X-Odicea-Assertion`; JWKS on :8099; docs/access.md). The agent
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
