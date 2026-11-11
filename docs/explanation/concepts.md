# Concepts

The mental model behind Odicea: what an app, a version and a cluster are, who sets the rules, and which word the console uses for each thing the API and the code name differently.

Odicea publishes internal apps on the company's own clusters. People who build apps publish them; IT decides what is allowed. Everything below follows from that split.

## The map

```text
organization                          the company: the tenant, from your sign-in token
 ├─ clusters ── agent ══ outbound mTLS ══► gateway ──► control plane
 │                                                      (API, worker, database)
 ├─ IT's rules · catalog · approvals · GitHub connection
 └─ apps                              API: "project"; has a tier
     └─ environment                   "production" today
         ├─ parts                     API: "services" (web, worker)
         ├─ versions                  API: releases and deployments
         ├─ storage                   API: "resources" (database, cache, files)
         └─ access mode               who may open it: team, organization, public
```

The control plane stores what should run. The agent, which lives inside each cluster, makes it so and reports back. See [Life of a version](life-of-a-version.md) for the path and [Security model](security-model.md) for why the planes are separate.

## Organization

An **organization** is the company, and it is the tenant: every app, rule, cluster and log entry belongs to exactly one. Odicea takes it from the organization of your IAMKit token, never from a URL or a request body, so one company cannot see another's data. People hold roles in it: **TI** (IT), **Builder** and **Lector** (reader) are the roles in the permission manifest. See [Permissions](../reference/permissions.md).

## App

An **app** is what a team publishes: an internal tool such as *Gastos* or *Inventario de bodega* at Andes Logística. The API and the code call it a *project*. It has a name, a URL slug derived from it, a **tier** and a life that goes from active to archived (an archived app is retired from its cluster; its history stays).

The **tier** is the app's "Nivel" in the console: *Sandbox* (to try things), *Interna* (the default for a new app) or *Producción*. Only IT changes it, and IT's rules ask about it: by default, publishing a production app needs approval.

## Environment

An app has **environments**. Today every app is born with one, `production`, and that is the only one the console and the API create. The console rarely names it. The environment is where a version runs, where settings live (variables, secrets, access mode) and which cluster it is placed on.

## Part

