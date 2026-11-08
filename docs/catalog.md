# Resource catalog (stage 1a)

What an organization lets its people create as a resource is IT's call, not
a hardcoded list. The catalog has four pieces with four owners:

| Piece | Owner | Mutable? | Where |
|---|---|---|---|
| **Type** (`postgres`, `redis`, `files`) | Odicea | with the binary | `pkg/resourcekind` (the contract: variables, noun, settings) |
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
  approval whatever the rules say (`resource.out_of_range`, cut 3).

Edits are not retroactive: resources keep their `recipe@version` and params.
Event `catalog.offering_updated` (audit trail).

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

## Stage 1a cuts

1. Kinds as a registry of contracts (`pkg/resourcekind`).
2. This domain, the offerings, the pinned resources (this document).
3. IT's rules: `create_resource` / `change_resource`, `resource.*` CEL fields,
   approval payload v2 revalidated on execute; creation resolves through the
   catalog.
4. Proto + agent: the resolved spec per engine, capabilities announced.
5. MCP `list_catalog` / `create_resource`, skill, capabilities.
