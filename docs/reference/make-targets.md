# Make targets

Every target in the repository's Makefile: what it does, the variables that change it and what it needs first. Use it as the command reference for local development, tests and IAMKit provisioning.

Run `make <target>` from the repository root. Targets that start a process on the host load `.env` first (`make setup` creates it). For the step-by-step flow see [Local development](../guides/operations/local-development.md); for what the variables mean inside the binaries, see [Configuration](configuration.md).

## Variables

Pass them on the command line: `make run CLUSTER=fleet`.

| Variable | Default | Used by | Meaning |
| :--- | :--- | :--- | :--- |
| `CLUSTER` | `fake` | `run`, `worker` | Value of `ODYSSEY_CLUSTER`: `fake` (in memory) or `fleet` (the enrolled clusters). Use the same value for `run` and `worker`. |
| `BUILDER` | empty (the binaries default it from `CLUSTER`) | `run`, `worker` | Value of `ODYSSEY_BUILDER`: `fake`, `fleet` or `none`. Use the same value for `run` and `worker`. |
| `BLOB_URL` | `file://<repo>/.data/blobs?create_dir=1&no_tmp_dir=1` | `run`, `worker`, `gateway` | `ODYSSEY_BLOB_URL` for the host processes; they share one directory. |
| `NAME` | `local` | `agent-enroll` | Name of the cluster to register. |
| `ARGS` | empty | `github-app` | Extra flags for `odyssey-api github-app create`, such as `ARGS="--org acme --public-url https://odyssey.example.com"`. |
| `PKG` | `./...` | `test-db` | Go packages to test, such as `PKG=./internal/project/...`. |
| `DRY_RUN` | unset | `iam-sync`, `iam-provision` | Any value adds `--dry-run`: print the changes, write nothing. |
| `PRUNE` | unset | `iam-sync`, `iam-provision` | Any value adds `--prune`: also remove permissions that left the manifest. |
| `K3D_CLUSTER` | `odyssey` | `cluster`, `cluster-down`, `agent-image`, `test-build` | Name of the local k3d cluster. |
| `K3D_NODE` | `k3d-$(K3D_CLUSTER)-server-0` | `cluster` | Docker name of the cluster's server node. |
| `KUBECONFIG_LOCAL` | `<repo>/.dev-secrets/kubeconfig` | cluster and test targets | Where the cluster's kubeconfig is written. Your `~/.kube/config` is never touched. |
| `APPS_PORT` | `8088` | `cluster`, `agent`, `test-kube-k3d`, `test-build` | Host port mapped to the cluster's load balancer. Apps answer at `http://<app>.apps.odyssey.localhost:<APPS_PORT>`. |
| `GATEWAY_HOST` | `host.k3d.internal` | `run`, `gateway` | The gateway's name as agents in k3d reach it (goes into enrolment tokens and the gateway certificate). |
| `GATEWAY_PORT` | `8443` | `run`, `gateway`, `agent` | Port of the gateway's agent tunnel. |
| `CNPG_VERSION` | `1.27.4` | `cluster-addons`, `test-cnpg` | CloudNativePG release installed in the cluster. |
| `CNPG_URL` | the release manifest of `CNPG_VERSION` | `cluster-addons`, `test-cnpg` | Where that manifest is downloaded from. |
| `ESO_VERSION` | `2.14.0` | `cluster-addons`, `test-eso` | External Secrets Operator chart version (development only). |
| `ESO_CHART` | `oci://ghcr.io/external-secrets/charts/external-secrets` | `cluster-addons`, `test-eso` | The chart installed with Helm. |
| `SMOKE_CLUSTER` | `fake` | `smoke` (read by `scripts/smoke.sh`) | `fleet` runs the smoke test against the local k3d cluster with real images and real builds. |
| `KUBECONFIG` | `.dev-secrets/kubeconfig` | `smoke` (fleet mode) | Kubeconfig the smoke test uses to read the app from inside the cluster. |

## Build and run

