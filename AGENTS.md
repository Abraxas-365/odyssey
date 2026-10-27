# AGENTS.md — Odyssey

Odyssey is a governed, enterprise Vercel-like platform. It runs on the customer's
own infra (on-prem/AWS/GCP/Azure). Business users ("vibecoders") connect a repo
and deploy, with security set by IT. Odyssey offers one MCP for coding agents
and uses IAMKit for platform and app auth.

This repo holds the **control-plane API** (`cmd/odyssey-api`), the **worker**
(`cmd/odyssey-worker`: outbox delivery, workflows, scheduled tasks) and the
**web console** (`frontend/`). Domain modules today: `project` (the golden
sample) and `audit` (an event subscriber). The full
design (binaries, domains, build order) lives outside the repo, in
`~/.rness/plans/Personal/odicea-architecture.md`. The UI design is in
`odicea-mockup.html` and `odicea-product-ux-v3.md` there.

## Commands

| Task | Command | ~Time |
|---|---|---|
| Build | `make build` (→ `bin/odyssey-api`, `bin/odyssey-worker`) | 5s |
| Unit + arch tests (DB tests skip) | `make test` | 5s |
| All tests vs compose Postgres | `make test-db` (`PKG=./internal/project/...`) | 10s |
| Local CI gate (gofmt, vet, test) | `make check` (CI also runs `-race` vs Postgres: `.github/workflows/ci.yml`) | 10s |
| Run the worker on the host | `make worker` (health on 8096) | 2s |
| Regenerate permissions | `make gen` (after editing `iam/permissions.json`) | 1s |
| Regenerate console wire types | `make api-types` (after editing `api/openapi.yaml`) | 2s |
| First local run (secrets, IAMKit, seed, API) | `make dev` | 2-4m |
| Push manifest changes to local IAMKit | `make iam-sync` (`DRY_RUN=1`, `PRUNE=1`) | 5s |
| Token / end-to-end check | `make token` / `make smoke` | 5s |
| Console dev server (needs API + IAMKit) | `make web` → http://localhost:5178 | 2s |
| Console gate (types, oxlint, vitest, build) | `make web-check` | 15s |

Local ports: API 8095, worker health 8096, IAMKit 8085, Postgres 5438, web 5178.

## File map

```
cmd/odyssey-api/          entry point → bootstrap.Run
cmd/odyssey-worker/       entry point → bootstrap.RunWorker
internal/bootstrap/       composition root, migrations list, ARCHITECTURE TESTS
internal/server/          Fiber app: middleware, /health, /api/config, /api/v1
internal/<domain>/        domain types + ports.go; <domain>svc, <domain>module, adapters/<domain>{http,pg}
internal/event/           event envelope + Bus; adapters/outboxpg: transactional outbox + dispatcher
internal/workflow/        durable workflow port + Registry; adapters/workflowpg: River-backed engine
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
| Every route is in `api/openapi.yaml` and vice versa; `x-permissions` = what the route enforces; responses match the schemas | `TestRoutesMatchTheOpenAPISpec`, `TestOperationPermissionsMatchTheRoutes`, `checked(...)` in `TestServerRoundTrip` |

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
  is retried (the step isn't), so make it idempotent.
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
