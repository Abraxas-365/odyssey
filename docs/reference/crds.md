# Custom resources

The Kubernetes custom resources Odicea's agent uses in every connected cluster: `OdysseyApp`, `OdysseyResource` and `OdysseyBuild`.

They are the agent's working copy of what the control plane wants. The control plane sends its desired state over the tunnel, and the agent writes these objects and reconciles them into ordinary Kubernetes objects. Nobody creates them by hand.

> [!IMPORTANT]
> Do not apply, edit or delete these objects yourself. The agent overwrites an `OdysseyApp` or `OdysseyResource` with the control plane's state, ignores a spec older than the one it holds, and an `OdysseyBuild` never accepts a change to its spec. To change an app, change it in the console or through an assistant, and Odicea sends the new state.

## Overview

All three belong to the API group `odyssey.dev`, version `v1alpha1`, and are namespaced. The CRDs install with the agent (`agent/install/crd/`).

| Kind | Short name | One object is | Namespace | Reconciled into |
|---|---|---|---|---|
| `OdysseyApp` | `oapp` | One app in one environment. | The environment's namespace, `odyssey-<environment id>`. | A Deployment per service, a Service per web service, an Ingress per public route. |
| `OdysseyResource` | `ores` | One database, cache, files space or company connection of an environment. | The environment's namespace. | A CloudNativePG Cluster, a Redis, a volume claim, or a connection Secret. |
| `OdysseyBuild` | `obuild` | One image build from uploaded source. | `odyssey-builds`. | A build Job, then a scan Job. |

All three carry the label `odyssey.dev/…` and the agent's `app.kubernetes.io/managed-by=odyssey-agent`. Useful commands for IT:

```sh
kubectl get oapp -A
kubectl get ores -A
kubectl get obuild -n odyssey-builds
```

The `CATEGORIES` column also lets you list all three with `kubectl get odyssey -A`.

## OdysseyApp

One app in one environment: what the cluster must run. It mirrors what the control plane sends (`release.DesiredApp`) field for field.

### Spec

| Field | Type | Meaning |
|---|---|---|
| `revision` | integer, at least 1 | Grows with every deployment of the environment. The agent never lowers it, so a superseded deployment cannot win late. |
| `services` | list, at most 8 | The app's workloads, in order. See below. |
| `image`, `port`, `healthPath`, `replicas` | | The single `web` service at `/` when `services` is empty (an object written before services existed). Ignored when `services` is set. |
| `env` | map | Non-secret configuration. |
| `labels` | map | Identify the app's objects (app, environment, version, deployment). The agent copies them to everything it creates. |
| `access` | `{mode, organization}` | Who may open the app: `public`, `team` or `organization`. Absent means public. The agent never exposes a protected app in a cluster that cannot protect it. |
| `secrets` | list, at most 100 | Secret variables as references only, never a value. `source` is `managed` (a value a person loaded in the console, sealed to this cluster, `version` at least 1) or `external` (read by External Secrets from a `store` and `key`, optional `property`). Optional `services` restricts which parts read it. The app does not start until every secret is available. |
| `resources` | list, at most 10 | Resources the app uses: `name`, `kind`, optional `prefix` for the variable names, optional `services`. The app reads their connection from Secrets the agent keeps, never from the spec. The app does not start until every resource is ready. |
| `ai` | `{baseURL, keySecret, services}` | Wires the app to the organization's AI gateway. `keySecret` is one of the app's managed secrets. Absent means no AI. |

A spec must have either `services`, or `image`, `port`, `healthPath` and `replicas` together.

Each entry in `services`:

| Field | Meaning |
|---|---|
| `name` | Lowercase letters and digits, starting with a letter, at most 20. |
| `kind` | `web` or `worker`. A web service needs a port. |
| `route` | The path of the app's address it answers (`/`, `/api`). Empty means internal (web) or none (worker). |
| `stripPrefix` | Removes the route from the path before the request reaches the service. Only for a route other than `/`. |
| `image` | Pinned by digest: `registry/repository@sha256:<64 hex>`. |
| `port`, `healthPath` | Port and readiness path of a web service. |
| `replicas` | 1 to 20. |
| `env` | Variables of this service only. They win over the app's `env`. |

### Status

| Field | Meaning |
|---|---|
| `observedRevision` | The spec revision the status describes. |
| `observedGeneration` | The object's `metadata.generation` that the status describes. A spec change at the same revision, such as the access mode, bumps the generation, and the status is stale until the agent observes again. |
| `phase` | `progressing`, `ready` or `failed`. |
| `reason` | Why a phase is `failed`: `image_pull`, `crash`, `unhealthy`, `config` or `resource`. |
| `message` | The cluster's detail, in English as Kubernetes says it. |
| `readyReplicas` | Ready replicas. |
| `url` | Where the app answers. |
| `services` | Each service's `name`, `phase`, `reason`, `message` and `readyReplicas`. The app's phase is their aggregate. |

`kubectl get oapp` shows `REVISION`, `OBSERVED`, `PHASE`, `REASON`, `URL` and `AGE`.

### What the agent makes

