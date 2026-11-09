# Resource catalog (stage 1a)

What an organization lets its people create as a resource is IT's call, not
a hardcoded list. The catalog has four pieces with four owners:

| Piece | Owner | Mutable? | Where |
|---|---|---|---|
| **Type** (`postgres`, `redis`, `files`, `object-storage`) | Odicea | with the binary | `pkg/resourcekind` (the contract: variables, noun, settings; `Builtin`: an engine of Odicea prepares it; `Existing`: it can be one the company already has) |
| **Recipe version** (`postgres-produccion@1`) | Odicea | **never** once released | `internal/catalog/recipes.go` |
| **Offering** (of a recipe, per organization) | IT | yes (`PUT …/offering`) | table `catalog.offerings` |
| **Resource** (pinned `recipe@version` + params) | the app's team | only its mutable params | table `resource.resources` |

## Recipe versions

A recipe version is immutable: kind, engine (`builtin` in stage 1), the
parameters it accepts (`storage_gi` today) with their widest range, default
and **mutability** (`immutable`, `grows`, `mutable`), the engine settings it
fixes (`resourcespec.Spec`: instances, PostgreSQL major, CPU/memory) and the
cluster **capabilities** it requires. `internal/catalog/testdata/versions.json`
freezes every released version: `TestReleasedVersionsNeverChange` fails when
one changes. An Odicea update only **adds** versions; the organization moves
an offering to the new one when IT decides.

Factory recipes (v1):

| Recipe | Spec | Offering created for every organization |
|---|---|---|
| `postgres-basico` | none (1 instance, as before the catalog) | every tier, default, 1–100 GiB pre-approved |
| `redis-basico` | none | every tier, default, 1–100 GiB pre-approved |
| `files-basico` | none | every tier, default, 1–100 GiB pre-approved |
| `postgres-produccion` | 2 instances, PostgreSQL 17, CPU 1, memory 2Gi | production only, not the default, ≥ 5 GiB, pre-approved ≤ 50 GiB, default size 10 |

The basic recipes are exactly what existed before: nothing changes for anyone
until IT edits the catalog.

### The organization's own recipes (stage 2)

An organization also publishes recipes of its own: today a resource the
company **already has** (engine `existing`), which Odicea only connects. They
live in `catalog.recipe_versions`, per organization, and follow the same rule
as the factory's: a published version never changes (the table only takes
inserts; triggers refuse updates, deletes and truncation) and a new connection
is a new version. Their ids start with `org-`, which no factory recipe does
(`TestFactoryVersionsAreSound`), so a later Odicea release never takes one.
They have no parameters and no factory offering. Stage 2 lands in cuts: so
far they are stored and read (the catalog lists one, inactive), their
connection is validated, and the agent's `existing` engine writes it in the
cluster (docs/resources.md, "A resource the company already has");
publishing them (API, permission) and offering them come next.

**The connection** (`resourcespec.Existing`) lists where each variable of the
type's contract comes from: a value written in the recipe, or a reference
`{store, key, property}` to a `ClusterSecretStore` (External Secrets). A
credential (a secret variable) is only ever a reference, so it never passes
through Odicea; a derived variable (`DATABASE_URL`) is never written, the agent
builds it from the pieces; optional ones (`S3_ENDPOINT`, the AWS keys) may be
left out, and a key goes with its secret (both or neither). A written host may
be a private address or an internal name, but never loopback, link-local (the
cloud's metadata service), a single label (the pod's search domains would make
it a Service), a number some resolvers read as an address, or a name under
`*.svc`, `*.cluster.local` or `*.localhost`. `PGSSLMODE` is optional and one
of libpq's modes (`disable` … `verify-full`). `files`
can never be one (it is a volume of the cluster); `object-storage` is only
ever one (no engine of Odicea makes it).

**Capabilities per engine.** A built-in resource needs the kind's engine in
the cluster (`builtin:<kind>`); one the company already has needs
`existing` (an agent that writes such connections, with External Secrets)
and `secretstore:<name>` for each store it reads, derived from the stores
the cluster reports, never the kind's built-in engine: a cluster without
CloudNativePG can connect the company's database. A store the cluster later
loses leaves the resource `failed`, naming the store. Such a resource has no
size: `storage_gi` is 0 and `params` has no `storage_gi` (CHECKs in
`resource.resources` and `fleet.desired_resources`). A tier change counts it
as stranded whenever its recipe is not offered in the new tier, even if its
kind holds no data. No event carries a recipe's spec (only `recipe@version`);
an architecture test keeps `resourcespec` out of every `events.go`.

Because of them, a resource's pinned version is read through
`catalog.Queries.Version(org, recipe, version)` (the factory's and the
organization's own, never another organization's); `catalog.Factory()` alone
is the catalog's (`TestOnlyTheCatalogReadsTheFactoryAlone`).

