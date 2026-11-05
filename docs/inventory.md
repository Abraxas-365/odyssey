# Inventario y "Necesita tu atención" — inventory (cut 3c)

`internal/inventory` is a **read-only** module: it owns no table and publishes no
event. It composes, per request, what other domains already answer through their
own `Queries` (project, release, build, fleet, approval), so nothing here can
drift from them. Two endpoints (`docs/api.md` §11), one console page, one MCP
tool.

## Inventory — `GET /api/v1/inventory`

Permission `odyssey:inventory:read` (the TI role). One row per **app and
environment**:

| Field | Meaning |
|---|---|
| `app`, `environment` | id, slug, name, tier / kind |
| `owner` | the user who created the app |
| `access`, `url` | access mode and the URL online |
| `status` | `not_deployed` · `deploying` · `online` · `failed` |
| `online` | `{image, since}` of the version being served |
| `resources`, `ai`, `secrets` | connected resources (name, kind, status), AI on/off, secrets `{total, missing[]}` |
| `scan` | `{state, critical, high, error?, scanned_at}` of the online image; `state`: `none` (nothing built here is online: no deployment, or `deploy_image`) · `pending` · `unknown` (could not be done; `error` says why) · `clean` · `vulnerable` |
| `cluster` | `{id, name}` |
| `needs_attention`, `attention[]` | what the release domain's `Uses` says for people and TI (never `wait`), or a failed last publication |
| `unavailable[]` | parts of this row that could not be read now (`scan`, `cluster`, `uses`, `resources`, `secrets`, `ai`, `egress`): the value is *unknown*, not "fine" |

Filters: `tier` (repeatable, OR), `cluster`, `needs_attention=true`, `search`
(name or slug), standard `limit`/`offset`. `?format=csv` exports **every row the
filters match** (no pagination): UTF-8 with BOM, Spanish headers, cells that a
spreadsheet could read as a formula are prefixed with `'`. Header
`X-Inventory-Truncated: true` says apps were left out (limit below).

`?cluster=` needs the placements and the clusters. If they cannot be read, the
answer is **not** an empty list: it carries `unavailable: ["clusters"]` (the
CSV, the header `X-Inventory-Unavailable: clusters`) and no rows, and the
console says "No pudimos leer los clústeres ahora" instead of "No hay entornos
que coincidan". Without `cluster=` nothing depends on them and a failed read
only marks each row's `unavailable`.

Limits: one read looks at **500 apps** (`inventory.MaxApps`); beyond that
`truncated: true` and the filters narrow it. Reads are batched (one query per
kind of data); only `needs_attention=true` and the CSV read each environment's
uses, **8 at a time** (`UsesWorkers`).

**Cost per request.** The inventory keeps nothing: every request re-reads from
the other domains, and the bound on all of it is `MaxApps`. A plain page loads
the (up to) 500 apps that match, their environments, what is serving, the
latest scans, the placements and the clusters, each as a batched read, and
then reads the `Uses` of **only the `limit` rows of the page** (at most 8 in
flight). `needs_attention=true` and the CSV additionally read the `Uses` of
**every** candidate environment (up to 500 apps × their environments), 8 at a
time: they are the expensive requests, are not cached, and an organization near
the cap should expect seconds rather than milliseconds. Use them occasionally;
do not poll them (the console polls `/attention` instead, which for a builder
reads only that builder's own apps). Raise `MaxApps` only together with a
cache.

Degradation: a failed read of scans or clusters sets that row's `unavailable`;
the inventory still answers. A row never shows "clean" for a scan it could not
read.

## Attention — `GET /api/v1/attention`

Any caller with `odyssey:projects:read`. The list depends on who asks:

| Scope | Kinds | Who sees it |
|---|---|---|
| `it` | `approval_pending` (waits for you; needs `approvals:decide`) | deciders |
| `it` | `app_vulnerable` (online image with critical vulnerabilities), `deployment_failed` (last publication failed in the last 24 h), `cluster_disconnected`, `cluster_policy` (network policy not applied) | holders of `inventory:read` |
| `mine` | `app_attention` (what `Uses` lists for the app's people), `deployment_failed` | the app's creator |
| `mine` | `request_pending`, `request_rejected` (7 days), `request_failed` (approved but could not run, 7 days) | the requester |

Each item: `kind`, `scope`, `severity` (`critical` · `warning` · `info`), a
plain-Spanish `sentence`, the console `link`, ids, `at`. Ordered by severity,
then newest. Each source is bounded to 100 items (`truncated`).

**An empty list is "Todo en orden" only when `unavailable` is empty.** Each
source (`approvals`, `apps`, `clusters`, `requests`) degrades on its own and
names itself in `unavailable`; the rest of the list is still answered.

## Console and MCP

- **Inicio** shows "Necesita tu atención" on top (empty and complete: "Todo en
  orden"; incomplete: says what it could not check). Refreshes every 30 s.
- **Inventario** (`/inventario`, TI): filters (nivel, clúster, atención,
  búsqueda), pages of 25, "Descargar CSV".
- **MCP** `list_apps` gains `needs_attention` per app: a cheap judgement
  (last publication failed, or critical vulnerabilities online) read in **one
  batch per page**, absent when it could not be read. It is **scoped like
  `/attention`**, by the caller's permissions (never a parameter): with
  `odyssey:inventory:read` (TI) every app of the page is judged; without it,
  only the apps the caller created, and for any other app the field is absent (a
  builder never learns of a colleague's failed deployment or vulnerabilities
  through `list_apps`; absent is not "fine"). The tool's description says so.
  `get_attention` returns
  the same list as the endpoint (`items`, `all_in_order`, `unavailable`,
  `truncated`). `get_app.needs_attention` remains the precise one (it reads
  `Uses`).

## Rules the architecture tests enforce

`TestInventoryIsReadOnly` (no repository, no writes, no events, no
subscriptions), `TestNoDomainKnowsTheInventory` (domains never import it; only
bootstrap does), `TestInventoryRulesCatchViolations`.
