# People, organizations and roles

Odicea has no user database of its own. IAMKit, the identity server, holds your people, organizations, roles and groups. This page explains how they map to what a person can do in the console, and what to do in IAMKit to add or change someone.

## How identity works

- **Sign-in.** The console sends people to IAMKit's hosted sign-in (OAuth authorization code with PKCE). IAMKit returns a token that carries the person's organization and permissions. Odicea checks each request against IAMKit and trusts nothing the browser claims about itself.
- **Organization.** Everything in Odicea belongs to one IAMKit organization: apps, clusters, rules, the audit log. The organization comes only from the token, never from a URL or a request body, so one organization cannot read another's data.
- **Permissions.** Odicea defines 17 permissions with the prefix `odyssey:` (for example `odyssey:deployments:create`). They live in an IAMKit resource, and IAMKit groups them into roles. A person's token carries the permissions of the roles they hold in that organization.
- **Names.** The audit log and version history show people's names. Odicea reads them from IAMKit with a service account (`odicea-backend`, with `iam:members:read`). Without it the names are empty and the API logs a warning. See [Configuration](../../reference/configuration.md).

## Roles

Odicea ships three roles in `iam/permissions.json`. The provisioning script creates them in IAMKit (see [Install Odicea](../operations/install.md)).

| Role | For | What it can do |
| :--- | :--- | :--- |
| **TI** | Ana Torres, who runs infrastructure at Andes Logística | Everything: it holds all 17 permissions. It connects clusters and GitHub, writes the IT rules, decides approvals, manages the catalog, and reads the inventory and the audit log. |
| **Builder** | Diego Ramos, who builds apps | Create, edit and archive apps; publish; roll back; delete the databases, caches and files of an app (6 permissions). |
| **Lector** | Lucía Paredes, who only needs to look | See apps, their environments and what is online (2 permissions). |

The exact permissions of each role, and what each one allows, are in [Permissions](../../reference/permissions.md).

> [!NOTE]
> The console shows a role name next to the person's name only when the person's permissions match one of the three roles exactly. If you give someone a custom set of permissions, no role name appears. Their permissions still apply.

What a person sees in the console follows their permissions:

| Area | Needs |
| :--- | :--- |
| Inventario | `odyssey:inventory:read` |
| Registro | `odyssey:audit:read` |
| Administración → Clústeres | `odyssey:clusters:read` (and `odyssey:clusters:manage` to change) |
| Administración → GitHub | `odyssey:integrations:manage` |
| Administración → Reglas | `odyssey:policy:manage` |
| Administración → Catálogo | `odyssey:catalog:manage` |
| Deciding approvals | `odyssey:approvals:decide` |

The Administración tab appears only for someone who can open at least one of its sections.

### Changing the roles

Edit `iam/permissions.json` (the permission catalog and the roles), then sync it with `make iam-provision` (see [Install Odicea](../operations/install.md)); on a local stack `make iam-sync` does the same. The script is idempotent: it adds what is missing and keeps what is no longer in the file unless you run it with `PRUNE=1`.

IAMKit puts permissions into a token when it is issued. After a change, people must sign in again to get the new ones. If a person sees "FORBIDDEN" on a page they should open, a stale token is the usual cause.

## Teams

The word "team" appears in two places in Odicea. They are not the same thing.

| Where | What a team is in IAMKit |
| :--- | :--- |
| App access "Solo tu equipo" | The person's **organization**. The people who can open the app are those of the app's own organization, once signed in. |
| Offerings limited to teams (**Catálogo**) | An IAMKit **group** of the organization. |

### "Solo tu equipo" and "Toda la empresa"

An app's access mode decides who may open it ([Who can open it](../who-can-open-it.md)):

| Console | Who opens the app |
| :--- | :--- |
| "Solo tu equipo" | People of the app's organization, signed in. For the gate, a team **is** an IAMKit organization. |
| "Toda la empresa" | Any person with a sign-in in the same IAMKit environment, from any organization. |
| "Cualquiera con el enlace" | Anyone. It needs `odyssey:access:public`, which only TI has. |

So, to let a different group of people open an app with "Solo tu equipo", they must be members of that organization in IAMKit. Odicea does not check IAMKit groups for this. The sign-in is described in [Access contract](../../access.md).

### Limiting an offering to teams

In **Administración → Catálogo** ([Catalog](catalog.md)) an offering can be offered to everyone (**Todos**) or only to certain teams (**Solo estos equipos**). The teams are the organization's IAMKit groups, either created by hand or synced from your company directory (SCIM). The console lists each team with its people and whether it comes from the directory.

Rules, from the [catalog contract](../../catalog.md):

- Teams are read with the `odicea-backend` service account and cached for a minute.
- The teams that count are those of the person who asks to create the resource. An approved request uses the teams of the person who asked.
- Teams only limit **creating**. What already exists keeps working for everyone.
- It fails closed: if Odicea cannot read the directory, an offering limited to teams is offered to nobody, and you cannot name a new team (`CATALOG_TEAMS_UNAVAILABLE`). If a team is later deleted in IAMKit, the offering shows "un equipo que ya no existe" and matches nobody.
- When two offerings of the same kind have a default, a team's default wins over the default for everyone.
- An offering can name at most 50 teams.

Creating a group and adding people to it is done in IAMKit, not in Odicea.

## Add a person

Odicea has no invitation page. Do it in IAMKit's console for your organization, which has two parts:

1. **Invite or add the person.** In the organization, open **Members → Invitations** and invite their email address with the roles they should have. IAMKit sends the invitation through its email delivery (a webhook, or an email it writes through SMTP or Resend). If delivery is not configured, the console shows the invitation token once, for you to share by hand. A new person chooses a password; an existing user just joins. The person is then a member of that organization.
2. **Give them a role.** Assign **TI**, **Builder** or **Lector** to the person (a direct assignment), or assign the role to a group and add the person to the group. A member's roles from a group show as `role via Group`. Adding a membership does not grant any permission by itself.

Menu names in IAMKit can differ by version; check them against your IAMKit's own documentation. What Odicea depends on is the organization membership, the role assignment and, for the Catálogo, groups.

**Verify:** ask the person to sign in at the console. The avatar menu shows their name and role. A Builder sees no **Inventario**, **Registro** or **Administración**.

> [!NOTE]
> To confirm a new role, have the person sign out and in again. The console token is issued with the permissions at that moment.

## Remove a person

Remove the person's role assignments and their membership in IAMKit. Remove the **membership** of that organization, rather than suspending the user globally, unless that is your policy. Odicea checks the console token with IAMKit on every request, so a token IAMKit has revoked stops working on the next one.

Apps the person created stay. The audit log keeps their entries.

## Switch organization

A person who belongs to more than one organization works in one at a time. The console token names that organization. To change, open the avatar menu and choose **Cambiar de organización**. The console clears what it loaded, ends the session and sends the person to IAMKit's sign-in again. The new token names the organization they sign in to. How IAMKit chooses it for a person in several organizations is IAMKit's behaviour, and this page does not cover it. Apps, clusters and the other data they then see are those of the new organization.

## See also

- [Permissions](../../reference/permissions.md)
- [Who can open it](../who-can-open-it.md)
- [Catalog](catalog.md)
- [Rules](rules.md)
- [Approvals](approvals.md)
- [Inventory and audit log](inventory-and-audit.md)
- [Install Odicea](../operations/install.md)
- [Security model](../../explanation/security-model.md)
