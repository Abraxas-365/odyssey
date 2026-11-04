# Quién puede abrirla — the sign-in gate (data plane)

Cut 1e. An app's **access mode** decides who may open it:

| mode | who | note |
|---|---|---|
| `team` | users of the app's organization (`app_org`) | in IAMKit a team is an org |
| `organization` | any user of the IAMKit environment | any org |
| `public` | anyone with the link | `""` means public too, **only** for revisions created before 1e; the control plane always sends an explicit mode |

## Wire

`proto/fleet/v1/fleet.proto`: `App.access = 12` (`Access{mode}`). The agent
maps it to `OdysseyApp.spec.access{mode, organization}` (`organization` is the
app's organization id, from `App.organization_id`).

## Agent (Traefik today)

Behind the small `Exposure` port (`agent/controllers/app`), for `team` and
`organization` the agent creates, owned by the OdysseyApp, two
`traefik.io/v1alpha1` Middlewares in the app's namespace and annotates the
Ingress `traefik.ingress.kubernetes.io/router.middlewares`:

1. `<app>-odicea-strip`: sets `X-Odicea-User|Email|Name|Org` and their
   underscore spellings (`X_Odicea_*`) to `""` (Traefik removes a header set to
   empty), so a client cannot spoof them.
2. `<app>-odicea-access`: `forwardAuth` to
   `http://odyssey-access.odyssey-system.svc:8098/verify?env=<env>&org=<app org>&mode=<mode>&host=<app host>&scheme=<http|https>[&port=<n>]`
   with `trustForwardHeader: false` and the four `X-Odicea-*` headers copied
   from the answer.

The cookie `__odicea_access` is **not** removed from the request to the app:
`Cookie` is a single header and Traefik's headers middleware cannot drop one
cookie from it. It reaches the app, but it is an HMAC-signed value bound to
{host, env, mode, app_org}: useless on any other app or to forge anything.

`public` deletes both and the annotation (only when they exist: a public app
costs no DELETEs). A public app never keeps the `router.middlewares`
annotation, even when the gate is disabled on the agent (it would make Traefik
answer 404).

The Middlewares are watched (owned) when `traefik.io` exists at agent start
(otherwise the manager could not start); a settled protected app is also
re-checked every 10 min, which covers Traefik installed later. This adds
`list;watch` on Middlewares to the agent's RBAC.

If the Ingress update is rejected as invalid, the existing Ingress is deleted
too (the app reports `failed`/`config`), never left unprotected. Order on every pass of a protected app: Protect the
Middlewares → update the existing Ingress (the annotation) → Deployment/Service.
**Fail closed**:

- a protected app in a cluster without the gate or without `traefik.io` is not
  exposed (its Ingress is removed) and reports `failed` / `config` with
  "Este clúster no puede proteger apps con inicio de sesión. Pide a TI que lo habilite.";
- any error while applying protection (Middlewares, Ingress) deletes the
  existing Ingress before the error is returned, and the pass is retried.

### Host binding (no host spoofing)

The host the gate trusts is the one **the agent wrote** in the forwardAuth
address (`host=`, from `Config.host`, lowercased; `scheme=` and `port=` from the
agent's `URLScheme`/`URLPort`). Traefik with `trustForwardHeader: false` sets
`X-Forwarded-Host|Uri|Method` itself from the real request. The gate requires
`X-Forwarded-Host` (lowercased, port stripped) to equal `host=`, else `403`, and
it takes the cookie binding, the ticket `aud` (`host[:port]`) and the return URL
from the query, never from request headers. So a cookie of app B is refused on
app A, and a forged `X-Forwarded-Host` gets `403`. A question without `host`,
`scheme`, a known `mode`, `env`, `org`, or without `X-Forwarded-Method`, is `403`
(a missing method is **not** taken as GET).

### Capability and old agents

The agent lists the string `odicea.access` in `capabilities.apis` (hello) only
when `ODYSSEY_ACCESS_ENABLED=true`, the gate configuration is valid
(`VerifyURL` an http(s) URL, key Secret named) **and** the cluster has
`traefik.io`. The control plane keeps it in `fleet.Capabilities.APIs`;
`fleet.CapabilityAccess` / `Capabilities.CanProtect()` name it. **The control
plane refuses a protected app on a cluster that does not list it** (an agent
older than 1e never does, and would ignore `App.access` and serve the app
open): `fleetsvc.Apply` answers a Business error `ACCESS_UNAVAILABLE`
("Este clúster no puede proteger apps con inicio de sesión. Pide a TI que lo
habilite."), which `releasefleet` turns into `APPLY_FAILED` with that message,
so the deployment fails at `apply` and nothing is exposed. `public` is never
refused. The agent also fails closed on its side.

### Same-revision changes

`desired.Apply` ignores a **lower** revision. An **equal** revision is applied
when its desired spec differs from the cluster's (so an `access` change cannot
be lost); an equal revision with the same spec is a no-op. The control plane
bumps the revision on an access change anyway (every settings change is a
`config` deployment, and every deployment gets a new revision), so rollouts and
reports stay unambiguous.

The status records what it describes: `status.observedGeneration` next to
`observedRevision`. A same-revision spec change bumps `metadata.generation`, so
the old status (even a final `failed`/`config`) is stale: the agent re-observes
and rewrites it, and `desired.statusOf` does not report a status whose
generation is not the spec's.

## Configuration

| where | variable | meaning |
|---|---|---|
| agent | `ODYSSEY_ACCESS_ENABLED` | `true`: the gate is installed (a boolean, not the URL) |
| agent | `ODYSSEY_ACCESS_VERIFY_URL` | gate's `/verify` as Traefik reaches it (default `http://odyssey-access.odyssey-system.svc:8098/verify`) |
| agent | `ODYSSEY_ACCESS_SECRET` | name of the cookie-key Secret (default `odyssey-access`) the agent creates |
| gate | `ODYSSEY_ACCESS_ISSUER` | control plane **public** API base URL: the `iss` claim and where browsers are sent. **https in production** |
| gate | `ODYSSEY_ACCESS_JWKS_URL` | optional server-side keys URL (default `<issuer>/.well-known/odicea-access-keys`) |
| gate | `ODYSSEY_ACCESS_INSECURE_ISSUER` | `true` allows an http issuer/JWKS that is not local. Without it, http is accepted only for `localhost`, `127.0.0.1`, `*.localhost`, `host.k3d.internal` |
| gate | `ODYSSEY_ACCESS_HEALTH_LISTEN` | liveness port (default `:8097`); the verify port `:8098` accepts only Traefik |

k3d (`deploy/k3d/agent`, a strategic-merge patch by container/env name): issuer
`http://localhost:8095`, JWKS
`http://host.k3d.internal:8095/.well-known/odicea-access-keys`.

### Install (`agent/install/access.yaml`)

- **Cookie key**: the **agent** creates the Secret (32 random bytes, never
  replacing an existing one), so the gate's Role is a single `get` on that
  Secret by name. The gate waits for it at start.
- **NetworkPolicy**: the verify port accepts ingress only from Traefik (default
  selector: namespace `kube-system`, pods `app.kubernetes.io/name=traefik`, as
  k3s); override it in your overlay for another install. The health port is open
  for the kubelet.
- **`replicas: 1`**: the replay cache (used ticket ids) is in memory, per pod.
  With more replicas a ticket could be redeemed once per pod during its life.
  The ticket is single-use per gate pod plus its short lifetime (<= 5 min, 60 s
  in practice). Keep 1 until the cache is shared.
- Agent RBAC on Middlewares: `get;list;watch;create;update;patch;delete` (list/watch for the watch above).

## The gate: `odyssey-agent access` (`agent/access`)

`GET /verify?env&org&mode&host&scheme[&port]` (forwardAuth; uses `X-Forwarded-Host|Uri|Method`):

- cookie `__odicea_access` valid and bound to {host, env, mode, app_org} → `200`
  with the identity headers;
- `X-Forwarded-Uri` starts with `/_odicea/callback` → verifies `ticket`, answers
  `302` to `return` (a path starting with `/`, not `//`; else `/`) made
  absolute on the app's public URL (`scheme://host[:port]` from the query:
  Traefik resolves a relative `Location` from forwardAuth against the gate's
  in-cluster address, which browsers cannot reach), with the
  cookie (host-only, `Path=/`, HttpOnly, `SameSite=Lax`, `Secure` on https, 8 h,
  HMAC-SHA256 with the key of Secret `odyssey-access`/`cookie-key`; name and
  email are truncated so the value stays under 3800 bytes);
- otherwise `GET`/`HEAD` → `302` to
  `<ISSUER>/access/authorize?env=<env>&mode=<mode>&return=<url-encoded original absolute URL>`;
  other methods → `401`; a malformed question → `403`.

`mode` in the redirect is a question, not a command: the control plane signs a
ticket only if `mode` is the one it **last told the cluster** for that
environment (`AccessTarget.Mode`: the settings revision recorded when a
deployment's apply step handed the app over; see "When a change fails" below).
A public app has no sign-in, and a gate that still asks for the old mode while
a change rolls out gets the generic error page for those seconds (fail closed)
instead of a ticket for a mode the app no longer has. The gate's own check that
the ticket's `mode` equals the app's mode stays the real enforcement.

JWKS: fetched outside any lock (one fetch at a time, shared by waiters) with a
5 s timeout and **no redirects**. Inside the 10 s minimum gap an unknown `kid`
is refused at once.

## Ticket contract (the control plane signs these — implement exactly)

After sign-in and the access decision, the control plane redirects the browser to

```
<scheme>://<app host>/_odicea/callback?ticket=<JWT>&return=<relative path>
```

JWT, header `{"alg":"EdDSA","typ":"JWT","kid":"<id>"}`, claims:

| claim | value |
|---|---|
| `iss` | the issuer, exactly as the gate is configured, **without a trailing `/`** (both sides trim it) |
| `aud` | string: `host[:port]` of the app's URL, host lowercased, the port only when the URL has one (the gate builds it from its `/verify` query: `host`, and `port` when the agent was given `ODYSSEY_APPS_URL_PORT`) |
| `sub`, `email`, `name` | the user |
| `org` | the user's organization id |
| `app_org` | the app's organization id (= `org` query param of `/verify`) |
| `env`, `mode` | environment id; `team` or `organization` (= the `/verify` query) |
| `iat`, `exp` | Unix seconds; `exp - iat <= 300` (30 s leeway on both); use 60 s |
| `jti` | unique; the gate accepts each once |
| `typ` | `odicea-access-ticket` |

For `team` the gate also requires `org == app_org`. Keys, at
`GET <ISSUER>/.well-known/odicea-access-keys`:

```json
{"keys":[{"kty":"OKP","crv":"Ed25519","x":"<base64url>","kid":"…","use":"sig","alg":"EdDSA"}]}
```

The gate caches the set (`jwksMaxAge` = 10 min) and refetches on an unknown
`kid`, at most every 10 s. If refreshing keeps failing it serves the cached
keys for at most a **hard max age of 1 h** since the last good load; after that
every ticket is refused until the issuer answers (a rotated-out or revoked key
does not outlive an unreachable issuer).

### Requirements on the control plane (implemented: `internal/access`)

Where each lives: the mode reaches the cluster in `DesiredApp.Access`, resolved
from the environment's settings *when the deployment applies* (so a change made
while a deploy is in flight is not lost; `release.Settings`, revision 0 =
`team`). The three routes are public (`accesshttp`); the organization is found
from `release.AccessLookup` (the environment's record), never from the request.
Ticket signing is `accessjwt` (EdDSA; the key is `ODYSSEY_ACCESS_SIGNING_KEY[_FILE]`,
old ones in `ODYSSEY_ACCESS_PREVIOUS_KEYS` stay in the published set). The sign-in
is on only with the key and the IAMKit client `IAMKIT_ACCESS_CLIENT_ID/_SECRET`;
otherwise the routes answer 404 `ACCESS_NOT_CONFIGURED`.

- `GET /access/authorize?env&mode&return` **validates `return`**: absolute, the
  same scheme, host and port as the environment's live URL (an explicit default
  port, `:443` on https / `:80` on http, equals none; the ticket's `aud` and
  the redirect use the URL's own authority), https unless a development
  host, no userinfo (an open redirect otherwise). Every refusal on `/access/*`
  (authorize and callback) is the same Spanish page — «No pudimos abrir esta
  app. Vuelve a intentarlo desde su enlace.» — so an unknown environment, a bad
  host and a public app look alike; the specific code goes only to the server
  logs. The flow state travels sealed
  (AES-256-GCM with a key derived from the signing key secret) in the HttpOnly cookie
  `__odicea_flow` (10 min); whether the user satisfies `mode` for the app's
  organization is decided at `GET /access/callback` against the environment's
  *current* records (the host is checked again), and that `mode` is what gets signed.
- **The sign-in with IAMKit** is authorization code + PKCE (S256) with `state`
  and a `nonce` (IAMKit requires both). Verifier, state and nonce live in the
  sealed flow. At the callback the ID token from the token endpoint must carry
  that nonce, name this client in `aud` (and in `azp` when `aud` has several),
  and its `sub` must equal the `userinfo` subject. A flow without a nonce is
  refused before the code is exchanged. The ID token's signature is not checked:
  it comes straight from the token endpoint on a request authenticated with the
  client secret (OIDC Core 3.1.3.7, step 6). That needs TLS, so **`IAMKIT_URL`
  must be https** unless its host is local or private (`localhost`, a
  single-label service name, `*.svc`, `*.svc.cluster.local`, `*.internal`) or
  `ODYSSEY_ACCESS_ALLOW_HTTP=true`; otherwise the API refuses to start.
- **The ticket travels in the query string** (it can land in logs and browser
  history). Mitigations: single-use (`jti`), 60 s life, and the gate redirects
  right away to `return` without it.
- **The issuer must be https in production** (the gate refuses a non-local http
  issuer unless `ODYSSEY_ACCESS_INSECURE_ISSUER=true`).
- **Key rotation**: keep the previous key published for at least `jwksMaxAge`
  (10 min) after switching the signing key, so gates with a cached set still
  verify; a new `kid` is picked up by the gate on first use. Assumes the clocks
  of the control plane and the gate agree within the 30 s leeway.

### When a change fails (what is "applied")

`environment_states.applied_settings_revision` is the settings revision the
cluster was last told. The deploy workflow's apply step sets it the moment
`Cluster.Apply` succeeds, **before** the app is ready and whether or not the
deployment then goes live. Three consequences, on purpose:

- **No automatic reopening.** A `config` deployment that fails after apply
  (the rollout never gets healthy) is not rolled back to the previous mode and
  does not start another: the cluster was told the new mode and keeps it. Being
  closed by mistake is a nuisance; being open by mistake is a leak. The failed
  deployment shows in Versiones with its error (a new version is the way out
  of an unhealthy app); the console shows the change as "aún no llega a la app"
  only while the latest revision is higher than `applied_settings_revision`,
  i.e. while no apply has told the cluster yet (a config deployment refused at
  apply: asking again retries it). Asking for the mode the cluster already has
  changes nothing.
- A deployment refused **at** apply (for instance `ACCESS_UNAVAILABLE`: the
  cluster has no `odicea.access`) never reached the cluster, so it records
  nothing and gets no auto-rollback either (there is nothing to restore).
- The sign-in trusts that revision, not the latest settings: a change still
  waiting for its deployment does not change who can sign in. For environments
  with nothing recorded yet it falls back to the latest settings.

Apps published before this feature were served open (their agents had no gate).
Migration 008 records that as settings revision 1, `public`, no author, for
every environment with a release online and no settings row, so nothing
changes silently and the console says «Cualquiera con el enlace». New
environments default to `team`. `GET .../settings` also reports
`access_protection_available`: whether the environment's cluster lists
`odicea.access` (an unplaced environment is judged by the organization's
default cluster, `false` with none). When it is false the console warns and
only offers `public`.
