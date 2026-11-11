# Security model

How Odicea decides who may do what, how it keeps secrets and builds apart from the control plane, what it records, and where its protections stop.

This page describes the current source. It is written for IT and for anyone who has to explain Odicea to a security review, so it names the gaps next to the guarantees.

## Three planes

```text
 browser / assistant ──HTTPS──► API (odyssey-api) ──► Postgres
                                  ▲                      ▲
                       worker (odyssey-worker) ──────────┘
                                  │
                       gateway (odyssey-gateway)   holds the fleet CA key
                                  ▲
                    mutual TLS, dialled out by the agent
                                  │
                  cluster: odyssey-agent ──► apps, builds, storage
```

- **Control plane**: the API, the worker and the database. It decides and records. It holds no credentials for any cluster and no cluster has to accept a connection from it.
- **Gateway**: the only process that holds the private key of the certificate authority that signs agents' certificates. The API has only the CA's public certificate, used to give agents a pin when a cluster is connected.
- **Data plane**: the agent and what it creates in the cluster. The agent dials out, so a cluster behind a firewall needs no inbound port.

The control plane and the agent share only a versioned protocol (`proto/fleet/v1`), and a test keeps the agent from importing the control plane's code.

## Identity

Odicea does not keep passwords. Sign-in and tokens come from IAMKit, the identity service.

- **The organization comes only from the token.** Every query filters by it, and a row of another organization is "not found", never "forbidden", so ids do not leak existence.
- **Tokens are checked online** with IAMKit on every request, with a five-second limit, and a check that cannot be made fails the request. A revoked session stops working at the next request.
- **Two kinds of token.** The REST API accepts only tokens issued to the console. A coding assistant gets a token that works for the MCP endpoint only, and only with the permissions the person has. An assistant therefore never has more power than its owner, and its token cannot call the console's API.
- **Permissions are fixed in a manifest.** Roles (TI, Builder, Lector) are bundles of named permissions with the prefix `odyssey:`. Every route is registered with the permission it needs, and a test checks that the permissions in the API contract (`api/openapi.yaml`) match the routes. See [Permissions](../reference/permissions.md).
- **The console is not a boundary.** It hides what you cannot do, but the API repeats every check.

## Secrets

A secret is a value an app needs but nobody should read: a password, a key, a token.

- **A person types it in the console.** The browser seals it to the *cluster's* public key (HPKE with X25519 and AES-256-GCM) before it leaves the page. The seal is bound to the cluster, the environment, the app, the name and the version, so a sealed value cannot be redirected to another app or replayed on an older version.
- **The API stores only ciphertext.** It cannot open it. The value is held until the agent acknowledges it (at most seven days) and then deleted. The private key is generated in the cluster and never leaves it.
- **An assistant never sees a value.** `request_secret` only declares the name and returns a console link. `set_env` refuses names that look like secrets (`PASSWORD`, `TOKEN`, `API_KEY` and others) and points to `request_secret`.
- **In the cluster** the agent writes the value into a Kubernetes Secret owned by the app and the pod reads it as an environment variable. The Secret goes when the app does. An app can instead read from the company's own secret store (External Secrets), which needs a separate permission, `odyssey:secrets:external`, held by TI.
- **AI keys** follow the same path: the control plane makes a key for the app's AI gateway and hands it to the cluster sealed. It never passes through a person or a log.

What this does not cover: once the value is in the app's environment, the app's own code can read it and print it, and so can anyone who can read Secrets in that cluster. See [Variables and secrets](../guides/variables-and-secrets.md) and [secrets.md](../secrets.md).

## Who may open an app

Protecting an app is done at the cluster's ingress, before a request reaches the app.

1. A request without a valid cookie is sent to Odicea's sign-in, and Odicea signs the person in with IAMKit (authorization code with PKCE). The return address must be on the app's own host.
2. Odicea answers with a ticket valid for 60 seconds, signed for that app's host, environment and access mode. It signs only if the mode asked for is the one it last told the cluster, so a stale request cannot get a ticket for a mode the app no longer has.
3. The gate in the cluster verifies the ticket and sets a cookie valid for 8 hours that is bound to host, environment and mode.
4. For every request the gate allows, it adds identity headers and a short signed assertion (valid 120 seconds). An app that makes decisions about people must verify the assertion, because a pod inside the cluster can call the app directly and invent the plain `X-Odicea-*` headers. See [Identity in your app](../guides/identity-in-your-app.md).

The gate's used-ticket memory is per pod, so the gate runs as a single replica; with more, a ticket could be redeemed once per pod during its 60 seconds.

The gate fails closed. If it has no valid keys (it keeps cached ones for at most an hour when the control plane is unreachable), or a request is malformed, the person is refused. A protected app is refused on a cluster that does not announce the access capability. "Cualquiera con el enlace" (public) needs a permission only IT has, and by default asking for it needs IT's approval. See [Who can open it](../guides/who-can-open-it.md) and [access.md](../access.md).

## IT's rules fail closed

IT's rules are evaluated by a sandboxed expression engine (CEL) with a cost budget and a 100 ms limit per rule. The strongest effect wins: deny beats approval, and approval beats allow.

A rule that cannot be evaluated (an error at run time, over budget, or no longer compiling) counts **at its own strength**: a broken *deny* still denies, a broken *approval* still asks, and a broken *allow* is ignored because an allow never opens a door. Saving a rule that does not compile is refused.

Every action that changes who can reach an app or what it costs goes through the rules: publishing, going back, changing access, creating and connecting storage, enabling AI, changing parts. The rules and the permissions are separate checks. Holding a permission does not skip a rule.