| Target | What it does | Needs |
| :--- | :--- | :--- |
| `build` | Builds `bin/odyssey-api`, `bin/odyssey-worker`, `bin/odyssey-gateway` and `bin/odyssey-agent` (the agent with the Railpack version set at link time). | Go. |
| `run` | Runs the API on the host (`PORT` from `ODYSSEY_PORT`, default 8095) against the compose database and IAMKit. It depends on `access-key`, sets the fleet CA certificate and gateway address, `CLUSTER`, `BUILDER`, `BLOB_URL`, and the access signing key. | `make up` and `make seed` done; `.env`. |
| `worker` | Runs the worker on the host (health on 8096). `CLUSTER=fleet` deploys to the enrolled clusters. | The same. |
| `gateway` | Runs the gateway on the host: agents on `GATEWAY_PORT` (8443), health on 8098, fleet CA from `secrets/fleet/`. | `make setup`; `.env`. |
| `web-install` | Installs the console's npm packages (pins npm 11, because npm 10.9 crashes on this tree). | Node. |
| `web` | Runs the console's dev server (installs packages first if missing). | Node; the API running. |
| `github-app` | Registers the GitHub App through GitHub's manifest flow: opens the browser and writes the secrets to `.dev-secrets/github/` (`github.env` has the variables to load). Pass flags with `ARGS`. | A browser; see [GitHub for IT](../guides/it/github-app.md). |

## Local stack

| Target | What it does | Needs |
| :--- | :--- | :--- |
| `setup` | One time: creates `secrets/jwt.pem`, the fleet CA in `secrets/fleet/` (`odyssey-gateway ca init`), `.env` from `.env.example` with random passwords, `ODYSSEY_UID`/`ODYSSEY_GID`, and the access signing key. Existing files are kept. | Go, `openssl`. |
| `access-key` | Creates `.dev-secrets/access-signing.key` (an Ed25519 seed, base64) if missing. `run`, `up` and `setup` call it. | `openssl`. |
| `up` | Starts the compose stack (database, IAMKit, API, worker, gateway) and waits for health. Before the first `make seed` it starts only the database and IAMKit and tells you to seed. | Docker; `make setup`. |
| `seed` | Provisions the local IAMKit from `iam/permissions.json` and writes the `IAMKIT_*` values into `.env` (below the marker `written by scripts/seed-iam.sh`). Creates the demo user (`DEMO_EMAIL`) with the first role, TI. | The stack up (`make up`); `curl`, `jq`. |
| `dev` | First run: `setup`, `up`, `seed` if needed, `up` again. | Docker. |
| `down` | Stops the compose stack (data kept). | Docker. |
| `reset` | **Destroys local data**: removes the volumes, `.dev-secrets/` and `.data/`, and the seeded IAM ids from `.env`. | Docker. |
| `logs` | Follows the logs of `api`, `worker` and `iamkit`. | Docker. |
| `token` | Logs the demo user in through IAMKit and prints an access token for the API. | `make seed`; `curl`, `jq`. |
| `smoke` | End-to-end check against the running stack with a real token: apps, publishing, auto-restore, "Volver a esta", audit, archive, auth and validation errors, and uploading a folder. Exits non-zero on any mismatch. With `SMOKE_CLUSTER=fleet` it runs on the local cluster; that needs `make gateway`, `make cluster`, `make agent-image`, `make worker CLUSTER=fleet` and `make run CLUSTER=fleet` all running, and waits up to 10 minutes per real build. | The stack running; `jq`. |

## IAMKit

| Target | What it does | Needs |
| :--- | :--- | :--- |
| `iam-sync` | Pushes changes in `iam/permissions.json` to the local IAMKit (runs `scripts/seed-iam.sh`). Without `PRUNE=1`, permissions removed from the manifest stay in IAMKit. | The local stack up. |
| `iam-provision` | Provisions or syncs **any** IAMKit (staging, production) from the manifest: project, organization, application, resource, roles, the console's OAuth client, the backend service account, the MCP resource and the access client. Configured through environment variables, such as `IAMKIT_URL`, `IAMKIT_KEY` or `IAMKIT_KEY_FILE`, `APP_URL`, `AUDIENCE`, `OUT`, `MCP_PRESETS`; run `scripts/provision-iam.sh --help` for the full list. | `curl`, `jq`; a management key. |

