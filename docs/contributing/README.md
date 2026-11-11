# Contributing

How to set up, change and check Odicea's code and documentation, and what a pull request must satisfy.

Read [AGENTS.md](../../AGENTS.md) first. It holds the commands, the file map and the rules that tests enforce, and it is the authority when anything here is out of date.

## Before you start

[Architecture](../architecture.md) explains how the code is organized and why. Three rules affect almost every change:

- The organization comes only from the authenticated token, and every repository statement filters by it.
- A domain never imports another domain's service or adapter. It declares the narrow port it needs, and only `internal/bootstrap` wires them.
- A write publishes its event in the same transaction as the change.

If a change needs a new binary, a new third-party dependency, a change to the architecture tests or to a foundation package, ask first. AGENTS.md lists these under "Ask".

## Prerequisites

- Go (the version in `go.mod`), Docker with Compose, and `make`.
- Node 22.12 or later for the console and the documentation website. Use `npx -y npm@11` to install console dependencies, because npm 10.9 crashes on them.
- For the cluster tests: `k3d` and `kubectl`. For a change to the fleet protocol: `protoc`.

## Set up

[Local development](../guides/operations/local-development.md) has the whole path to a running stack. In short:

```sh
make dev           # first run: secrets, IAMKit, seed data and the API
make worker        # the worker, in its own terminal
make web           # the console on http://localhost:5178
```

For a local cluster and an agent, see `make cluster` and `make agent-enroll` in [Make targets](../reference/make-targets.md). Every target is listed there.

## The gates

Run these before you push. CI runs the same.

```sh
make check         # gofmt, go vet and go test
make web-check     # console: types, oxlint, vitest and build (after touching frontend/)
make test-db       # all tests against the compose Postgres (PKG=./internal/project/... to narrow)
```

| Check | What it covers | Where |
| :--- | :--- | :--- |
| Backend | `gofmt`, `go mod tidy` is clean, `go vet`, then `go test -race` against Postgres 18 | CI job "backend" |
| Generated code | `make gen` leaves no diff in `internal/permission`, `agent` and `proto` | CI job "backend" |
| Cluster | gateway, agent and the fleet adapter against a real k3s | CI job "the fleet" (`make test-kube`) |
| Console | generated API types, typecheck, oxlint, vitest, build | CI job "console" |
| Documentation | `npm test`, `npm run build` and `npm run check:links` in `website/` | See [Contributing documentation](documentation.md) |

> [!WARNING]
> Never weaken an architecture test to make a change pass. Fix the code. If the rule itself is wrong, change the rule and AGENTS.md in the same pull request, and add a fixture to `TestArchitectureRulesCatchViolations`.

Every new repository method needs a repository test against a real database (`pgtest`), including a test that another tenant's row is not found. Tests that need Postgres skip when it is not there; `make test-db` makes them run.

## Common changes

### Add a permission

1. Add it, with a label in Spanish, to `iam/permissions.json`, and to the roles that should have it.
2. Run `make gen`. It regenerates `internal/permission`.
3. Use the generated constant, `permission.X`, never a string literal.
4. Run `make iam-sync` to push the manifest to your local IAMKit.
5. If the console needs it, add it to `frontend/src/lib/permissions.ts`, which must stay a subset of the manifest.
6. Update [Permissions](../reference/permissions.md).

### Add or change an endpoint

1. Put the route in the domain's `adapters/<domain>http` with `httpx.RequirePermissions(permission.X)`.
2. Update `api/openapi.yaml` (shapes and `x-permissions`) and `docs/api.md` section 11 in the same change.
3. Run `make api-types` so the console's wire types match.
4. Return `errx` types and never set statuses by hand. The status table is in `docs/api.md` section 2.
5. In `server_test.go`, route new calls through `checked(...)`.

`internal/bootstrap/openapi_test.go` fails on an undocumented route, a permission mismatch or a response that does not match its schema.

### Add an MCP tool

Declare its permissions and risk, and add it to the module's MCP adapter. Update the golden file with `ODYSSEY_UPDATE_GOLDEN=1 go test ./internal/bootstrap -run MCPToolsContract`, then review the diff. Add the tool to the `odicea` skill under `skills/` (a test checks the names), and update [MCP tools](../reference/mcp-tools.md).

### Add an event

Add the type and payload to the domain's `events.go`. Every event type needs a golden contract in the domain's `testdata/events/`, made with `UPDATE_GOLDEN=1 go test ./internal/<domain>/...`. Payload changes are additive within a version; otherwise bump the version and add a new golden file. Subscribers are idempotent, because delivery is at least once.

### Add a domain

Copy `internal/project`, the golden sample in AGENTS.md:

1. The domain root has the typed ID, `ports.go` (`Commands`, `Queries`, `Repository`), entities with `Validate()` and the business errors.
2. `<domain>svc` orchestrates. `adapters/<domain>pg` holds SQL and an embedded `migrations/NNN_*.up.sql` in its own schema. `adapters/<domain>http` holds the routes.
3. `<domain>module` assembles it. Register it in `bootstrap.NewServer`, add its migrations to `Migrations()`, and wire what it needs from other domains with adapters in the composition root.
4. Run `go test ./internal/bootstrap/...`. The architecture tests tell you what is missing.

Add new migration files only. Never edit one that has been applied: its checksum is verified at boot.

### Change the console

Pages compose the components in `src/components/odyssey`, and copy is Spanish and plain. Update the design-system page at `/design` when you add or change one of those components. `src/lib/openapi.gen.ts` is generated, so never edit it. AGENTS.md has the rest, under "Frontend".

## Commit style

Commits use a short Conventional Commits subject, in the imperative: `feat(scope): what changed`, `fix(scope): …`, `docs: …`, `test: …` or `refactor: …`. The scope is the domain or area (`catalog`, `console`, `agent`). Keep one change in a commit, and say in the body why when it is not obvious.

Never commit `.env`, `secrets/` or `.dev-secrets/`.

## Documentation

The files in `docs/` are the documentation, and the same files feed the website. A change that alters behavior (a variable, a route, a permission, a limit, an MCP tool or a console label) updates the pages in the same pull request. The format, the vocabulary and the checklist are in [Contributing documentation](documentation.md). Update [Known limitations](../project/known-limitations.md) when a limit appears or goes away.

## Before you open a pull request

- [ ] `make check` passes, and `make web-check` if you touched `frontend/`.
- [ ] Generated files are fresh (`make gen`, `make api-types`).
- [ ] New repository methods have tests, including tenant isolation.
- [ ] No architecture test was weakened, and AGENTS.md matches any rule you changed.
- [ ] The documentation is updated, and no secrets, real hostnames or customer names are in the change.
- [ ] The description says what changed and why, and how you checked it.