Organizations are born with built-in rules. For example, publishing to production and making an app public ask for approval, and publishing a production app with critical vulnerabilities is denied. Two more, "Producción exige revisión de vulnerabilidades" and the one for changing a production app's parts, are off until IT turns them on. See [IT's rules](../guides/it/rules.md) and [Rule input](../reference/rule-input.md).

## Approvals

- Only people who hold the permission to decide can approve. **Nobody approves their own request.**
- An approval is used once and is tied to the action, the place and the exact payload it was requested for. It cannot be reused for something else.
- When someone approves, the action runs again *as the requester*, with the permissions the requester had when they asked. Approving therefore never widens what the requester could do.
- Requests expire after seven days. The requester can cancel; IT can reject.

Gap: the requester's permissions are a snapshot. Odicea cannot ask the identity provider whether a user was deactivated after asking, so within the seven days a deactivated user's approved request still runs. The re-run does evaluate IT's rules again, so a deny rule added in the meantime wins, and IT can reject the pending request. See [Approvals](../guides/it/approvals.md) and [approvals.md](../approvals.md).

## Scanning

Every image Odicea builds is scanned for known vulnerabilities in the customer's own cluster, right after the build, and the result feeds the rules and the "Seguridad" panel.

Gaps, stated plainly:

- A scan is a check for *known* issues in packages. It does not review the app's own code.
- An image published with `deploy_image` is not built by Odicea and is not scanned.
- A cluster whose agent does not announce `odicea.scan`, or a scan that fails or times out, produces "unknown". The builtin deny rule does not match an unknown scan. Because a builder controls the image and can make a scan fail, that rule alone can be bypassed. The rule "Producción exige revisión de vulnerabilidades" makes IT approve those cases, and it is off by default.

## Egress

IT chooses, per cluster, what apps can reach. The agent writes Kubernetes NetworkPolicies in each app's namespace and rewrites them if somebody edits or deletes them (checked every two minutes).

- **`internet` mode (the default)** lets apps reach public addresses, but not private ranges, the cloud metadata address or the cluster's own nodes. **`restricted` mode** lets them reach only DNS, their own storage, the sign-in gate and the addresses IT lists.
- Apps accept traffic only from the ingress controller and their own namespace. Databases and caches accept connections only from their own app's namespace and the database operator.

Gaps:

- **The network plugin does the enforcing.** On a cluster whose network plugin ignores NetworkPolicy, the objects exist and nothing is blocked. The agent cannot tell, so the console says "Aplicada en el clúster" when policies were written, not when traffic was proven closed. Test it once on a new cluster.
- **A managed cluster's public Kubernetes API address** is a public address, so `internet` mode does not close it. Use `restricted` mode there. There is no deny list.

See [Egress](../guides/it/egress.md) and [egress.md](../egress.md).

## Audit

Every change publishes an event in the same database transaction as the change, and a subscriber turns each event into an entry in the company's log, "Registro". The entries of an organization form a **hash chain**: each entry's SHA-256 covers its own content and the previous entry's hash. The console runs a verification and shows "El registro fue alterado" at the first entry that does not match. Only TI can read the log.

Be precise about what this gives you. The chain is *tamper-evident*: editing or deleting an entry in place breaks verification. It is not *tamper-proof*. There is no key and no copy outside the database, so someone with write access to the database could recompute the whole chain from the point they changed. Treat database access as privileged, and export the log if you need an independent record.

The log also records where an action came from (console, GitHub, an assistant) and by whom, but the name is read from the directory when you look, not stored in the hash. See [Inventory and audit](../guides/it/inventory-and-audit.md).

## Build isolation

An app's build runs the app's own code (install scripts, build commands), so it is treated as untrusted.

- Each build is a one-off Job in its own namespace with no service account token. The only credential in the pod is a one-time token for fetching its source from the agent.
- BuildKit runs rootless as an unprivileged user. Nothing else runs in that namespace.
- A network policy lets the build reach DNS, the agent's source server and the public internet (for base images and packages), and **no private address**: not the Kubernetes API, other apps, databases or the cloud metadata address. Nothing can reach into a build pod.
- Builds are limited to 25 minutes, and the source is verified against the digest the control plane sent.

Gaps: the build namespace needs the "privileged" Pod Security level, because rootless BuildKit needs relaxed seccomp and AppArmor profiles to create user namespaces. That is why it is a namespace of its own. A build can also send anything it can read, including the app's source, to the public internet.

## The cluster connection

- A cluster is connected with a one-time enrolment token that lives 24 hours and is stored only as a hash. The agent trades it, with a certificate request, for a client certificate. The private key never leaves the cluster.
- Agents pin the CA, so they trust only a gateway whose chain ends at it. Certificates last 30 days and the agent renews them well before they expire.
- Every call except enrolment and recovery needs a verified client certificate, and the gateway checks the cluster in it. A cluster can fetch only the sources of its own builds.
- IT can revoke a cluster. Apps on it can be moved to another active cluster.

## What this model does not protect against

- A person with the permission to publish who puts malicious code in an app. Rules, approvals, the scan and egress limit what it can reach, but they do not read it.
- An attacker with write access to the database or to the cluster's Kubernetes API. Odicea is not designed to defend a cluster from its own administrators.
- A compromised identity provider. Odicea trusts IAMKit's answers about who someone is and what they may do.

## See also

- [Concepts](concepts.md)
- [Life of a version](life-of-a-version.md)
- [Known limitations](../project/known-limitations.md)
- [Architecture](../architecture.md)
