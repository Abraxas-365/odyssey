# Contributing documentation

How Odicea's documentation is organized, the format its pages must follow, and how the website renders them.

The Markdown files in `docs/` are the only documentation source. GitHub renders them as they are, and the website under `website/` renders the same files, so a page has to work in both.

## Organization

English is the canonical language. Pages are grouped by reader need (the [Diátaxis](https://diataxis.fr) split):

| Directory | For | Example |
| :--- | :--- | :--- |
| `tutorials/` | a guided path with an observable result | [Your first app](../tutorials/first-app.md) |
| `guides/` | one task for a person who builds apps | [Publish an app](../guides/publish.md) |
| `guides/it/` | one task for IT: clusters, rules, approvals, the catalog | [IT's rules](../guides/it/rules.md) |
| `guides/operations/` | installing and running Odicea itself | [Install Odicea](../guides/operations/install.md) |
| `reference/` | exact contracts: configuration, commands, MCP tools, permissions, limits | [Configuration](../reference/configuration.md) |
| `explanation/` | rationale and mental models | [Concepts](../explanation/concepts.md) |
| `project/` | limitations and compatibility | [Known limitations](../project/known-limitations.md) |
| `contributing/` | development workflows | [Contributing](README.md) |

[`architecture.md`](../architecture.md) sits at the top level for contributors. The design contracts (`access.md`, `ai.md`, `api.md`, `approvals.md`, `catalog.md`, `egress.md`, `inventory.md`, `policy.md`, `resources.md`, `scan.md`, `secrets.md`) also stay at the top level: code comments and [AGENTS.md](../../AGENTS.md) cite them by path, so never move or rename them. Guides explain a task and link to the contract for the details; they don't copy it.

Create a page when there is verified content for it; don't scaffold empty pages.

## Format rules

The website build depends on these:

- **No front matter.** The first line is `# Title`. The first paragraph after it is the page description: one or two plain sentences (about 160 characters), ideally without links or code.
- **Plain GitHub Markdown.** Tables, fenced code blocks with a language (`sh`, `yaml`, `json`, `text`, `go`, `cel`), and relative links between pages, such as `../reference/mcp-tools.md#publishing`. Anchors are the GitHub slug of the heading.
- **Links are checked.** A relative link to a file that doesn't exist fails the website build. Links to files outside `docs/` (`../../AGENTS.md`, `../../iam/permissions.json`) are allowed and become GitHub links. Link to files, not to line numbers.
- **Callouts** use GitHub alert syntax: `> [!NOTE]`, `> [!TIP]`, `> [!IMPORTANT]`, `> [!WARNING]`. Use them sparingly.
- **Screenshots** live in `docs/assets/screenshots/` and are referenced relative to the page: `![Inicio](../assets/screenshots/home.png)`. They show the dark theme with fictional data only. The current set is listed [below](#screenshots).
- **Style.** Concise and concrete, second person ("you"). Say why briefly, then how. No marketing language.

### Vocabulary

The product is **Odicea**; the Go module, the binaries (`odyssey-api`, `odyssey-worker`, `odyssey-gateway`, `odyssey-agent`), the permission prefix (`odyssey:`) and the CRDs (`OdysseyApp`, `OdysseyResource`) keep the name `odyssey`. Write "Odicea" for the product and the code name only in code.

Two audiences read these docs:

- a **builder** is anyone who makes apps (often not a developer). Use the console's plain words: an **app** (a project in the API), a **version** (a deployment), a **part** (a service of an app with several), **storage** (resources), **who can open it** (access);
- **IT** sets the rules, runs the clusters and decides approvals. IT pages may use the technical words (deployment, namespace, CEL, capability).

The console is in Spanish. Name its screens as they appear, with the English meaning in parentheses the first time on a page: **Inicio** (Home), **Bandeja** (Inbox), **Inventario** (Inventory), **Registro** (Audit log), **Administración** (Administration) with **Clústeres**, **GitHub**, **Reglas** (IT's rules) and **Catálogo** (Catalog); an app's tabs **Resumen** (Overview), **Historial** (History), **Compartir** (Sharing) and **Configuración** (Settings); **Conecta tu asistente** (Connect your assistant), **Necesita tu atención** (Needs your attention), **Volver a esta** (Go back to this one), **Detalles técnicos** (Technical details). App tiers are **sandbox**, **internal** and **production** (*Sandbox*, *Interna*, *Producción* in the console). The roles in [`iam/permissions.json`](../../iam/permissions.json) are **TI**, **Builder** and **Lector**.

## Public documentation rules

Write as if the repository were public. Never put in docs, examples or screenshots:

- real domains, emails, hostnames, IP addresses, tokens, keys or GitHub installations. Use `example.com`, `odicea.example.com`, `apps.example.com`, `ana.torres@example.com`, `ghp_…` / `sk-…` (elided) and variable names without a value;
- customer names. The fictional company is **Andes Logística**, its people are Ana Torres (IT), Diego Ramos (builder) and Lucía Paredes (reader), and its apps are *Gastos*, *Inventario de bodega* and *Portal de proveedores*.

The local development defaults (`localhost`, `ti@demo.local`, `*.apps.odyssey.localhost`) are fine where a page is about running Odicea on your machine.

## Source of truth

Read the code before documenting a contract, and trace behavior through the service and the adapter, not only a comment, which may describe an earlier design. Verify every flag, environment variable, route, permission, limit and field against `cmd/`, `internal/bootstrap` (configuration), the domain packages, [`api/openapi.yaml`](../../api/openapi.yaml), [`iam/permissions.json`](../../iam/permissions.json), the [`Makefile`](../../Makefile) and the console in `frontend/src`. If you can't verify a claim, leave it out.

[AGENTS.md](../../AGENTS.md) holds the engineering invariants. Keep it and the docs current with behavior in the same change: a new environment variable, route, permission, limit, MCP tool or console screen updates the matching reference page or guide, [known limitations](../project/known-limitations.md) if relevant, and AGENTS.md.

## Examples

- Never include real secrets. Pass secret values through stdin, the console or environment variables in examples.
- Say what each user-chosen name is (an app, a cluster, a recipe).
- State prerequisites, such as a running stack, an enrolled cluster or a connected assistant.
- Show commands you run (`sh`) separately from YAML, JSON bodies and CEL rules.
- An assistant prompt is plain text in a `text` block, in the language a builder would use.

## Screenshots

Screenshots are 1440×900 PNGs of the console in the dark theme, captured with Playwright from a stack seeded with the fictional company above. Reuse an existing one before adding another.

| File | Shows |
| :--- | :--- |
| `home.png` | Inicio with apps and *Necesita tu atención* |
| `create-app.png` | The *Crear una app* dialog with its three paths |
| `app-overview.png` | An app's Resumen: online version, parts, what it uses |
| `app-history.png` | Historial with versions and *Volver a esta* |
| `app-share.png` | Compartir: who can open the app |
| `app-settings.png` | Configuración: variables, secrets and resources |
| `app-resources.png` | Adding a database from the catalog |
| `app-ai.png` | The app's AI panel |
| `app-security.png` | The version's scan result (*Seguridad*) |
| `technical-details.png` | The same app with *Detalles técnicos* on |
| `assistant.png` | Conecta tu asistente |
| `bandeja.png` | Bandeja with a pending approval |
| `inventory.png` | Inventario |
| `registro.png` | Registro (audit log) |
| `clusters.png` | Administración → Clústeres, with the egress card |
| `github.png` | Administración → GitHub |
| `rules.png` | Administración → Reglas, with the rule editor |
| `catalog.png` | Administración → Catálogo |

## Preview the website

`website/` is an Astro Starlight site that reads `../docs`. It needs Node 22.12 or later:

```sh
cd website
npm ci
npm run dev           # http://127.0.0.1:4350, search is not indexed in dev
npm run build         # production build with search
npm run check:links   # rendered links, anchors and assets (run after build)
```

`npm test` runs the loader tests, including one that fails when a page under `docs/` is missing from the sidebar or listed twice.

The page title and description come from the H1 and the first paragraph, and the build removes the duplicate H1 and rewrites relative links.

> [!WARNING]
> The sidebar is curated by hand in `website/src/sidebar.mjs`. Every new page must be listed there, or `npm test` fails.

## Before you submit

- [ ] The page starts with `# Title` and a plain description paragraph, with no front matter.
- [ ] Relative links resolve to existing files and headings; `npm run build` and `npm run check:links` pass.
- [ ] Commands, variables, routes, permissions and MCP tools agree with the code.
- [ ] No real domains, emails, hosts or keys; screenshots use fictional data.
- [ ] The new page is in `website/src/sidebar.mjs`.
- [ ] [Known limitations](../project/known-limitations.md) and [AGENTS.md](../../AGENTS.md) are updated when behavior changed.