> [!WARNING]
> `PRUNE=1` removes permissions from the catalog, and IAMKit strips them from every role, grant and service account at once. Prune only after no deployed code checks the permission, and never against a shared IAMKit without asking. See [Permissions](permissions.md#keeping-iamkit-in-sync).

## Local cluster

| Target | What it does | Needs |
| :--- | :--- | :--- |
| `cluster` | Creates (or starts) the k3d cluster `K3D_CLUSTER` with the load balancer on `APPS_PORT`, makes sure the node uses `deploy/k3d/registries.yaml`, writes `KUBECONFIG_LOCAL`, installs the CRDs, the zot registry (`deploy/k3d/registry`) and the add-ons. Safe to run again. | Docker, `k3d`, `kubectl`, `helm`. |
| `cluster-addons` | Installs CloudNativePG and External Secrets Operator (dev only) in the local cluster. | The cluster. |
| `cluster-down` | Deletes the cluster and its kubeconfig. | `k3d`. |
| `agent-enroll` | Registers the cluster named `NAME` through the API (as "Conectar clúster" does in the console) and stores the one-time token in the agent's Secret. Re-running for an existing cluster issues a new token, and the previous certificate stops working. | `make run`; the cluster; `jq`. |
| `agent` | Runs the agent on the host against the cluster, dialling the gateway on `localhost:$GATEWAY_PORT`. It does not build. Pick this or `agent-image`, not both. | `make cluster`, `make agent-enroll`, `make gateway`. |
| `agent-image` | Builds the agent image, imports it into k3d, applies `deploy/k3d/agent` and waits for the rollout. The in-cluster agent can build apps (it uses the local registry) and runs the access gate. | Docker; `make cluster`, `make agent-enroll`. |

## Testing

| Target | What it does | Needs |
| :--- | :--- | :--- |
| `test` | `go test ./...`. Tests that need Postgres skip, or start a container per package. | Go. |
| `test-db` | The same against the compose Postgres: every test creates and drops its own database. `PKG` limits the packages. | `make up`; `.env`. |
| `vet` | `go vet ./...`. | Go. |
| `fmt` | `gofmt -w` on `agent api cmd internal pkg proto`. | Go. |
| `check` | The local CI gate: fails if `gofmt -l` lists files, then `go vet`, then `go test`. CI also runs the tests with `-race` against Postgres. | Go. |
| `web-check` | The console's checks (`npm run check`). Run it after changing `frontend/`. | Node. |
| `test-kube` | Fleet end to end (gateway, an in-process agent and the fleet adapter) on a throwaway k3s in Docker. | Docker. |
| `test-kube-k3d` | The same on the long-lived k3d cluster, then the multi-part app test. Stop other agents first. | `make cluster`. |
| `test-build` | Real builds on k3d: builds the agent image, imports it, uploads folders through the API, builds them with BuildKit pods, pushes to the local registry and deploys. Pauses the in-cluster agent and needs up to 4 GiB per build, so run it alone. | `make cluster`; Docker. |
| `test-cnpg` | A recipe's database settings on a real CloudNativePG, in a throwaway k3d cluster that it creates and deletes. | Docker, `k3d`. |
| `test-eso` | Reading an existing connection through a real External Secrets Operator, in a throwaway k3d cluster that it creates and deletes. | Docker, `k3d`, `helm`. |

The cluster tests are switched on by variables the targets set (`ODYSSEY_TEST_KUBE`, `ODYSSEY_TEST_KUBECONFIG`, `ODYSSEY_TEST_APPS_PORT`, `ODYSSEY_TEST_BUILD`, `ODYSSEY_TEST_CNPG_KUBECONFIG`, `ODYSSEY_TEST_ESO_KUBECONFIG`); without them those tests skip.

## Generated code

| Target | What it does | Needs |
| :--- | :--- | :--- |
| `gen` | Regenerates `internal/permission` from `iam/permissions.json`, the fleet protocol from `proto/fleet/v1/fleet.proto`, the agent's deepcopy code, and the CRD and RBAC manifests under `agent/install/`. Run it after editing the manifest, `proto/`, `agent/api` or a `+kubebuilder` marker. Tests fail while generated files are stale. | `protoc`. |
| `api-types` | Regenerates the console's wire types from `api/openapi.yaml`. `web-check` fails while they are stale. | Node. |
| `tidy` | `go mod tidy`. | Go. |

## See also

- [Local development](../guides/operations/local-development.md)
- [Configuration](configuration.md)
- [Permissions](permissions.md)
- [Contributing](../contributing/README.md)
