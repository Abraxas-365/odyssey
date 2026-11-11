# Life of a version

What happens between pressing "Publicar" and seeing "En línea": the four ways code arrives, the steps every version goes through, the states it can be in and what each failure leaves behind.

A version is one attempt to put a release online. The same steps run whichever way you publish, and IT's rules are asked before anything is built.

## Four ways in

| Way in | Who starts it | What arrives |
| :--- | :--- | :--- |
| Console upload ("Subir una carpeta") | A person in the browser | The console packs the folder in the browser (leaving out `node_modules`, `.git` and similar) and sends it with the publish request. |
| Assistant, `deploy_folder` | A person's assistant over MCP | The tool answers with a single-use upload link (valid 10 minutes) and a shell command. The assistant runs the command in the app's folder, which tars and uploads it. The code never passes through the tool. |
| GitHub push | A push to the connected branch, or "Publicar ahora" | The control plane downloads the commit with the GitHub App, repacks it as a source and publishes it. The actor is the system actor `github:<login>`. |
| Assistant, `deploy_image` | A person's assistant over MCP | An image that is already built, pinned by digest. There is no build and no scan. |

All four end up as a **deployment** of an environment, started by the same service, with the permission to create deployments. See [Publish an app](../guides/publish.md), [Publish from GitHub](../guides/github.md) and [MCP tools](../reference/mcp-tools.md).

A commit is published once per connection, whatever reported it (the webhook, the periodic check that catches a missed webhook, or a person). A newer publication of the same app replaces one that is still waiting.

## The steps

A deployment is a durable workflow that runs in the worker, so a restart of the API or the worker loses nothing. The steps, in order:

```text
authorize        IT's rules judge the action, as the person who asked
approval         carry on, or cancel if IT said no
build            (from a folder or GitHub) build the image, then record the release
authorize_scan   judge the build's vulnerability scan with the same rules
approval_scan    carry on, or cancel
apply            hand the desired app to the cluster, with this deployment's revision
wait_ready       poll the cluster until that revision answers its health check
promote          make the release the one the environment serves
```

Console copy for the build phases is "Construyendo tu app" (build), "Preparando tu app" (apply), "Revisando que funcione" (wait_ready) and "Publicando" (promote).

### Before the build

`authorize` evaluates IT's rules for the action `deploy` (or `rollback`) with the actor and the permissions stored when the deployment started. The answer is one of three:

- **Allow.** The deployment goes on.
- **Deny.** It fails at once with `POLICY_DENIED` and says why. Nothing was built or uploaded for it to run.
- **Approval.** The deployment becomes `waiting_approval` ("Esperando aprobación de TI"), a request appears in **Bandeja**, and the workflow parks. It waits at most eight days; the request itself expires after seven.

Because this comes first, a refused or held publication costs no build. If the rules could not be evaluated, the answer is the strongest effect among the rules that could not be read, so a broken rule never turns into a silent allow. See [Security model](security-model.md) and [Approvals](../guides/it/approvals.md).

### Build

From a folder or GitHub, the control plane asks the cluster's agent for a build and follows it, for at most 25 minutes. In the cluster:

1. A short-lived pod fetches the source from the agent with a one-time token.
2. If the folder has a `Dockerfile`, it is used. Otherwise Railpack detects the language and writes the plan.
3. BuildKit, running rootless, builds the image and pushes it to the registry the cluster was given.
4. The agent reports the image digest, and the control plane records an immutable **release**: the image plus its configuration.

The build log is stored (a runaway log is cut at 50 000 lines) and is what "Detalles técnicos" and `get_build_logs` show. A cluster with no registry cannot build, and the version fails with `NO_BUILDER`.

### Scan

Right after a build the cluster scans the image for known vulnerabilities, when it announces the `odicea.scan` capability. The result feeds the rules and the "Seguridad" panel ("Escaneando…", "Sin vulnerabilidades graves", "Sin escaneo"). The build does not wait for the scan: `authorize_scan` waits for it up to ten minutes. If it did not arrive, failed, or the cluster does not scan, the deployment is judged as *checked but unknown*. By default an unscanned image is allowed; the built-in rule "Producción exige revisión de vulnerabilidades" (off by default) makes IT approve that case. An image published with `deploy_image` is never scanned.