An app can be made of several **parts**: a web front, an API, a queue worker. The API calls them *services* (kind `web` or `worker`, at most 8 per app), and the console says "Partes de la app" (the app's parts). Each part is built from its own folder of the same code. One part answers at `/`; the others answer on their own path. An app of one part never shows the word at all. See [Several parts](../guides/several-parts.md).

## Version

A **version** is one thing that runs. Under the name there are two objects:

- A **release** is immutable: an image plus its configuration.
- A **deployment** is one attempt to make a release the one an environment serves. It has a reason: `deploy` (someone published), `rollback` (someone went back), `auto_rollback` (the platform restored the working version after a failure) or `config` (the same version again, to apply a changed setting).

The console shows both as "Versión". A version is "En línea" (online) only after it passed its health check; going back is "Volver a esta". See [Versions](../guides/versions.md).

## Storage

An app can ask for **storage**: a database, a cache, a file volume, or a bucket the company already has. The API calls these *resources* and the console groups them in the panel "Datos e IA" (data and AI). The app receives the connection as environment variables. Data lives in the cluster and does not move with the app. See [Storage](../guides/storage.md).

## Catalog, recipe and offering

IT decides what people may create as storage:

- A **recipe** is a versioned way of making one kind of storage (for example a basic PostgreSQL, or a production one with two instances). A released recipe version never changes; an Odicea update only adds versions.
- An **offering** is IT's choice for a recipe in this organization: whether it is offered, in which tiers, for which teams, within which size limits, and how much can be created without asking IT.
- The **catalog** ("Catálogo", under "Administración") is where IT manages the offerings. It can also register a resource the company already runs, which Odicea only connects.

See [The catalog](../guides/it/catalog.md) and the contract in [catalog.md](../catalog.md).

## IT's rules

**Rules** ("Reglas") are IT's rulebook. Each rule is a short expression over what is happening (the action, the app's tier, the access mode, who acts, the vulnerability scan) and an effect: *Bloquea* (deny), *Pide aprobación* (ask for approval) or *Permite* (allow). Deny beats approval, and approval beats allow. A rule that cannot be evaluated counts at its own strength, so it fails closed. The code calls this the *policy*. See [IT's rules](../guides/it/rules.md) and [Rule input](../reference/rule-input.md).

## Approval

When a rule asks for approval, the action waits. The request appears in **Bandeja** (Inbox) for people who can decide, and a held publication shows "Esperando aprobación de TI". Nobody approves their own request; an approved request is used once; it expires after seven days. See [Approvals](../guides/it/approvals.md).

## Cluster, agent and gateway

A **cluster** ("Clústeres") is a Kubernetes cluster where apps run. IT connects it with "Conectar clúster", which gives a one-time enrolment token.

- The **agent** (`odyssey-agent`) runs inside the cluster. It dials out to the gateway, receives the desired state and turns it into Kubernetes objects. The control plane never holds credentials for the cluster, and the cluster needs no inbound port.
- The **gateway** (`odyssey-gateway`) is where agents connect over mutual TLS. It is the only process that holds the fleet certificate authority's private key.
- The **control plane** is the API, the worker and the database.

See [Connect a cluster](../guides/it/clusters.md).

## Capability

A **capability** is something an agent announces it can do, such as `odicea.access` (protect apps with a sign-in) or `odicea.scan` (scan images). Odicea never assumes a cluster can do what it did not announce: it refuses or holds back the work that needs it. IT sees them under "Detalles técnicos". See [Capabilities](../reference/capabilities.md).

## Access mode

The **access mode** answers "Quién puede abrirla" (who can open it):

| Console | API | Who opens the app |
| :--- | :--- | :--- |
| "Solo tu equipo" | `team` | People of the app's own organization, signed in. |
| "Toda la empresa" | `organization` | Any person with an Odicea sign-in in the same identity environment. |
| "Cualquiera con el enlace" | `public` | Anyone, with no sign-in. Needs a permission only IT has. |

See [Who can open it](../guides/who-can-open-it.md) and [access.md](../access.md).

## Assistant, MCP and skill

An **assistant** is a coding assistant (Claude Code, Codex, Cursor, VS Code, Antigravity) that a person connects to their account at "Tu asistente". It speaks to Odicea through the **MCP** endpoint (`POST /mcp`), with that person's permissions and no more. The **skill** is a short instruction file named `odicea` that teaches the assistant how to prepare and publish an app and what never to do, such as putting secrets in code. See [Connect an assistant](../guides/connect-an-assistant.md) and [MCP tools](../reference/mcp-tools.md).

## Console word, API word

| Console (Spanish) | Meaning | API and code |
| :--- | :--- | :--- |
| App | What a team publishes | `project` |
| Nivel (Sandbox, Interna, Producción) | The app's tier | `tier` (`sandbox`, `internal`, `production`) |
| Partes de la app | Workloads of one app | `services` (`web`, `worker`) |
| Versión | One thing that runs | `release` + `deployment` |
| Publicar / Publicar ahora | Make a new version | create a deployment; `publish_now` (MCP) |
| Volver a esta | Go back to an earlier version | rollback (`reason: rollback`) |
| En línea | Passed its health check and serving | deployment status `ready` |
| Publicando… | In progress | `building`, `pending`, `applying`, `verifying` |
| Esperando aprobación de TI | Held by a rule | `waiting_approval` |
| Datos e IA | Storage and AI of an app | `resources`, `ai` |
| Catálogo, "Se ofrece" | What IT lets people create | catalog, recipe, `offering` |
| Reglas | IT's rulebook | `policy` rules (CEL) |
| Bandeja | Requests waiting for a decision | `approvals` |
| Inventario | Everything the company runs, for IT | `inventory` (read-only) |
| Registro | The company's activity trail | `audit` |
| Clústeres | Where apps run | `fleet` clusters |
| Quién puede abrirla | Who may open the app | `access` mode |
| Tu asistente | A connected coding assistant | MCP client |
| Detalles técnicos | Extra detail for technical people | technical mode (`preferences`) |

Code names keep `odyssey`: the binaries (`odyssey-api`, `odyssey-worker`, `odyssey-gateway`, `odyssey-agent`), the permission prefix `odyssey:` and the Kubernetes resources `OdysseyApp`, `OdysseyResource` and `OdysseyBuild`. See [CRDs](../reference/crds.md).

## See also

- [Life of a version](life-of-a-version.md)
- [Security model](security-model.md)
- [Architecture](../architecture.md)
