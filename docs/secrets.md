# Variables y secretos — secrets (data plane)

Cut 2a. An app has plain variables (`App.env`, in the OdysseyApp) and secret
variables (`App.secrets`, **references only**: the spec never holds a value).
Each secret becomes an environment variable of the same name, read by the pod
from a Kubernetes Secret (`secretKeyRef`). A name in `secrets` replaces the
same name in `env`.

## Wire

`proto/fleet/v1/fleet.proto` (the contract; this page is the agent's side):

- `Capabilities.secret_key{key_id, public_key, suite}`: the cluster's HPKE key
  (`pkg/secretbox`). Sent only when the agent holds the private key. Without
  it the control plane cannot deliver managed secrets to the cluster.
- `Capabilities.secret_stores`: names of the cluster's `ClusterSecretStore`s
  (external-secrets.io/v1). Sorted; empty when there is no External Secrets
  Operator or the list is not permitted.
- `App.secrets[]`: `SecretRef{name, source, version, store, key, property}`.
  `managed`: the value is in the cluster, at `version`. `external`: read from
  `store` by the operator (`key`, optional `property`).
- `ControlMessage.secret` → `AgentMessage.secret_ack{delivery_id, error}`: the
  control plane delivers one managed value, sealed to the cluster key. The ack
  is always sent; `error` is empty when the value is stored (or already was).

## Cluster key (`agent/tunnel/secrets.go`)

`secret.key` in the `odyssey-agent` Secret (next to the identity). Generated
on first connect when missing, never replaced (a second replica's key loses
the race; an unreadable key is left alone and the capability is not
announced). The Hello carries `suite = secretbox.Suite`, `key_id = key.ID()`.

## Receiving a value (`agent/secretstore`)

The tunnel handles the message serially, before the next one, and acks:

1. ids are UUIDs, `app` a DNS label, `name` an environment-variable name;
2. `key_id` must be ours, else the ack error is `sealed for another cluster
   key`;
3. open with `secretbox.Target{Cluster: <our cluster id>, Environment, App,
   Name, Version}` (info `"odicea/secret/v1" NUL cluster NUL environment NUL
   app NUL name NUL version`): a value sealed for another cluster,
   environment, app, name or version does not open, so even the control plane
   cannot redirect it to another app's Secret. Errors never contain the
   value; it is zeroed after the write;
4. the namespace is created like an app's (`desired.EnsureNamespace`), then
   `<app>-secrets` (labels `app.kubernetes.io/managed-by=odyssey`,
   `odyssey.dev/app=<app>`) gets `data[name]` **only if the stored version is
   lower**. Versions are in the annotation `odicea.dev/secret-versions`
   (JSON `{"NAME": version}`). The same or an older version is a successful
   no-op (deliveries repeat and arrive late). Whenever a key is written its
   delivery time goes in the annotation `odicea.dev/secret-delivered` (JSON
   `{"NAME": "<RFC 3339, UTC>"}`; see the prune grace below);
5. a `<app>-secrets` that **lacks** `app.kubernetes.io/managed-by=odyssey` is
   someone else's: nothing is written and the ack error is `a Secret named
   <app>-secrets already exists in <namespace> and is not managed by
   Odyssey` (never the value).

## OdysseyApp (`spec.secrets`) and the reconciler

`desired.Valid` refuses (the app is reported failed): a bad variable name
(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`), repeated names, a source other than
`managed|external`, an external without store and key, a managed with
version < 1, more than 100.

- Managed refs read `<app>-secrets`. External refs read one Secret per store,
  `<app>-external-<store>`, which an **ExternalSecret** of the same name makes
  (`refreshInterval: 1h`, `creationPolicy: Owner`, `secretStoreRef`
  `ClusterSecretStore/<store>`, one `data` item per variable; `property`
  omitted when empty). ExternalSecrets are owned by the OdysseyApp; the ones
  the spec no longer needs are deleted.
- No ExternalSecret kind in the cluster and an external ref: failed, reason
  `config`, "Este clúster no tiene un gestor de secretos externos (External
  Secrets Operator). Pide a TI que lo instale."
- **Gate before the Deployment**: a managed secret needs its key with a stored
  version ≥ the wanted one; an external one needs its target Secret to hold
  the key. Missing → progressing "Esperando el secreto NAME", look again in
  3 s; the running Deployment is not touched. Still missing 2 minutes after
  the revision was first observed (the first moment is kept in the annotation
  `odicea.dev/secrets-waiting`, so a restart does not reset it) → failed,
  reason `config`: "Falta el secreto NAME: cárgalo en la consola" (managed) or
  "No se pudo leer el secreto externo NAME (revisa la referencia en el
  almacén STORE)" (external). A failed revision stays failed; the next
  revision is tried afresh.
- The pod template carries `odicea.dev/secrets` = hash of the sorted
  `(name, source, version, store, key, property)` list, so a new managed
  version or a changed reference rolls the pods.
