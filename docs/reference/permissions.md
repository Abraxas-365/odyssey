# Permissions

Odicea decides what each person can do with 17 permissions, grouped into three roles in the identity server. This page lists every permission, what each role holds, and where each permission is checked: the API, the assistant tools and the console.

> [!NOTE]
> The names keep the `odyssey:` prefix. The single source is `iam/permissions.json`; the Go constants in `internal/permission` are generated from it (`make gen`), and the console's `permissions.ts` mirrors it by hand, with a test that keeps them in sync.

## Roles

A person's token carries the permissions of their role. Roles are assigned in the identity server (IAMKit); see [People and roles](../guides/it/people-and-roles.md).

| Permission | TI | Builder | Lector |
| :--- | :---: | :---: | :---: |
| `odyssey:projects:read` | ✓ | ✓ | ✓ |
| `odyssey:projects:write` | ✓ | ✓ | |
| `odyssey:projects:tier` | ✓ | | |
| `odyssey:deployments:read` | ✓ | ✓ | ✓ |
| `odyssey:deployments:create` | ✓ | ✓ | |
| `odyssey:deployments:rollback` | ✓ | ✓ | |
| `odyssey:resources:delete` | ✓ | ✓ | |
| `odyssey:clusters:read` | ✓ | | |
| `odyssey:clusters:manage` | ✓ | | |
| `odyssey:integrations:manage` | ✓ | | |
| `odyssey:audit:read` | ✓ | | |
| `odyssey:access:public` | ✓ | | |
| `odyssey:secrets:external` | ✓ | | |
| `odyssey:policy:manage` | ✓ | | |
| `odyssey:catalog:manage` | ✓ | | |
| `odyssey:approvals:decide` | ✓ | | |
| `odyssey:inventory:read` | ✓ | | |

The manifest also defines a service account, `odicea-backend`, with the identity server's own `iam:members:read` permission. Odicea uses it to read members and teams; it is not one of the 17.

## What each permission allows

The descriptions are the labels the manifest gives each permission (in Spanish, as people see them in the identity server), in English.

| Permission | Allows |
| :--- | :--- |
| `odyssey:projects:read` | See apps, their environments and services. |
| `odyssey:projects:write` | Create, edit and archive apps; set services, access, variables, secrets, resources, AI and the GitHub connection of an app. |
| `odyssey:projects:tier` | Change an app's tier (sandbox, internal or production). |
| `odyssey:deployments:read` | See which version is online and the publication history, builds and logs. |
| `odyssey:deployments:create` | Publish a new version of an app. |
| `odyssey:deployments:rollback` | Go back to a previous version of an app. |
| `odyssey:clusters:read` | See the organization's clusters and their status. |
| `odyssey:clusters:manage` | Connect, reconnect, disconnect and move clusters; change a cluster's internet egress. |
| `odyssey:integrations:manage` | Connect and disconnect GitHub for the organization. |
| `odyssey:audit:read` | See the organization's activity history. |
| `odyssey:access:public` | Let anyone with the link open an app without signing in. |
| `odyssey:secrets:external` | Connect an app's secrets to the company's secret store. |
| `odyssey:resources:delete` | Delete an app's databases, caches and files (deletes their data). |
| `odyssey:policy:manage` | See and edit IT's rules. |
| `odyssey:catalog:manage` | Choose which databases, caches and files the organization offers, and their limits. |
| `odyssey:approvals:decide` | Approve or reject actions that IT's rules leave pending. A rule can name another permission as its approvers; this is the default. |
| `odyssey:inventory:read` | See the inventory of every app in the organization and export it. |

## Where each permission is checked

### API

Every operation in `api/openapi.yaml` lists the permissions it enforces under `x-permissions` (all of them are required). The reference below is derived from it.

| Permission | Operations |
| :--- | :--- |
| `projects:read` | Lists and reads of apps, services, environments, settings, uses, catalog offer, resources and AI of an environment; `GET /attention`, `GET /assistant`, GitHub connection and installations lists. |
| `projects:write` | Create, edit and archive apps; set services; detect services; set access, variables, secrets (and their scope), resources (create, edit, connect, disconnect), AI (enable, disable, rotate); GitHub repositories, branches and services lists; connect or disconnect an app's repository. |
| `projects:tier` | `PUT /projects/{id}/tier`. |
| `deployments:read` | Current version, versions, one version, releases, builds and logs, services status, app states; repository connection and pushes. |
| `deployments:create` | Publish a version; connect a repository (with `projects:write`); publish from the repository. |
| `deployments:rollback` | Roll back to a version. |
| `resources:delete` | Delete a resource. |
| `clusters:read` | List and read clusters and their egress. |
| `clusters:manage` | Register a cluster, issue an enrollment token, revoke, move, set egress. |
| `integrations:manage` | Start a GitHub install, link and unlink an installation. |
| `audit:read` | Audit entries, verification, summary and people. |
| `policy:manage` | List, create, change and delete rules; evaluate a rule. |
| `catalog:manage` | Register recipes, edit offerings, add recipe versions, list secret stores and teams. |
| `inventory:read` | `GET /inventory`. |

