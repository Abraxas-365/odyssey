# Your first real cluster

In this tutorial you replace the simulated cluster with a real one. You create a local Kubernetes cluster with k3d, start the gateway that agents connect to, enrol the cluster and install Odicea's agent inside it, then publish a real folder: Odicea builds it with Railpack, scans the image, runs it, and serves it at a real address behind the company sign-in. Last, you add a Postgres database. It takes about forty minutes the first time, most of it downloads and the first scan, and it is the most technical of the three tutorials.

You will end with the app Inventario de bodega running in your k3d cluster at `http://inventario-de-bodega.apps.odyssey.localhost:8088`, with a build log, a security scan and a database connected to it.

## Before you start

- Everything from [Your first app](first-app.md): the stack from `make dev`, and the three terminals for the API, the worker and the console.
- `k3d`, `kubectl` and `helm` installed, and Docker running with enough free disk and memory. The cluster pulls the images of CloudNativePG and External Secrets, builds your app, and keeps a vulnerability database of about 2 GB for the scans, so give Docker plenty of free disk and memory. The repository does not state a minimum; if `make cluster` or the first scan fails half way, free space and try again.
- Nothing else using ports `8088` (where the cluster serves your apps) and `8443` (the gateway).
- Network access: the cluster downloads operators, base images and the scanner's database.

> [!WARNING]
> Never run `kubectl apply -k agent/install` (or `-f agent/install`) against this local cluster. That is the stock install a customer's cluster gets. It has no registry, so it would turn your cluster back into one that cannot build. For this cluster always use the Makefile targets below, which apply `deploy/k3d/agent`: the same install plus the in-cluster registry that builds push to and the sign-in gate settings. The contributors' guide, [AGENTS.md](../../AGENTS.md), says the same.

## 1. Create the cluster

```sh
make cluster
```

