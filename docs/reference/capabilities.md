# Cluster capabilities

What a cluster's agent tells Odicea it can do, what each capability unlocks, and what happens when a cluster lacks one.

Each agent reports its capabilities when it connects and again in its periodic report. Odicea never assumes a cluster can do something it did not announce. It refuses, or holds back, any work that needs a missing capability, instead of sending it and hoping. To see what a cluster announced, switch on "Mostrar detalles técnicos" in the avatar menu, then open Administración → Clústeres and expand "Detalles técnicos" on the cluster.

## What an agent reports

| Report field | Meaning | Shown in "Detalles técnicos" as |
|---|---|---|
| `kubernetes_version` | Kubernetes version of the cluster. | Kubernetes |
| `apis` | Platform API groups the agent found and the `odicea.*` capabilities it supports. | APIs |
| `ingress_classes` | Ingress classes in the cluster. | Ingress classes |
| `storage_classes` | Storage classes, the default one marked `(default)`. | Storage classes |
| `builder` | Present when the cluster can build images (registry and platforms). | not listed |
| `secret_key` | Public key a person's secret value is sealed to in the browser. Only this cluster can open it. | not listed |
| `secret_stores` | Names of the `ClusterSecretStore` objects External Secrets can read from. | not listed |
| `resource_kinds` | Resource kinds the cluster can provision: `postgres`, `redis`, `files`. | not listed |
| `resource_capabilities` | What the agent honors when preparing resources, such as `spec/v1` and `existing`. | not listed |

## `odicea.*` capabilities

These strings appear in `apis`. Most are always reported by a current agent. They matter when you run an older agent next to a newer control plane.

| Capability | The agent reports it when | What it unlocks |
|---|---|---|
| `odicea.services` | Always. | Apps made of several parts (web and worker services). |
| `odicea.service-settings` | Always. | A secret, resource, AI setting or variable given to some parts of an app only, and rollouts that restart only the parts whose pods change. |
| `odicea.ai` | Always. | Turning on the AI for an app. The agent wires the gateway address and key into the app. |
| `odicea.retire` | Always. | Removing an app from the cluster when it is deleted. |
| `odicea.access` | The sign-in gate is installed and configured, and the cluster has the `traefik.io` API. | Apps whose access is "Solo tu equipo" or "Toda la empresa". |
| `odicea.scan` | The agent is set up to scan the images it builds. | Vulnerability scans after each build, feeding the `scan.*` inputs of [rules](rule-input.md). |
| `odicea.egress` | The agent enforces the cluster's egress policy. | The "Salida a internet" settings. |

The agent also lists the platform API groups it recognizes: `postgresql.cnpg.io` (CloudNativePG), `external-secrets.io` (External Secrets Operator), `traefik.io`, `kyverno.io`, `keda.sh`, `gateway.networking.k8s.io` and `cert-manager.io`. Three of them change what Odicea offers: `postgresql.cnpg.io` (managed databases), `external-secrets.io` (external secrets and company resources that read from a store) and `traefik.io` (needed for `odicea.access`). No check described on this page depends on the others.

## What each capability does, and what happens without it

| Capability | What a builder or IT sees without it |
|---|---|
| `odicea.services` | Saving or publishing an app of several parts is refused with `SERVICES_UNAVAILABLE`: "El clúster de esta app todavía no puede correr varios servicios. Pide a TI que actualice su agente." |
| `odicea.service-settings` | Giving a setting to only some parts is refused with `SERVICE_SETTINGS_UNAVAILABLE`. The AI panel reports `scoped_settings: false` and offers the AI for the whole app only. |
| `odicea.ai` | Turning on the AI is refused with `AI_UNAVAILABLE`: "Este clúster todavía no puede conectar apps con la IA. Pide a TI que actualice su agente." |
| `odicea.retire` | Deleting an app is kept pending. Odicea does not send the deletion to an agent that would run the app again. It waits for the agent's update. |
| `odicea.access` | Changing an app to "Solo tu equipo" or "Toda la empresa" is refused with `ACCESS_UNAVAILABLE`: "Este clúster no puede proteger apps con inicio de sesión. Pide a TI que lo habilite." Only public access can be applied there. |
| `odicea.scan` | Builds on that cluster have no scan: the build records "Este clúster no revisa vulnerabilidades." The rules then see `scan.known == false`. See below. |
| `odicea.egress` | The egress card shows "Este clúster no puede aplicarla: actualiza su agente". Your choice is stored and applies once the agent is updated. |