A few operations need no permission, only a signed-in user: `GET /me`, `DELETE /me/assistants/{id}`, `GET /catalog/recipes`, `GET /catalog/recipes/{recipe}`, `GET /catalog/kinds`, and the approval endpoints (list, read, approve, reject, cancel). The approval endpoints check inside the handler: a person sees the requests they made and those they could decide, and deciding needs one of the request's approver permissions (by default `approvals:decide`) and a person other than the requester.

Three permissions are checked by the action that needs them, on top of the route's own:

| Permission | Extra check |
| :--- | :--- |
| `access:public` | Setting an app's access to `public` (403 `FORBIDDEN` without it). |
| `secrets:external` | Connecting a secret to the company's secret store, or registering an existing resource whose connection reads from a secret store (403 `FORBIDDEN` without it). |
| `approvals:decide` | Seeing and deciding requests waiting for a decision; it also adds those requests to `GET /attention`. `inventory:read` adds the IT items (critical vulnerabilities, failed publications, clusters that do not answer). |

### Assistant tools (MCP)

The `/mcp` server checks the permissions of the person's token before each tool runs. Each tool lists its permissions in [MCP tools](mcp-tools.md); in short:

| Permission | Tools |
| :--- | :--- |
| `projects:read` | `whoami`, `list_apps`, `get_app`, `get_attention`, `list_services`, `list_catalog`, `list_resources`, `get_request`, `list_requests`, and the read half of every tool that names an app. |
| `projects:write` | `create_app`, `set_services`, `detect_services`, `set_access`, `set_env`, `request_secret`, `list_repositories`, `connect_repository`, `create_resource`, `create_database`, `enable_ai`. |
| `deployments:read` | `get_app`, `list_versions`, `get_deployment`, `get_build_logs`. |
| `deployments:create` | `deploy_folder`, `deploy_image`, `connect_repository`, `publish_now`. |
| `deployments:rollback` | `rollback`. |
| `access:public` | `set_access` with `mode: "public"` (not listed on the tool). |

The assistant never gets more than the person has: a Lector's assistant can read but not publish. The other permissions (clusters, integrations, audit, policy, catalog, approvals, inventory, tier, secrets from a store and resource deletion) have no assistant tool.

### Console

The console hides what the person cannot use.

| Where | Permission |
| :--- | :--- |
| "Inventario" tab | `inventory:read` |
| "Registro" tab; the audit part of an app's history | `audit:read` |
| "Administración" tab: appears when at least one section is available | |
| Section "Clústeres" | `clusters:read` (connect, revoke, move and egress need `clusters:manage`) |
| Section "GitHub" | `integrations:manage` |
| Section "Reglas" | `policy:manage` |
| Section "Catálogo" | `catalog:manage` (a recipe that reads from a secret store also needs `secrets:external`) |
| "Bandeja": requests waiting for a decision | `approvals:decide`; without it the person sees only what they asked for |
| App "Resumen" and "Historial": version list and state | `deployments:read` |
| App: publish folder and repository actions | `deployments:create` (and `projects:write` to connect) |
| App: "Volver a esta" | `deployments:rollback` |
| App: edit settings (services, variables, resources) | `projects:write` |
| App: "Nivel" (tier) editable | `projects:tier` |
| App: "Quién puede abrirla", option "Cualquiera con el enlace" | `access:public` |
| App: "Secretos" connected to the company's store | `secrets:external` |
| App: "Datos e IA", delete a resource | `resources:delete` |

## Keeping IAMKit in sync

The manifest has to reach the identity server for a new permission to exist there. Two Make targets do it; see [Make targets](make-targets.md).

- `make iam-sync` pushes the manifest to the local IAMKit.
- `make iam-provision` provisions or syncs any IAMKit (staging, production): project, organization, application, resource, roles, the console's OAuth client and the backend service account.

Both accept `DRY_RUN=1` to print the changes without writing and `PRUNE=1` to also remove permissions that left the manifest. A pruned permission is stripped from every role, grant and service account at once, so prune only after no deployed code checks it.

After editing `iam/permissions.json`, run `make gen` to regenerate the Go constants, update the console's `permissions.ts` by hand (a test fails when it drifts), then sync.

## See also

- [MCP tools](mcp-tools.md)
- [Errors](errors.md)
- [People and roles](../guides/it/people-and-roles.md)
- [Rules](../guides/it/rules.md)
- [Approvals](../guides/it/approvals.md)