- A Deployment named `<app>-<service>` for every service.
- A Service for every web service, and an Ingress for every service with a route. The service at `/` uses the app's own name and host.
- Gate objects when `access` is `team` or `organization` and the cluster supports it (`odicea.access`).
- Environment variables, from `env`, the service's `env`, secrets, resources and AI, in that order of increasing priority.
- Secrets for external references. One `ExternalSecret` per store, named `<app>-external-<store>`. Managed values live in a Secret `<app>-secrets`.

Objects the agent creates are owned by the `OdysseyApp`, so deleting it removes them. The environment's namespace and its resources stay when an app is deleted.

## OdysseyResource

One resource of an environment, named after the resource. A resource is either made by Odicea or is a connection to something the company already has, never both.

### Spec

| Field | Meaning |
|---|---|
| `kind` | `postgres`, `redis`, `files` or `object-storage`. |
| `generation` | Grows with every change of the desired state. The agent never lowers it. |
| `storage` | Size such as `5Gi`. It may grow but never shrink: a smaller value is ignored. Set only for a resource Odicea makes. |
| `recipe` | The catalog `recipe@version` that resolved it, such as `postgres-produccion@1`. It is for people reading the cluster. The agent acts on the settings below. |
| `postgres` | `instances` (1 to 5), `major` (16, 17 or 18) and `cpu` and `memory` (each applied as request and limit of every instance). A zero field is the engine's default. The major version is fixed when the database is made. |
| `existing` | The connection of a company resource: a list of `values`, each a written `value` or a `ref` to `{store, key, property}`. |
| `deleted` | Asks for the resource and its data to be removed. |

Rules the CRD enforces:

- A resource cannot have both `postgres` and `existing`.
- A resource Odicea makes has `storage`. One the company already has has none.
- A resource cannot change between made by Odicea and the company's. Delete it and create another.

### Status

| Field | Meaning |
|---|---|
| `observedGeneration` | The `generation` the status describes. |
| `phase` | `provisioning`, `ready`, `failed` or `deleted`. |
| `message` | Why it is not ready, short and in English. |

`kubectl get ores` shows `KIND`, `GENERATION`, `OBSERVED`, `PHASE`, `STORAGE` and `AGE`, and `RECIPE` with `-o wide`.

### What the agent makes

| Kind | What the agent creates |
|---|---|
| `postgres` | A CloudNativePG `Cluster`. Its application secret is `<name>-app`. |
| `redis` | A Redis with a password, one replica, a volume, a Service, and a Secret, all named `<name>-redis`. |
| `files` | A PersistentVolumeClaim named `<name>-files`, which the app mounts. |
| `object-storage` | Only ever the company's own: a connection Secret `<name>-object-storage`. |
| any, with `existing` | Only the connection Secret. The agent never makes, changes or deletes the resource itself. |

For `existing`, written values go into the Secret directly. Any reference to a secret store becomes an `ExternalSecret` that External Secrets reads from a `ClusterSecretStore`, refreshed every hour. Pods read the Secret when they start, so a value rotated in the store reaches an app at its next deployment. A credential is only ever a reference, so it never passes through Odicea.

Deleting a resource (`deleted: true`) removes what the agent made, data included. The agent reports phase `deleted` and then removes the object. A resource of the company is only disconnected, and its data is untouched. If someone deletes an `OdysseyResource` by hand in the cluster, its data is kept until Odicea deletes the resource.

## OdysseyBuild

One build of an app from uploaded source. Its spec never changes after creation (`a build's spec never changes`).

### Spec

| Field | Meaning |
|---|---|
| `organization`, `project`, `environment` | The ids it belongs to. |
| `source` | `{sha256, size}` of the uploaded archive the build fetches through the agent. |
| `imageRepository` | Where the image is pushed, without a tag. The build's id is the tag. |
| `cacheRef` | Registry reference of the project's build cache. |
| `timeoutSeconds` | 60 to 7200. Bounds the build Job. |
| `reuseImage` | An image from an earlier build of the same code under the same builder. If it is still in the registry, the build tags it and builds nothing. |
| `skipScan` | The reused image's scan is recent and the control plane copies it, so a reused image is not scanned again. |

### Status

| Field | Meaning |
|---|---|
| `phase` | `pending`, `running`, `succeeded` or `failed`. |
| `imageDigest` | The pushed image, `sha256:…`. Succeeded only. |
| `reason` | Why it failed: `unsupported` (nothing says how to build or start the app), `source`, `push`, `timeout` or `failed`. |
| `message` | The end of the failing step's output. |
| `finishedAt` | When the agent saw the phase become final. |
| `reused` | The build reused `reuseImage`, so nothing was built. |

Once the phase is final it never changes. A finished build is deleted a while later, three hours by default.

### What the agent makes

A build runs as a Job in the `odyssey-builds` namespace: it fetches and verifies the source, plans the build, builds with BuildKit and pushes the image. If the cluster has `odicea.scan`, a separate Trivy scan Job follows. See [Cluster capabilities](capabilities.md) and [`scan.md`](../scan.md).

## See also

- [Cluster capabilities](capabilities.md)
- [Clusters](../guides/it/clusters.md)
- [Resource catalog](../guides/it/catalog.md)
- [Concepts](../explanation/concepts.md)
- [Security model](../explanation/security-model.md)
- [`resources.md`](../resources.md)