## Offerings

One per organization and recipe, created lazily and idempotently on the
organization's first catalog read (like IT's built-in rules). IT edits it with
`odyssey:catalog:manage` (`docs/api.md` §11 "Resource catalog"):

- **version** offered, **active**, **tiers** where it is offered;
- **defaults**: tiers where it is its kind's default. One default per kind and
  tier (409 `CATALOG_DEFAULT_TAKEN`, checked under a per-organization lock);
- **limits** per parameter: hard; outside them a request is refused (422
  `CATALOG_OUT_OF_LIMITS`), not even IT's approval allows it. Inside the
  parameter's own range;
- **preapproved** range: inside the limits; a value outside it needs IT's
  approval whatever the rules say (`resource.out_of_range`, cut 3);
- **teams**: IAMKit group ids it is limited to (empty: everyone; at most 50).
  See "Teams" below.

Edits are not retroactive: resources keep their `recipe@version` and params.
Event `catalog.offering_updated` (audit trail; `teams` added within v1).

### Teams

An offering can be limited to some of the organization's **teams**, which are
IAMKit's generic groups (made by hand or synced from the company directory by
SCIM). Odicea reads them with its service account (`iam:members:read`) through
`catalog/adapters/catalogiam`, which takes the token from `releaseiam` (the
only holder of the service-account secret, `TestOneOwnerOfTheServiceAccountSecret`)
and caches a person's teams for a minute (the organization's list, which only
IT reads, is always fresh). Like `releaseiam`, any IAMKit error drops the
token (the SDK does not tell a 401 apart). `GET /catalog/recipes`, open to the
whole organization, carries an offering's team ids, never names or members.

- **Whose teams:** the person who asks to create (never read from the body).
  An approved request runs with the teams of who asked, read again when it
  runs. Projects have no team.
- **Only creating:** `Resolve`, `Available` and `Offered` filter by teams.
  Changing or growing what exists (`ResolveChange`) and moving an app to
  another tier (`Stranded`) do not: what exists keeps working for everyone.
- **Defaults:** the most specific wins — a team's default over everyone's. Two
  defaults clash on save (409 `CATALOG_DEFAULT_TAKEN`) only if both are for
  everyone or their teams overlap; someone in two teams that each have a
  default is asked to choose (`CATALOG_CHOOSE_RECIPE`).
- **Fails closed:** teams are read only when an active offering names any.
  Without the directory, offerings with teams are offered to nobody and naming
  a new team is 422 `CATALOG_TEAMS_UNAVAILABLE` (the teams already saved are
  kept on edit); a directory error is an error, never "no teams". A team new to
  an offering must exist (400); one deleted later stays and matches nobody.
- `GET /api/v1/catalog/teams` (`catalog:manage`) lists them for the console:
  `{available, items: [{id, name, members, directory}]}`.

## Resolving a request

`catalog.Queries.Resolve` turns "a `postgres` in a production app" into a
pinned recipe:

1. the recipe asked for, or the kind's default in the tier (several offered
   and none the default: 422 `CATALOG_CHOOSE_RECIPE` with the choices);
2. it must be offered in the tier (422 `CATALOG_UNAVAILABLE`) and the
   environment's cluster must announce every capability the version requires
   (422 `CATALOG_UNAVAILABLE` with `details.missing`; an agent from before the
   catalog only gets the basic recipes);
3. a pinned version that is no longer the offered one is 422
   `CATALOG_RECIPE_CHANGED` (an approval executed after IT changed the catalog);
4. params are normalized: unknown ones refused, defaults filled in, hard limits
   checked, `out_of_range` computed.

`ResolveChange` checks a change of params against the resource's pinned
version: an `immutable` param that changes or a `grows` one that goes down is
refused.

## Resources

`resource.resources` carries `recipe`, `recipe_version` and `params` (jsonb,
the source of truth). Migration `resourcepg/002_recipes` pins every existing
resource to `<kind>-basico@1` with `{"storage_gi": <size>}`; `storage_gi`
stays as a column in step with `params.storage_gi` (a CHECK) while consumers
move to `params`.

## MCP

What an assistant sees of the catalog is one environment's view,
`resource.Queries.Offered` (it writes nothing):