This creates a k3d cluster called `odyssey` (no extra agent nodes, port `8088` of your computer mapped to the cluster's ingress), writes its kubeconfig to `.dev-secrets/kubeconfig` without touching your `~/.kube/config`, applies Odicea's custom resources, installs a small registry called zot where builds push their images, and installs CloudNativePG (it makes the databases) and External Secrets. It takes a few minutes the first time. It is safe to run again: it reuses an existing cluster.

The last line says the cluster is ready and what to do next. To look at it yourself, point `kubectl` at that kubeconfig:

```sh
export KUBECONFIG=$PWD/.dev-secrets/kubeconfig
kubectl get nodes
```

```text
NAME                    STATUS   ROLES                  AGE   VERSION
k3d-odyssey-server-0    Ready    control-plane,master   2m    v1.…
```

## 2. Start the gateway

Agents never receive connections. They dial out to a gateway, and the control plane talks to them through it. In a fourth terminal:

```sh
make gateway
```

It listens for agents on `8443` (its health is on `8098`) and answers to the name `host.k3d.internal`, which is how a pod inside k3d reaches your computer. It signs each agent's certificate with the fleet CA that `make dev` created in `secrets/fleet/`. Leave it running.

## 3. Enrol the cluster

With the API from the first tutorial still running:

```sh
make agent-enroll NAME=local
```

This does what the console's **Conectar un clúster** does: it registers a cluster named `local` through the API as the demo user, and stores the one-time enrolment token, the gateway address and the CA fingerprint in a Secret called `odyssey-agent` in the cluster. The token is valid for a limited time, and the line it prints says until when. Run the command again to get a new token for the same cluster; the agent then enrols again and its old certificate stops working.

Open the console, the tab **Administración** in the top bar, then **Clústeres**. The cluster `local` is there with the state **Esperando al agente**: nobody has answered with the token yet.

## 4. Install the agent in the cluster

```sh
make agent-image
```

It builds the agent's image, imports it into k3d, applies `deploy/k3d/agent` and waits for the rollout. It takes two to five minutes the first time. The agent runs inside the cluster, so that it can run build pods; an agent on your own computer (`make agent`) cannot build, because build pods could not reach it. Pick one of the two, and do not run both: if you ever started `make agent`, stop it first.

When the agent starts, it enrols with the token. Reload **Clústeres**: the state of `local` becomes **Conectado**. The agent also starts warming up the scanner's database, which takes minutes. Your first scan waits behind that download.

## 5. Restart the API and the worker on the fleet

So far both processes use the fake cluster. Stop `make run` and `make worker`, and start them again, both with the same setting:

```sh
make run CLUSTER=fleet
make worker CLUSTER=fleet
```

They must agree. With `fleet`, apps go to the enrolled clusters, and the builder is the cluster's own: the fake builder is refused on purpose, because its images exist nowhere. Create new apps from here on: the apps you made on the fake cluster were never on a real one.

> [!NOTE]
> The first cluster that connects receives every new app (the console says: "El primero que se conecta recibe las apps nuevas."). That is `local`.

## 6. Publish a real folder

You need a real app. Make a folder with a web server that has no dependencies:

```sh
mkdir inventario-de-bodega && cd inventario-de-bodega

cat > package.json <<'EOF'
{
  "name": "inventario-de-bodega",
  "version": "1.0.0",
  "private": true,
  "scripts": { "start": "node index.js" }
}
EOF

cat > index.js <<'EOF'
const http = require('node:http')
const port = process.env.PORT || 8080
http.createServer((req, res) => {
  res.setHeader('content-type', 'text/plain; charset=utf-8')
  const db = process.env.DATABASE_URL ? 'con base de datos' : 'sin base de datos'
  res.end(`Inventario de bodega de Andes Logística (${db})\n`)
}).listen(port, '0.0.0.0')
EOF
```

It listens on `$PORT` and on all addresses, which is what a published app must do. In the console, open **Inicio**, press **Crear app** (or the tile **Crear una app**), name it `Inventario de bodega`, choose **Subir una carpeta** and press **Crear y subir**. In the upload dialog pick the folder and press **Publicar**, exactly as in the first tutorial.

## 7. Watch the build and the scan

Turn on **Mostrar detalles técnicos** in the avatar menu if you have not: it opens the build log by default. This time the log is the real one, not a script. It begins like this and then shows Railpack's and BuildKit's own output:

```text
Fetching the source (… MiB)
Source verified: 2 files, … unpacked
No Dockerfile: detecting how to build the app (Railpack)
…
Pushed zot.odyssey-registry.svc.cluster.local:5000/odyssey/…@sha256:…
```

If your folder had a `Dockerfile`, the log would say "Found a Dockerfile: building with it" instead. The build runs in a pod inside your cluster and pushes the image to the zot registry; nothing leaves your computer except the downloads of base images.

The steps then go on as before: **Preparando tu app**, **Revisando que funcione**, **Publicando**. When the header says **En línea**, the app is running in the cluster. The header also says "Corre en local".

The scan starts as soon as the build ends. On the panel **En línea ahora**, the footer says **Seguridad:** and then one of:

- **Escaneando…**, while the scanner works. The first scan is slow, because it needs the vulnerability database (about 2 GB, stored once in the cluster and reused). Later scans take about a minute.
- **Sin vulnerabilidades graves**, when nothing critical or high was found.
- the counts of critical and high findings, such as "2 altas".
- **No se pudo escanear**, with the reason, if the scanner could not run. An unknown result is never shown as clean.

The scan covers operating system packages and the dependencies of the app's language. The **Inventario** tab in the top bar lists every app with its scan in the column **Escaneo**.

## 8. Open it

Open <http://inventario-de-bodega.apps.odyssey.localhost:8088> in the browser. Names ending in `.localhost` resolve to your own computer, and `8088` is the port `make cluster` mapped to the cluster.

You do not see the app yet. A new app can be opened only by your team, so the cluster sends you to Odicea to sign in first: the browser goes to `http://localhost:8095/access/authorize…`, you sign in as `ti@demo.local` if you were not already, and you come back to the app, which answers:

```text
Inventario de bodega de Andes Logística (sin base de datos)
```

A person who is not in your organization would be stopped at that step. The decision of who can open an app is on its **Compartir** tab, panel **Quién puede abrirla**. The sign-in is applied in front of your app by the cluster, so the app itself needs no login code.

If `curl` cannot resolve the name, give it the address yourself and look at the redirect:

```sh
curl -si --resolve inventario-de-bodega.apps.odyssey.localhost:8088:127.0.0.1 \
  http://inventario-de-bodega.apps.odyssey.localhost:8088 | head -n 5
```

```text
HTTP/1.1 302 Found
Location: http://localhost:8095/access/authorize?env=…&mode=team&…
```

## 9. Add a database

Open the app, then **Configuración**, and the panel **Datos e IA**. Press **Agregar**. In the dialog **Agregar a la app**, the **Tipo** is **Base de datos** (the options are the kinds the cluster can create; this one can, because CloudNativePG is installed). Leave the **Nombre** `db`, which cannot be changed later, and the default size, then press **Agregar**.

The database is prepared by the operator in the cluster. Its badge reads **Preparando…** and then **Lista**. Odicea connects it to the app and publishes a new version of the app that receives the connection as environment variables: `DATABASE_URL`, `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER` and `PGPASSWORD`. Passwords are kept in the cluster and are never shown in the console.

When that version is online, reload the app's address. The answer changes:

```text
Inventario de bodega de Andes Logística (con base de datos)
```

If you ask for it from your assistant (see [Publish with your assistant](first-app-with-an-assistant.md)), the result is the same. And if a cluster has no CloudNativePG, the panel says "El clúster de esta app todavía no puede crear bases de datos, cachés ni archivos. Pídele a TI que lo habilite."

## 10. Stopping and cleaning up

Stop the gateway, the API, the worker and the console with Ctrl-C. The cluster and the agent keep existing in Docker. To remove the cluster and its kubeconfig:

```sh
make cluster-down
```

## If something went wrong

| Symptom | Cause |
| :--- | :--- |
| `make cluster` fails to pull an image, or the operators never become ready | Docker has no network, or it is out of disk or memory. Free some space and run `make cluster` again; it is safe to repeat. |
| `make agent-enroll` fails with a connection error | The API is not running. Start `make run` first. |
| The cluster stays **Esperando al agente** | The agent has not enrolled. Check the gateway is running, then `kubectl -n odyssey-system logs deployment/odyssey-agent`. If the token expired, run `make agent-enroll NAME=local` again. |
| The cluster is **Sin conexión** | The gateway stopped, or the agent lost it. Start `make gateway` again and check the state of the cluster again. After restarting k3d with a bare `docker restart` it cannot reach the gateway: use `k3d cluster stop` and `start`, or run `make cluster`. |
| The upload fails with "No hay un clúster donde construir esta app. Pide a TI que registre uno." | The cluster is not enrolled or connected yet. Finish steps 3 and 4 and check **Clústeres**. |
| The API or the worker refuses to start with `ODYSSEY_BUILDER=fake cannot build for ODYSSEY_CLUSTER=fleet` | You set the fake builder with the fleet cluster. Unset `BUILDER`, or use `fleet`. |
| The version fails at **Construyendo tu app** | Read the build log. A folder with no start command is refused as an app Odicea cannot run; check `package.json` has a `start` script. |
| **Seguridad** stays on **Escaneando…** a long time | The first scan waits for the database download. Later ones are quick. A scan has a time limit of fifteen minutes once it starts. |
| The address never opens or says "site can't be reached" | The cluster is not on port `8088` (something else holds it), or the version is not **En línea** yet. |
| The address opens a Odicea sign-in that fails | The API must be running on `localhost:8095`, because the cluster's sign-in gate fetches its keys from it. |
| **Datos e IA** says the cluster cannot create databases | CloudNativePG is not installed. Run `make cluster` again, which installs it. |
| Odicea cannot build after you applied `agent/install` | You applied the stock install over the local one. Run `make agent-image` to apply `deploy/k3d/agent` again. |

## What you learned

- A real cluster needs four things from your computer: the cluster itself (`make cluster`), the gateway (`make gateway`), a registration (`make agent-enroll NAME=local`) and the agent (`make agent-image`); and the API and the worker must both be started with `CLUSTER=fleet`.
- Builds run in the cluster with Railpack (or your `Dockerfile`), the image goes to a registry in the cluster, and every build is scanned.
- The cluster itself puts the company sign-in in front of an app, according to **Quién puede abrirla**.
- The kinds of resources you can add depend on the operators the cluster has; CloudNativePG gives you databases.

## Next steps

- [Connect a cluster](../guides/it/clusters.md) for a real company's cluster, not a local one.
- [Databases and storage](../guides/storage.md) and [Variables and secrets](../guides/variables-and-secrets.md).
- [Make targets](../reference/make-targets.md) and [Local development](../guides/operations/local-development.md).
- [Who can open an app](../guides/who-can-open-it.md) and the [security model](../explanation/security-model.md).
