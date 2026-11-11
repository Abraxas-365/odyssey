# Run Odicea locally

Bring up Odicea on your own machine: the database and identity server in Docker, the API, worker and console on the host, and optionally a local k3d cluster with a real agent. Everything on this page is for development, never for production.

For a production setup see [Install Odicea](install.md). Every Make target is listed in [Make targets](../../reference/make-targets.md); every variable in [Configuration](../../reference/configuration.md).

## Requirements

- Go (the version in `go.mod`), Docker, Node with `npx`, `jq` and `openssl`.
- A checkout of [IAMKit](https://github.com/Abraxas-365/iamkit). `IAMKIT_SRC` in `.env` points at it; the default is `../../Desktop/Proyectos/iam`, so set your own path. Compose builds IAMKit from it.
- For a real cluster, also `k3d`, `kubectl` and `helm` (`helm` installs External Secrets in the cluster).

## First run

```sh
make dev
```

It runs `make setup` (the signing key, the fleet CA in `secrets/fleet/`, `.env` with random secrets, the access signing key), starts Postgres and IAMKit, seeds IAMKit (`make seed`) and starts the stack again. It takes two to four minutes.

The seed creates the demo user `ti@demo.local` with the role **TI**. Its password is `DEMO_PASSWORD` in `.env`. It also writes the `IAMKIT_*` ids into `.env`, below the line `# --- written by scripts/seed-iam.sh ---`.

> [!NOTE]
> Before the first `make seed`, `make up` starts only Postgres and IAMKit and tells you to seed. After it, `make up` starts all five services: `db`, `iamkit`, `api`, `gateway` and `worker`.

### The stack commands

| Command | What it does |
| :--- | :--- |
| `make up` | Starts the compose stack and waits for it to be healthy. |
| `make down` | Stops it. Data is kept. |
| `make reset` | **Destroys local data**: the volumes, `.dev-secrets/`, `.data/` and the seeded ids in `.env`. Run `make dev` again afterwards. |
| `make logs` | Follows the logs of `api`, `worker` and `iamkit`. |
| `make seed` | Provisions the local IAMKit from `iam/permissions.json` and writes the ids to `.env`. |
| `make iam-sync` | The same seed after you edit `iam/permissions.json`. `DRY_RUN=1` shows the changes; `PRUNE=1` also removes permissions that left the manifest. Then get a fresh token: sign in again. |

## Run the processes on your machine

The compose stack already contains an API, a worker and a gateway, on the fake cluster. To work on Go code, or to use a real cluster, GitHub or AI, run them on the host instead. The host processes read `.env` and connect to the compose database and IAMKit.

> [!IMPORTANT]
> The compose `api` publishes `127.0.0.1:8095` and the compose `gateway` publishes `8443`. Stop the containers before you start the host processes, or the ports clash: `docker compose stop api worker gateway`. Keep `db` and `iamkit` running.

Three terminals, or tmux windows:

```sh
make run       # the API on :8095 (restart it after Go changes)
make worker    # outbox, workflows, scheduled tasks; health on :8096
make web       # the console on http://localhost:5178
```

Open `http://localhost:5178` and sign in as `ti@demo.local`. The first `make web` installs the console's packages with npm 11, because npm 10.9 crashes on this tree. A page with the design system, with sample data and no login, is at `http://localhost:5178/design`.

### The fake cluster and the fleet

| | `fake` (default) | `fleet` |
| :--- | :--- | :--- |
| Set with | nothing | `make run CLUSTER=fleet` and `make worker CLUSTER=fleet` |
| Apps | "Run" in memory, no cluster | Deployed to enrolled clusters through their agents |
| Builds | A scripted log and a placeholder image | Real builds in the cluster (`BUILDER` defaults to `fleet`) |
| Needs | Nothing else | The gateway, a k3d cluster and an enrolled agent |

The API and the worker must use the same `CLUSTER` and `BUILDER`. `BUILDER=none` makes every build fail with `NO_BUILDER`.

On the fake cluster, an image whose repository contains `unhealthy` fails its health check and one containing `reject` is refused. Use them to see a failed version and the automatic restore.

### Check it works

```sh
make smoke     # create an app, publish, a failing version, auto-restore, "Volver a esta", audit
make token     # a bearer token for the demo user, for your own curl calls
```

Both need the API and the worker running. `make smoke` exits non-zero on any mismatch.

## A local cluster with a real agent

This gives you real builds and real apps. Use the fleet in the API and the worker, and these steps in order:

```sh
make gateway                   # agents connect here, :8443; the only process with the fleet CA key
make run CLUSTER=fleet         # in its own terminal
make worker CLUSTER=fleet      # in its own terminal
make cluster                   # k3d cluster "odyssey", the CRDs, a registry (zot), CloudNativePG, External Secrets
make agent-enroll NAME=local   # registers the cluster through the API; the token goes into the agent's Secret
make agent-image               # the agent inside the cluster; it can also build
```

`make agent-enroll` needs the API running and signs in as the demo user. Re-running it for the same name issues a new token and the previous certificate stops working.

The cluster's kubeconfig is `.dev-secrets/kubeconfig`; your `~/.kube/config` is never touched. Use it with `KUBECONFIG=.dev-secrets/kubeconfig kubectl …`. Apps answer at `http://<app>.apps.odyssey.localhost:8088` (`APPS_PORT`).

The console shows the same cluster under **Administración → Clústeres**. See [Clusters](../it/clusters.md).

### `make agent` or `make agent-image`

Pick one. Never both at once: only a single agent may run for the cluster.

| | `make agent` | `make agent-image` |
| :--- | :--- | :--- |
| Where the agent runs | On your machine, with your credentials | In the cluster, as in a customer's |
| Builds | **No**: build pods cannot reach it | Yes, with the local registry |
| Restart | After Go changes | Rebuilds the image (2 to 5 minutes) |

`make agent-image` builds `docker build --target agent -t odyssey-agent:dev .`, imports it into k3d, applies `deploy/k3d/agent` and restarts the agent. To switch to `make agent`, scale the in-cluster agent to zero first: `KUBECONFIG=.dev-secrets/kubeconfig kubectl -n odyssey-system scale deployment/odyssey-agent --replicas=0`.

> [!WARNING]
> On the local cluster, never apply `agent/install` directly. Apply `deploy/k3d/agent` (`make agent-image` does). The base manifests lack the overlay's registry rules and sign-in gate settings, so the agent stops reporting `odicea.access` and every deploy fails with "Este clúster no puede proteger apps con inicio de sesión". If it happens: `KUBECONFIG=.dev-secrets/kubeconfig kubectl apply -k deploy/k3d/agent`, then restart the agent's deployment.

To delete the cluster: `make cluster-down`.

The same smoke test can run on real images and real builds: `SMOKE_CLUSTER=fleet make smoke`, with everything above up. The build part takes minutes.

## Ports

| Port | What |
| :--- | :--- |
| 8095 | API |
| 8096 | Worker health |
| 8097 | Agent health |
| 8443 | Gateway (agents' mutual-TLS tunnel); health on 8098 |
| 8085 | IAMKit (`IAMKIT_PORT`) |
| 5438 | Postgres (`DB_PORT`) |
| 5178 | Console (`make web`) |
| 8088 | Apps, through the cluster's ingress: `http://<app>.apps.odyssey.localhost:8088` (`APPS_PORT`) |

Compose binds Postgres, IAMKit and the API to `127.0.0.1`.

## Real GitHub through a tunnel

GitHub must reach your API to send webhooks, and a laptop has no public address. Use a tunnel (cloudflared, ngrok, smee) that forwards a public HTTPS URL to `http://localhost:8095`. Its address is `<tunnel-url>` below. [GitHub App](../it/github-app.md) explains what the App is and what each setting means.

1. Start the tunnel and note its URL. A quick tunnel changes URL every time it restarts.
2. Register the App against that URL:

   ```sh
   make github-app ARGS="--name 'Odicea Dev' --public-url <tunnel-url>"
   ```

   A browser opens on GitHub; confirm with **Create**. The secrets land in `.dev-secrets/github/`, with a `github.env` that points at them.
3. Load them and restart the API and the worker. The Makefile does not read `github.env` by itself, so source it in the same shell, or add its lines to `.env`:

   ```sh
   set -a; source .dev-secrets/github/github.env; set +a
   make run      # and, in the other terminal, make worker
   ```

   The compose `api` and `worker` do not receive the `GITHUB_*` variables, so use the host processes.
4. The demo user needs `odyssey:integrations:manage`. TI has it. If the permission is newer than your IAMKit seed, run `make iam-sync` and sign in again.
5. Install the App from the console, under **Administración → GitHub**.

When the tunnel URL changes, edit the webhook, setup and callback URLs in the App's settings on GitHub. To see why a push did not publish, open the App's Advanced tab on GitHub and read its recent deliveries.

## Real freerouter (AI for apps)

AI for apps needs the organization's AI gateway, freerouter, which is a separate project. Without it the console says the AI is unavailable. To try the real thing, run freerouter yourself, make a service key for the platform there, and add these to `.env`:

```sh
ODYSSEY_FREEROUTER_URL=http://localhost:<freerouter-port>
ODYSSEY_FREEROUTER_KEY=ik_svc_…
# ODYSSEY_FREEROUTER_APP_URL=   # how apps reach it; defaults to the URL
```

URL and key go together or the process refuses to start. Set them for both the API and the worker, so run those on the host (the compose services do not get these variables), and restart them. The app's cluster must also report the `odicea.ai` capability, or the console keeps saying the AI is unavailable. Apps run inside the cluster, where `localhost` is not your machine, so give `ODYSSEY_FREEROUTER_APP_URL` an address that apps can reach. See [AI for apps](../ai-for-apps.md).

## Run the tests

| Command | What it runs | Needs |
| :--- | :--- | :--- |
| `make test` | `go test ./...`: unit and architecture tests. Tests that need Postgres skip, or start a container per package. | Go |
| `make test-db` | All tests against the compose Postgres. `PKG=./internal/project/...` limits it. | `make up` |
| `make check` | The CI gate: `gofmt`, `go vet`, tests. | Go |
| `make web-check` | The console: types, lint, tests, build. | Node |
| `make test-kube` | Fleet end to end on a throwaway k3s in Docker. | Docker |
| `make test-kube-k3d` | The same on the local k3d cluster, then a multi-part app. Stop other agents first. | `make cluster` |
| `make test-build` | Real builds on k3d. Pauses the in-cluster agent, uses up to 4 GiB per build: run it alone. | `make cluster` |
| `make test-cnpg`, `make test-eso` | Databases on CloudNativePG, and existing connections through External Secrets, each in a throwaway k3d cluster. | Docker, `k3d` (`helm` for `test-eso`) |

The full list, including `gen` and `api-types` for regenerating code, is in [Make targets](../../reference/make-targets.md#testing).

## Common problems

| Symptom | What to do |
| :--- | :--- |
| `address already in use` on 8095 or 8443 | The compose `api` or `gateway` is running. `docker compose stop api worker gateway`. |
| Pods in k3d stay `Pending`, including the agent | The Docker disk is almost full and the node has the taint `node.kubernetes.io/disk-pressure`. Run `docker builder prune -af` (build cache is safe to delete), delete failed pods with `KUBECONFIG=.dev-secrets/kubeconfig kubectl delete pods -A --field-selector=status.phase=Failed`, and wait a few minutes for Kubernetes to lift the taint. |
| Every deploy fails with "Este clúster no puede proteger apps con inicio de sesión" | `agent/install` was applied over the overlay. Apply `deploy/k3d/agent` (see the warning above). |
| The agent cannot reach the gateway after a Docker restart | Restart the cluster with `k3d cluster stop odyssey` and `k3d cluster start odyssey`, never a bare `docker restart` of the node: it loses `host.k3d.internal`. |
| `make iam-sync` says "Management key rejected" (401) | IAMKit's local management key in `.dev-secrets/owner.json` expires 24 hours after bootstrap. Recover it, below. |
| A new permission is in the manifest but the console hides the action | Run `make iam-sync`, then sign in again. |
| The console says sign-in is not configured | `make seed` has not run, or the API started before it. Run `make seed`, then restart the API. |

### Recover the IAMKit management key

IAMKit's `recover-owner` issues a new management key. It takes the workspace's UUID, not its name.

```sh
docker compose exec -T db psql -U postgres -d iamkit -Atc "select id from workspaces"
docker compose exec -T iamkit sh -c 'rm -f /tmp/owner.json && iamkit recover-owner --email "$0" --workspace "$1" --output /tmp/owner.json >/dev/null && cat /tmp/owner.json && rm -f /tmp/owner.json' admin@odyssey.local <workspace-uuid> > .dev-secrets/owner.json
chmod 600 .dev-secrets/owner.json
make iam-sync DRY_RUN=1
make iam-sync
```

The email `admin@odyssey.local` is the seed's default operator (`IAMKIT_OPERATOR_EMAIL` changes it). Your `.env` stays the same.

## See also

- [Install Odicea](install.md)
- [Make targets](../../reference/make-targets.md)
- [Configuration](../../reference/configuration.md#local-stack-env)
- [Clusters](../it/clusters.md)
- [GitHub App](../it/github-app.md)
- [Contributing](../../contributing/README.md)