- **`list_catalog(app, environment?)`** (`projects:read`): the recipes the
  app's tier is offered and its cluster can honour (`catalog.Entry.Usable`,
  the same test as `Resolve`), each kind's default first; per recipe its
  params with hard limits, pre-approved range, default and mutability
  (`grows_only` / `fixed`), the variables the app receives (from the kind's
  contract) and `approval`: `instant`, `needs_approval` or `denied` — the
  verdict `Create` would give the caller for `create_resource`'s default —
  that recipe **with its defaults, connected to the whole app** (IT's rules
  under `create_resource`, the catalog's floor and the rules on
  `connect_resource`; a files volume in an app of several services, which
  must name one, is judged without the connection). A param outside the
  pre-approved range still asks IT. An archived app is refused, as `Create`
  refuses it. The kinds nothing
  fulfils are in `unavailable` with why (not offered in the tier, the
  cluster cannot, or no cluster that announces anything). Several defaults
  for one kind (`Resolve` would ask to choose) mark none. `can_create` says
  whether the caller holds `projects:write`. It is the caller's catalog: an
  offering limited to teams the caller is not in is left out (see "Teams").
- **`create_resource(app, kind?, name?, recipe?, params?, connect?, prefix?,
  services?)`** (`projects:read` + `projects:write`): `Commands.Create`, the
  same path as `POST …/resources`. It connects by default (`connect: false`
  creates it alone; `prefix`/`services` then are refused). Held → the usual
  `pending_approval` + `request_id`; catalog refusals (`CATALOG_*`,
  `RESOURCES_UNAVAILABLE`, an unknown recipe or param) carry a `next_step`
  that points at `list_catalog` (an agent too old for the recipe is told it
  is IT's to update) and says not to retry another value without asking.
- **`create_database`** stays as the short form: `create_resource` with the
  default recipe, always connected, `storage_gi` optional.

## Console (stage 1b)

The same view over HTTP is `GET …/environments/:environment/catalog`
(`projects:read`, `resource.Offered`: `verdict` is the effect, `rules` what
decided it, `rules_verdict` the effect without the catalog's own floor). The
console uses it in "Agregar a la app": only the kinds offered here, the option
to pick when IT offers more than one, each parameter inside the hard limits,
and next to the button whether it is created **al instante** or **pide
aprobación**. That mark is a forecast: `rules_verdict` (IT's rules on the
defaults), made stricter by a value outside the pre-approved range (always
approval) or the limits (refused) — not `verdict`, which a default outside
the pre-approved range would turn to approval for every value. The POST has
the last word (202 when held). The request sends `recipe` and `params`, never
the `storage_gi` shorthand. Limit errors word GiB as "GB" (`catalog.UnitLabel`).

**Administración → Catálogo** (`catalog:manage`) lists every recipe at its
offered version (with the versions this Odicea knows, `versions`), what it
fixes (`spec`) and the offering in sentences. Editing is the whole `PUT
…/offering`; a range equal to its outer one is sent as absent. A tier's
default already held by another recipe of the kind cannot be ticked (the
server's 409 `CATALOG_DEFAULT_TAKEN` otherwise; a team's default does not
clash with everyone's). The ranges shown are the
offered version's: choosing another version locks them until it is saved.
"Quién puede crearla" is Todos or Solo estos equipos (ticks over `GET
/catalog/teams`, with each team's people and whether it comes from the
company directory); without the directory the teams option is off unless the
offering already has some, which are kept. Ids never reach the screen: a team
deleted in IAMKit reads "un equipo que ya no existe".

IT's "Probar" (dry run) tries `create_resource` with the recipe the catalog
would resolve (the one picked among those offered in the tier, else the
tier's default) and `change_resource` with any recipe of the kind, offered or
not (a change uses the resource's pinned version, whatever the offering), and
a size: `resource.recipe`, `recipe_version`, `engine`, `params` and
`out_of_range` computed from the offering. A change also asks the size the
resource has now and, like `ResolveChange`, judges only the size: what the
catalog refuses before any rule (the same size, shrinking what only grows,
changing what is immutable, beyond the limits) is said under the field and
not tried. The other parameters go at their defaults (the dry run does not
know a real resource's), so a rule reading them sees those.
The dry run evaluates IT's rules only, so when the size is outside the
pre-approved range the console adds that the catalog's floor
(`catalog:preapproved`) asks for approval on its own.

"Cambiar tamaño" reads the resource's recipe offering from `GET
/catalog/recipes` (the limits and pre-approved range hold even when IT
withdrew it): beyond the limits it is refused in the form, beyond the
pre-approved range the button says "Pedir aprobación" (the PATCH answers 202).
It waits for the catalog before sending; a resource pinned to another version
than the offered one (its parameter may differ) is left to the server.

## Stage 1a cuts

1. Kinds as a registry of contracts (`pkg/resourcekind`).
2. This domain, the offerings, the pinned resources (this document).
3. IT's rules: `create_resource` / `change_resource`, `resource.*` CEL fields,
   approval payload v2 revalidated on execute; creation resolves through the
   catalog.
4. Proto + agent: the resolved spec per engine, capabilities announced.
5. MCP `list_catalog` / `create_resource`, skill, capabilities.