### Scans and rules

A cluster without `odicea.scan` produces `scan.known == false` and `scan.checked == true` after each build. The built-in rule "Producción no publica con vulnerabilidades críticas" does not match it, so an unscanned image is allowed. If you want production to require a scan, switch on "Producción exige revisión de vulnerabilidades". See [Rule input](rule-input.md#built-in-rules) and [Write a rule](../guides/it/rules.md).

## Capabilities for building and secrets

These are not `odicea.*` strings, but they gate work in the same way.

| Report field | Needed for | Without it |
|---|---|---|
| `builder` | Publishing from source (a folder, a repository). | The build is refused with `NO_BUILDER`: "El clúster … todavía no puede construir apps. Pide a TI que instale el constructor del agente." |
| `secret_key` | Secrets whose value a person types in the console. | `SECRETS_UNAVAILABLE`: "Este clúster todavía no puede guardar secretos. Pide a TI que actualice su agente." |
| `external-secrets.io` in `apis` | Secrets read from the company's secret store. | `EXTERNAL_SECRETS_UNAVAILABLE`: "Este clúster no tiene un gestor de secretos externos (External Secrets Operator). Pide a TI que lo instale." |
| `secret_stores` containing the store name | A secret or resource that names that store. | `SECRET_STORE_UNKNOWN`, naming the store. |

## Capabilities for resources

A resource is a database, cache, files space or object storage ([Catalog](../guides/it/catalog.md)). The cluster must have what the resource's recipe requires, or the app is refused with `RESOURCES_UNAVAILABLE` or `RESOURCES_UNSUPPORTED`.

| Kind | Provisioned by Odicea when | A company resource needs |
|---|---|---|
| `postgres` | The cluster has a default storage class and `postgresql.cnpg.io`. | `existing` |
| `redis` | The cluster has a default storage class. | `existing` |
| `files` | The cluster has a default storage class. | not possible: files are always a volume of the cluster |
| `object-storage` | Never. Odicea does not create object storage. | `existing` |

`resource_capabilities` reports what the agent honors:

| Value | Meaning |
|---|---|
| `existing` | Always reported. The agent writes the connection of a resource the company already has. |
| `spec/v1` | Reported when the cluster provisions at least one kind. The agent reads a recipe's settings. |
| `builtin:postgres:instances` | Reported with `postgres`. Databases with replicas. |
| `builtin:postgres:version` | Reported with `postgres`. Pins the PostgreSQL major version. |
| `builtin:postgres:resources` | Reported with `postgres`. Sets CPU and memory. |

Odicea also derives from `resource_kinds` a `builtin:<kind>` entry per provisioned kind, and from `secret_stores` a `secretstore:<name>` entry per store. A recipe goes only to a cluster that announces everything it requires.

| Situation | Error |
|---|---|
| A built-in resource of a kind the cluster does not list | `RESOURCES_UNAVAILABLE`, with the reason for the kind, such as "Este clúster no tiene bases de datos administradas. Pide a TI que instale CloudNativePG." |
| A recipe needs a capability the cluster lacks | `RESOURCES_UNSUPPORTED`: "El clúster de este entorno no puede preparar … como lo pide su receta: su agente necesita una actualización (…)." |
| A company resource on a cluster without `existing` or External Secrets | `RESOURCES_UNSUPPORTED`: "El clúster de este entorno no puede conectar … de la empresa: necesita External Secrets y un agente actualizado. Pide a TI que lo revise." |
| A company resource whose secret store the cluster does not have | `RESOURCES_UNSUPPORTED`: "El clúster de este entorno no tiene el almacén de secretos … Pide a TI que lo revise." |

In the catalog, "Registrar un recurso que ya existe" shows which clusters can use a company resource: those that read from all the stores it names.

## Where it shows

- **Console:** Administración → Clústeres, "Detalles técnicos" (with technical details on), and the egress card state.
- **Placement:** an app is only applied to a cluster whose agent can honor it. A revoked cluster gets no new versions.
- **Errors:** every refusal above carries the code in the left column of the tables and a plain-language message for the builder.

## See also

- [Clusters](../guides/it/clusters.md)
- [Resource catalog](../guides/it/catalog.md)
- [Internet access from apps](../guides/it/egress.md)
- [Rule input](rule-input.md)
- [Errors](errors.md)
- [`scan.md`](../scan.md), [`egress.md`](../egress.md), [`resources.md`](../resources.md)
