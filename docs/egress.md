# Egress (cut 2d)

TI decides what the apps of a cluster may reach. The control plane stores the
choice per cluster, sends it to the agent over the tunnel, and the agent keeps
it as Kubernetes **NetworkPolicies** in every environment namespace. Nothing
else enforces it: the cluster's network plugin (CNI) does.

## What TI must know

- **The CNI must enforce NetworkPolicy.** k3s does (kube-router); so do Calico,
  Cilium and the managed offerings that enable it (GKE Dataplane V2, EKS with
  the VPC CNI network policy agent, AKS with Azure/Calico network policy).
  On a CNI that ignores NetworkPolicy the objects exist and **nothing is
  blocked**. The agent cannot tell, so the console says "Aplicada en el
  clúster" when the policies were written, not when traffic was proven closed.
  Test it once on a new cluster (below).
- **A default cluster is `internet`.** Apps reach the public internet, DNS, their
  own resources (Postgres, Redis) and the sign-in gate. They do **not** reach
  the private ranges (see below).
- **Namespaces are fenced before their first pod, when the agent starts and
  every 2 minutes.** A policy somebody deletes or edits comes back.
- **Ingress is fenced too.** An app's pods accept traffic only from the ingress
  controller (default: pods `app.kubernetes.io/name=traefik` in `kube-system`)
  and from their own namespace. A cluster with another controller sets the
  agent's `ODYSSEY_INGRESS_NAMESPACE` / `ODYSSEY_INGRESS_POD_LABEL` (and
  `ODYSSEY_DNS_NAMESPACE` / `ODYSSEY_DNS_POD_LABEL` for DNS,
  `ODYSSEY_OPERATOR_NAMESPACE` for CloudNativePG). Wrong values break the app
  (readiness fails) rather than leaving it open.
- **Resources are fenced.** Databases and caches accept connections only from
  the apps of their own namespace and from the database operator.
- **Old agents.** A cluster whose agent does not list the capability
  `odicea.egress` shows "Este clúster no puede aplicarla: actualiza su agente"
  (`state: unsupported`). The choice is stored and applies when it upgrades.

## The two modes

| Mode | Apps can reach |
|---|---|
| `internet` (default) | DNS, their namespace (resources), the sign-in gate (`odyssey-access:8099`), every public address, plus the allow list |
| `restricted` | DNS, their namespace (resources), the sign-in gate, plus the allow list only |

`allow` is a list of CIDRs (or single addresses) open to the apps in either
mode: an internal LLM gateway, a database on the company network, a partner API.

### What `internet` keeps closed

`internet` is `0.0.0.0/0` **except** the reserved ranges, and `::/0` except
theirs:

| Family | Excepted |
|---|---|
| IPv4 | `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (private), `100.64.0.0/10` (carrier-grade NAT), `169.254.0.0/16` (cloud metadata, 169.254.169.254), `127.0.0.0/8`, `0.0.0.0/8` (this network), `192.0.0.0/24` (IETF assignments), `198.18.0.0/15` (benchmarking), `224.0.0.0/4` (multicast), `240.0.0.0/4` (reserved) |
| IPv6 | `fc00::/7`, `fe80::/10`, `::1/128`, `64:ff9b::/96` (NAT64: it reaches IPv4 hosts), `ff00::/8` (multicast) |
| **Every node of the cluster** | the `InternalIP` and `ExternalIP` of each node, as `/32` or `/128`, when not already inside one of the ranges above |

The node addresses matter: a node's public (or otherwise unlisted) address would
expose its **kubelet (10250)**, the **API server** on a control-plane node's IP
and every **NodePort** to any app. The agent lists the nodes (RBAC: `list` on
`nodes`) and rewrites the policies at every 2-minute check, so a node that joins
is excepted within two minutes (a new namespace is fenced with the nodes known at
that moment). If the nodes cannot be listed the policies **stay as they are** and
the state is `failed` (the error says why) until the next check: the agent never
writes an internet rule it knows is open.

That covers the Kubernetes API (a service IP), pods and services of other
namespaces, node addresses and the cloud's metadata service. An address in those
ranges is reachable only if the allow list names it.

### Managed clusters with a public API endpoint

**Limit of `internet`.** A managed cluster (EKS, GKE, AKS and similar) often
exposes its Kubernetes API at a **public address** that is not a node and not
inside the reserved ranges. In `internet` mode that address is "public internet",
so an app **can reach it** (the in-cluster service IP, `kubernetes.default.svc`,
stays closed). An app still needs a valid token to do anything there (the pod's
service-account token, or any credential it holds), so this is not an open door,
but it is a network path the fence does not close. There is **no deny list**: the
`allow` list only adds exceptions, and the mode has no way to subtract one public
address. For such clusters **use `restricted` mode** and list what the apps need;
the endpoint is then unreachable unless TI names it. The agent cannot know the
endpoint's address, so it does not warn about it.

### Allow entries that include a node

An allow CIDR can contain a node's address (for instance `10.0.0.0/8` on a
cluster whose nodes live there), in either mode: an allow entry overrides the
exceptions. The entry is **kept** (TI asked for it), but the egress view carries
a **warning** in `warnings` (and the console a yellow "Revisa estas direcciones"
card): the apps will be able to reach that node's kubelet, its ports and, on a
control-plane node, the Kubernetes API. The agent sends the warnings with every
acknowledgement and again with each full report (every 5 minutes), so they follow
node changes; they show only while the policy is `applied`. The console also asks
for **confirmation** before saving an entry that is private or overlaps the
cluster's own ranges ("Esta dirección es interna del clúster o de tu red: las
apps podrán llegar a ella").

### The `0.0.0.0/0` decision

`allow` **refuses** `0.0.0.0/0` and `::/0` (400, field `allow`, with a message
that points to "Internet pública"). An allow entry is an exception; `0.0.0.0/0`
in it would silently reopen the private ranges `internet` keeps closed
(metadata, the API server). "Everything public" is the mode, spelled out;
"everything, private included" is not offered. TI who truly needs a private
range lists it (`10.20.0.0/16`).

## Contract

`GET /api/v1/clusters/{id}/egress` (`clusters:read`) and
`PUT /api/v1/clusters/{id}/egress` (`clusters:manage`): see `docs/api.md` §11
(Clusters). The body is `{mode, allow}`; the response adds:

| Field | Meaning |
|---|---|
| `generation` | grows with every change; 0 is the default nobody changed |
| `acknowledged_generation` | the last generation the agent answered for (`null` before any) |
| `error` | what the agent said when it could not apply that generation |
| `warnings` | what TI should review although it is applied (an allow entry that includes a node); at most 10 |
| `supported` | its agent lists `odicea.egress` |
| `state` | `applied`, `pending`, `failed`, `unsupported` |
| `enforced` | `state` is `applied` |

The change feed pushes the policy at once (and at every connection, right after
`Hello`); the agent answers with a `PolicyAck{generation, error, warnings}`, an
observation recorded without an event (stored in `fleet.clusters.policy_warnings`).
The agent repeats it with every full report (5 min): the 2-minute check may have
fixed or broken what the last ack said. The same setting twice changes nothing
and records nothing; a change records `cluster.egress_changed` (new and
previous mode and list).

Agent side: `agent/egress`. The policy in force is kept in the ConfigMap
`odyssey-cluster-policy` of the agent's namespace, so a restart enforces it
before the control plane answers, and a cluster that cannot reach the control
plane keeps its fence. **Without any policy received, the agent enforces
`internet`** (the default).

NetworkPolicies per environment namespace (label
`app.kubernetes.io/managed-by=odyssey-agent`):

- `odicea-apps`: selects the pods with the label `odyssey.dev/app`; ingress from
  the ingress controller and the namespace; egress as in the table above.
- `odicea-resources`: **positively** selects the pods with the label
  `odyssey.dev/resource` (Redis pods carry it from their template; CloudNativePG
  pods get it through the Cluster's `spec.inheritedMetadata.labels`, which the
  agent also adds to databases made before); ingress from the namespace's apps
  and the database operator.
- `odicea-default`: every other pod of the namespace (neither `odyssey.dev/app`
  nor `odyssey.dev/resource`, **nor `cnpg.io/cluster`**): ingress from its own
  namespace only; egress to DNS and its own namespace only. Selecting resources
  by label and closing the rest means a pod nobody labelled (a build, a debug
  pod, something new) is closed, not open: the old "everything that is not an
  app" selector failed open. CloudNativePG pods are excluded by the label the
  operator itself puts on them: a database made before the agent labelled its
  Cluster (`spec.inheritedMetadata`) has pods without `odyssey.dev/resource`
  until the operator rolls them, and `odicea-default` would cut the operator and
  the apps off from it in the meantime. They fall under `odicea-resources` once
  they carry the label (no gap in either direction).
- `odicea-ai`: only while an app of the namespace has `spec.ai`: selects the pods
  with `odyssey.dev/ai: "true"` and allows egress to the resolved addresses (and
  port) of the AI gateway, or, for an in-cluster Service, to the Service's
  namespace **on any port** (the rule has no `ports`: a Service's port is not the
  pod's `targetPort`). Rewritten when an app changes and every 2 minutes. If any
  gateway host does not resolve, `odicea-ai` is **not written at all** (it stays
  exactly as it was, even for the hosts that did resolve) and the state shows the
  failure. Removed when no app uses AI **and no running pod of the namespace still
  carries `odyssey.dev/ai=true`** (old pods mid-rollout still hold the key and keep
  their access; the next check removes it, and if the pods cannot be listed it
  stays). Pods without AI, and other namespaces, cannot reach it.

RBAC (least privilege; `TestEgressPermissionsAreTheLeastTheEnforcerNeeds`): the
enforcer reads NetworkPolicies by name straight from the API (no cache), so it
needs no `list` or `watch` and nothing cluster-wide on them; it gets `get`,
`create`, `update` and `delete` **only in environment namespaces**, through the
`odyssey-agent-environment` ClusterRole it binds per namespace
(`agent/install/rbac.yaml`; the admission policy lets it bind only that role) —
no `patch`. Cluster-wide it only lists `nodes`, and lists pods by label
(`odyssey.dev/ai`) with the read the controllers already hold. Its ConfigMap
(`odyssey-cluster-policy`, in `odyssey-system`) has its own Role
`odyssey-agent-policy`: `get`/`update` on that one name; `create` cannot be
restricted by name.

## Interactions

- **Cut 2c (sign-in gate)**: apps must reach `odyssey-access.odyssey-system:8099`
  to fetch the signing keys (JWKS). Always allowed, in both modes.
- **Cut 2b (resources)**: apps reach their Postgres/Redis in the same
  namespace, always. Another namespace does not.
- **Builds** have their own policy (`agent/install/builds.yaml`) and are not
  affected.
- **AI gateway (cut 2d-ai)**: the agent opens it for the apps that use AI and
  only for them (policy `odicea-ai`, below), in either mode and whether the
  gateway is public, internal or the local `host.k3d.internal`. TI lists
  nothing. Details: `docs/ai.md`.
- **Release "Lo que usa esta app" (cut 2e)** reads `Cluster.Egress(target)`
  (mode and whether it is enforced) and shows "Salida a internet" per app
  (`GET …/uses`, `get_app` → `uses.egress`). A `restricted` policy the agent
  has not confirmed is the attention `egress_not_applied` (who `wait` until
  the cluster says it cannot or failed to apply it, then `it`); `internet` never
  is (nothing to apply). An environment with no cluster has an empty mode.

## Checking a new cluster

From a pod of an app (or an ephemeral container with its labels):

```sh
curl -sS -m5 https://example.com                          # internet mode: works
curl -sS -m5 http://169.254.169.254/                      # blocked (timeout)
curl -sSk -m5 https://kubernetes.default.svc/             # blocked
curl -sS -m5 http://odyssey-access.odyssey-system.svc:8099/   # works
```

If the blocked ones answer, the CNI does not enforce NetworkPolicy.

## Local (k3d)

`make agent-image` redeploys the agent; k3d's k3s enforces NetworkPolicy with
kube-router.