### Apply and wait

`apply` writes the desired state to the cluster inside one transaction that holds the app, so an app being archived at the same moment cannot be re-created afterwards. This is the one place where the app's access mode, variables and secrets reach the cluster. The agent turns it into Kubernetes objects: a namespace, a deployment, a service and a route for each part, plus the storage, secrets and network policies the app needs. If the cluster is unreachable, the step retries with backoff for about four minutes.

`wait_ready` polls the agent. The revision of the deployment grows with every publication, and the agent never goes backwards, so a deployment that a newer one replaced fails as superseded instead of overwriting it. The step ends when the revision is ready, when it fails, or when time runs out: five minutes for an app without storage, and longer for one whose storage must be ready first.

### Promote

`promote` is the only code that changes what an environment serves. Only now is the version "En línea", the previous one is kept in the history as the one to go back to, and, for an app published from GitHub, the commit gets its success status.

## States

| API status | Console | Meaning |
| :--- | :--- | :--- |
| `waiting_approval` | Esperando aprobación de TI | Held by a rule before anything was built. |
| `building` | Publicando… | The image is being built (or scanned). |
| `pending` | Publicando… | Built, waiting to be handed to the cluster. |
| `applying` | Publicando… | Being handed to the cluster. |
| `verifying` | Publicando… | The cluster runs it and the health check is being polled. |
| `ready` | En línea (if current) | Passed its health check and was promoted. |
| `failed` | No se publicó | A step failed; the reason is stored. |
| `cancelled` | — | Replaced by a newer one, refused by IT, or the app was archived. |

`ready`, `failed` and `cancelled` never change again. The app's own status ("En línea", "Publicando…", "Necesita atención", "En pausa", "Esperando aprobación", "Archivada") is derived from the versions.

## Why a version stops

| What happened | Code or reason | What is left |
| :--- | :--- | :--- |
| IT's rules deny it | `POLICY_DENIED` | Nothing built; the current version keeps serving. |
| IT rejects it, it expires or its author cancels it | `cancelled`, reason `approval_rejected`, `approval_expired` or `approval_cancelled` | Same. |
| A newer publication replaced it | `cancelled`, reason `superseded` | The newer one continues. |
| The app was archived meanwhile | `cancelled`, reason `archived` | The app is retired. |
| The build fails, times out or the source is unusable | `BUILD_FAILED`, `BUILD_TIMEOUT`, `SOURCE_INVALID`, `UNSUPPORTED_APP` | The log says why. The cluster was not touched, so nothing is restored. |
| The cluster cannot build or does not exist | `NO_BUILDER`, `NO_CLUSTER` | Ask IT, "Clústeres". |
| The cluster cannot run it | `APPLY_FAILED`, `IMAGE_PULL_FAILED`, `RESOURCE_NOT_READY` | See below. |
| It starts but does not answer, or keeps crashing | `HEALTH_CHECK_FAILED`, `APP_CRASHED`, `NOT_READY` | See below. |

When a version fails **after it reached the cluster** while another version was serving, the platform starts a new deployment of that serving version at once (reason `auto_rollback`). The environment's current version never moved, so the cluster is put back to it, and "Historial" shows the restore as "Restaurada automáticamente". A failed build changes nothing in the cluster. A first version that fails has nothing to restore: the app shows "No se publicó" (it needs attention) and stays offline.

The codes are listed in [Errors](../reference/errors.md).

## Going back

"Volver a esta" on an earlier version opens "¿Volver a esta versión?". It creates a new deployment with reason `rollback` of the *release* you chose. It reuses the built image, so it does not build and does not scan again, but it **is** judged by IT's rules as the action `rollback`, so it can be held for approval too. Only a version that was once online can be restored.

## Changing a setting

Changing the access mode, a variable or a secret on an app that is online publishes the same release again with reason `config`. It goes through the same steps, minus the build. "Historial" lists it as its own entry. See [Variables and secrets](../guides/variables-and-secrets.md).

## See also

- [Versions](../guides/versions.md)
- [Security model](security-model.md)
- [Concepts](concepts.md)
- [Rule input](../reference/rule-input.md)