- When the revision is **ready**, managed keys that the spec no longer lists
  are removed from `<app>-secrets` (with their versions and delivery times):
  the previous pods are gone by then. **Grace**: a key delivered less than 1 h
  ago (`secretGrace`) stays even if the spec does not list it. The control
  plane delivers a value as soon as a person loads it, but the revision that
  lists it can arrive later (deployments queue); without the grace a
  reconcile of the old Ready revision would delete the value, the new
  revision would fail "Falta el secreto" and nothing would send it again. A
  key with no recorded time (written before the annotation existed) or one
  dated in the future counts as old. While a key is kept for its grace the
  reconcile requeues for the time left (capped by the 10 min resync of
  protected apps), so the next pass prunes it. A key an old Ready spec
  dropped is pruned on the first pass.
- When pruning leaves `<app>-secrets` with **no key**, the Secret is deleted
  (with a UID/resourceVersion precondition, so a value delivered meanwhile is
  not lost; if one arrives later `Put` creates it again).
- **Owner reference**: the first reconcile (`adoptManaged`, before the gate)
  adds a plain owner reference OdysseyApp → `<app>-secrets` (not a controller
  reference: no block on deletion), so deleting the app deletes its values,
  also for a Secret written before the app existed. Idempotent.
- **Foreign Secret**: a `<app>-secrets` without the label
  `app.kubernetes.io/managed-by=odyssey` is never adopted, pruned nor
  deleted. An app that lists managed secrets fails with reason `config`:
  "a Secret named <app>-secrets already exists in <namespace> and is not
  managed by Odyssey" (the same message as a foreign ExternalSecret); one
  without managed secrets ignores it.

## Control plane (`internal/fleet`, `internal/release`)

- `fleet.secret_deliveries` holds one sealed value per (environment, app,
  name), the newest version only; the time it waits from is stamped by the
  service clock. It is deleted by the agent's ack, by the TTL (7 days: when
  the agent connects, and by the `fleet.secret_sweep` schedule every hour, so
  a cluster that never comes back leaves nothing), by revoking the cluster
  (nobody else can open it) and by moving the environment.
- A delivery that ends badly (ack error, TTL, revoke) is recorded in
  `fleet.secret_failures` (one per environment/app/name, the newest version)
  and published as `cluster.secret_failed`; a newer delivery or a stored
  version clears it. The settings show it as state `failed`.
- Versions: the next value of a name is
  `max(current, high-water) + 1` (`release.NextSecretVersion`). The high-water
  is `release.secret_versions (environment_id, organization_id, name,
  version)`, one row per managed secret: `AppendSettings` raises it
  (`GREATEST`, tenant-guarded) in the same transaction as every settings
  revision, and `ListSecretVersions` reads it (no scan of the history). It
  never goes down, even if the settings history is purged one day, so
  removing a secret and adding it again never reuses a number the cluster may
  still hold. Migration 009 backfills it from the existing revisions.

## RBAC

The agent holds **no access to Secrets cluster-wide**: it cannot read
`kube-system`'s or another team's.

- `odyssey-agent-secrets` (`agent/install/rbac.yaml`, ClusterRole):
  `secrets` **get/create/update/delete** (no list, watch or patch: Secrets
  are never cached by the agent, every read is a `get` by name through the
  API reader; `get` gates and prunes, `create`/`update` write deliveries and
  the owner reference, `delete` removes a Secret pruned empty). Nothing in
  the install binds it. `desired.Namespaces.Ensure` binds it with a
  RoleBinding of the same name in each environment namespace it creates (for
  the agent's service account, `ODYSSEY_SERVICE_ACCOUNT` from the pod spec)
  and waits until a SelfSubjectAccessReview says it applies (also when the
  binding already existed: a concurrent `Ensure` may have just made it); on
  start the leader binds it in every namespace it made before (`BindAll`:
  upgrades and deleted bindings; no wait per namespace, a reconcile that
  races it is retried). In the cluster the agent refuses to start without
  `ODYSSEY_SERVICE_ACCOUNT`; the policy names `odyssey-system/odyssey-agent`
  literally, and `agent/install_test.go` checks it matches the Deployment. A namespace `odyssey-<environment>` that exists without
  `app.kubernetes.io/managed-by=odyssey` is someone else's: the app (or the
  delivery) is refused and nothing is bound there.
- `odyssey-agent` (generated `agent/install/rbac/role.yaml`, bound
  cluster-wide) may `bind` exactly that role (`resourceNames`), `create`
  rolebindings, and `get/list/create` namespaces, never update or patch them
  (so it cannot label someone else's namespace as its own); plus
  `externalsecrets` create/delete/get/list/update and `clustersecretstores`
  list.
- `agent/install/policy.yaml` (ValidatingAdmissionPolicy, Kubernetes ≥ 1.30)
  denies any RoleBinding of `odyssey-agent-secrets` outside a namespace
  `odyssey-*` labelled managed-by=odyssey, or for any subject but the agent.
- The other Secret uses are namespaced `Role`s (the agent's own identity and
  the build tokens). `agent/install_test.go` fails if a ClusterRole bound
  cluster-wide grants `secrets` or wildcards, or the policy is missing.
