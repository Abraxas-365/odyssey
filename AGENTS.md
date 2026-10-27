# AGENTS.md — Odyssey

Odyssey is a governed, enterprise Vercel-like platform. It runs on the customer's
own infra (on-prem/AWS/GCP/Azure). Business users ("vibecoders") connect a repo
and deploy, with security set by IT. Odyssey offers one MCP for coding agents
and uses IAMKit for platform and app auth.

This repo holds the **control-plane API** (`cmd/odyssey-api`) and the **web
console** (`frontend/`). The API has one real module, `project`. The full
design (binaries, domains, build order) lives outside the repo, in
`~/.rness/plans/Personal/odicea-architecture.md`. The UI design is in
`odicea-mockup.html` and `odicea-product-ux-v3.md` there.

## Commands

| Task | Command | ~Time |
|---|---|---|
| Build | `make build` (→ `bin/odyssey-api`) | 5s |
| Unit + arch tests (DB tests skip) | `make test` | 5s |
| All tests vs compose Postgres | `make test-db` (`PKG=./internal/project/...`) | 10s |
| CI gate (gofmt, vet, test) | `make check` | 10s |
| Regenerate permissions | `make gen` (after editing `iam/permissions.json`) | 1s |
| First local run (secrets, IAMKit, seed, API) | `make dev` | 2-4m |
| Push manifest changes to local IAMKit | `make iam-sync` (`DRY_RUN=1`, `PRUNE=1`) | 5s |
| Token / end-to-end check | `make token` / `make smoke` | 5s |
| Console dev server (needs API + IAMKit) | `make web` → http://localhost:5178 | 2s |
| Console gate (types, oxlint, vitest, build) | `make web-check` | 15s |

Local ports: API 8095, IAMKit 8085, Postgres 5438, web 5178.

## File map

```
cmd/odyssey-api/          entry point → bootstrap.Run
internal/bootstrap/       composition root, migrations list, ARCHITECTURE TESTS
internal/server/          Fiber app: middleware, /health, /api/config, /api/v1
internal/<domain>/        domain types + ports.go; <domain>svc, <domain>module, adapters/<domain>{http,pg}
internal/{errx,identity,query,text,txn,pgerr,migrate,pgtest,config,httpx,permission}/  foundation
iam/permissions.json      the IAMKit permission manifest (source of internal/permission)
docs/api.md               HTTP contract; code cites its sections (§N)
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
- Wire types in `src/lib/types.ts` mirror `docs/api.md` §11 in snake_case.
  Update both together.
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

When you adopt a new pattern (events/outbox, workflow, blob), add an arch rule
**and** a fixture in `TestArchitectureRulesCatchViolations`.

Other rules:

- **Tenancy**: the organization comes only from the token. Every repo statement
  filters by `organization_id`, and another tenant's row is `NotFound`.
- **Errors**: return `errx` types, never set HTTP statuses by hand. Map DB
  errors with `pgerr`. The status table is `docs/api.md` §2.
- **Migrations**: add new files only, never edit applied ones. Each module owns
  a Postgres schema.
- **New permission**: add it to `iam/permissions.json`, run `make gen`, use
  `permission.X`, then run `make iam-sync`.
- Update `docs/api.md` §11 with every endpoint change.

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
