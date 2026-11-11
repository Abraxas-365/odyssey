# Install Odicea

What you need to run Odicea for your organization: the processes, the services around them, the settings each one reads and the order to bring them up. Written for the people who operate it.

Odicea is early-stage software that has not run a production pilot yet. This page describes what the repository supports today, and says where you have to build the missing piece yourself.

## What you install

| Piece | What it is | Where it comes from |
| :--- | :--- | :--- |
| Postgres | One database for `odyssey-api`, `odyssey-worker` and `odyssey-gateway`. | You provide it. |
| IAMKit | Identity server: people, sign-in, roles, permissions. It needs its own database and refuses to share a schema with another application. | You provide it ([IAMKit](https://github.com/Abraxas-365/iamkit)). `scripts/provision-iam.sh` configures it for Odicea. |
| `odyssey-api` | The HTTP API, the MCP endpoint (`/mcp`), the sign-in of protected apps and the webhooks. Port 8095. | The repository's `Dockerfile`. |
| `odyssey-worker` | Delivers events, runs workflows and scheduled tasks. Health on 8096. Any number of replicas. | The same image. |
| `odyssey-gateway` | The mutual-TLS gRPC server that cluster agents dial (8443, health on 8098). The only process that holds the fleet CA's private key. | The same image. |
| Console | The single-page web app in `frontend/`. | You build it (see [The console](#the-console)). |
| Blob store | Where uploaded sources and build logs live. | You provide it (`file://` or `s3://`). |
| Fleet CA | The certificate authority that signs every agent's certificate. | You create it once with `odyssey-gateway ca init`. |
| `odyssey-agent` | Runs in each customer cluster. | Built from the same `Dockerfile` (`--target agent`). See [Clusters](../it/clusters.md). |

```text
 console ──► odyssey-api ──┐
 assistant ─► (MCP)        ├──► Postgres ◄── odyssey-worker
                           │        ▲
                           │   odyssey-gateway ◄══ mTLS, dialled out ══ odyssey-agent (in each cluster)
```

> [!IMPORTANT]
> The repository has no Helm chart, Terraform module or Kubernetes manifests for the control plane, and no workflow that publishes images. The only deployment recipe it contains is `docker-compose.yml`, which is for local development ([Run Odicea locally](local-development.md)). To run in production you write your own manifests or compose file around the image, using the settings below.

## 1. Postgres

Create one database for Odicea and give its connection string to the three processes as `DATABASE_URL`. IAMKit uses a different database on the same server or another one.

- Each pooled connection gets `statement_timeout=30s`, `lock_timeout=10s` and `idle_in_transaction_session_timeout=60s`, unless your URL sets them. The pool holds at most 20 connections per process.
- The worker and the workflow engine use `LISTEN`/`NOTIFY` on that URL. Do not put a transaction-mode pooler in front of it.

Details are in [Configuration](../../reference/configuration.md#database).

## 2. IAMKit and its provisioning

Odicea needs an IAMKit environment with its permission catalog, roles, a public client for the console, a service account, and clients for MCP and for protected apps. `scripts/provision-iam.sh` creates all of them by name and only when missing, so you can run it again after the manifest `iam/permissions.json` changes. In the repository: `make iam-provision`.

You need a **management key** for IAMKit. IAMKit's `iamkit bootstrap --email <email> --workspace <name> --output <file>` creates one in a new private file. The key expires after 24 hours, so provision soon after you create it.

Example for Andes Logística (placeholders are yours to replace):

```sh
IAMKIT_URL=https://iam.example.com \
IAMKIT_KEY_FILE=owner.json \
APP_URL=https://odicea.example.com \
AUDIENCE=https://odicea.example.com/mcp \
PROJECT_NAME="Odicea" ORG_NAME="Andes Logística" APP_NAME="Odicea Console" \
OWNER_EMAIL=ana.torres@example.com OWNER_PASSWORD='<12 to 72 characters>' \
MCP_PRESETS=claude_code,codex,cursor,vscode,antigravity \
OUT=odicea-iamkit.env \
make iam-provision DRY_RUN=1
```

`DRY_RUN=1` prints what would change and writes nothing. Run it again without it to apply. The variables that matter:

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `IAMKIT_URL` | `http://localhost:8080` | IAMKit's base URL. |
| `IAMKIT_KEY` or `IAMKIT_KEY_FILE` | none (required) | The management key, or a JSON file with `management_key`. |
| `APP_URL` | `http://localhost:3000` | Where the console is served. The console's sign-in returns to `<APP_URL>/auth/callback`. |
| `AUDIENCE` | `APP_URL` | The MCP endpoint's URL, `https://<api>/mcp`. **It cannot be changed once the IAMKit resource exists.** Odicea's API refuses to start with MCP on unless its path is `/mcp`. |
| `ACCESS_API_URL` | `AUDIENCE` without `/mcp` | The API's public base URL, for the redirect of the sign-in of protected apps (`<ACCESS_API_URL>/access/callback`). |
| `EXTRA_ORIGINS` | none | Extra console origins IAMKit should allow. |
| `PROJECT_NAME`, `ENVIRONMENT_NAME`, `ORG_NAME`, `APP_NAME` | `My App`, `Development`, `Demo Org`, `Web` | Names in IAMKit. Everything is found by name. |
| `OWNER_EMAIL`, `OWNER_PASSWORD`, `OWNER_NAME` | none | An optional first person, added to the organization with the first role of the manifest (**TI**). Without a password the person is skipped: invite them from IAMKit instead. |
| `MCP_PRESETS` | none | Assistants admitted to `/mcp`. Empty means no MCP resource. |
| `OUT` | `.iamkit.env` | File that receives the `IAMKIT_*` settings (mode `0600`). |
| `DRY_RUN`, `PRUNE` | unset | Any value adds `--dry-run` or `--prune`. Pruning removes permissions that left the manifest; do it only after no deployed code checks them. |

The script writes the settings Odicea reads into `OUT`: `IAMKIT_URL`, `IAMKIT_ISSUER`, `IAMKIT_ENVIRONMENT_ID`, `IAMKIT_APPLICATION_ID`, `IAMKIT_RESOURCE_ID`, `IAMKIT_AUDIENCE`, `IAMKIT_WEB_CLIENT_ID`, `IAMKIT_SERVICE_ACCOUNT_SECRET`, `IAMKIT_INTROSPECTION_CLIENT_ID` and `_SECRET`, `IAMKIT_ACCESS_CLIENT_ID` and `_SECRET`. It also writes ids Odicea does not read (`IAMKIT_ORGANIZATION_ID`, `IAMKIT_ROLE_*_ID` and others).

> [!WARNING]
> The service-account secret and the two client secrets exist only when IAMKit creates them. Keep `OUT` somewhere safe and out of git. If you lose a secret, revoke that account or client in IAMKit and run the script again.

The secrets are written as plain values. Every one has a `_FILE` variant in Odicea ([Configuration](../../reference/configuration.md#iamkit-and-sign-in)); put them in your secret store and mount them as files if you prefer.

For who gets which role afterwards, see [People and roles](../it/people-and-roles.md).

## 3. The fleet CA

Create the CA once, on a machine you control:

```sh
odyssey-gateway ca init ca.crt ca.key
```

It writes the key with mode `0600`, is valid for ten years and **refuses to overwrite** existing files, because replacing the CA disconnects every cluster. Back both files up.

| File | Goes to |
| :--- | :--- |
| `ca.crt` | The API (`FLEET_CA_CERT`) and the gateway (`FLEET_CA_CERT`). |
| `ca.key` | Only the gateway (`FLEET_CA_KEY`). Never give it to the API or the worker. |

The gateway reads the key as the user its container runs as. Make the file readable by that user; do not loosen its mode to make that work.

## 4. The blob store

`ODYSSEY_BLOB_URL` is required by the API, the worker and the gateway, and all three must point at the **same** store. The API writes uploaded sources and build logs, the worker reads them, and the gateway serves sources to the agents.

| Scheme | Use |
| :--- | :--- |
| `s3://bucket?region=…` | Production. AWS credentials come from the usual `AWS_*` variables. |
| `file://<dir>?create_dir=1&no_tmp_dir=1` | One machine, or a volume all three processes mount (the compose file does this). |

The code registers only `file://`, `mem://` and `s3://`. `mem://` is private to each process, so it never works across the three. The sources an organization keeps are capped by `ODYSSEY_SOURCE_QUOTA_GIB` (default 10). Upload size limits are in [Limits](../../reference/limits.md#uploads-and-sources).

## 5. Build the image

One image holds the three control-plane binaries:

```sh
docker build -t <registry>/odyssey:<tag> .
```

Its default entrypoint is `odyssey-api` (user `odyssey`, port 8095, a `HEALTHCHECK` on `/health`). Run the other two by overriding the entrypoint: `odyssey-worker` and `odyssey-gateway`. The agent image is a separate target of the same `Dockerfile` (`docker build --target agent …`); see [Clusters](../it/clusters.md#what-the-cluster-needs).

## 6. Configure and start

Every variable is in [Configuration](../../reference/configuration.md). These are the ones each process refuses to start without:

| Process | Required |
| :--- | :--- |
| `odyssey-api` | `DATABASE_URL`, `ODYSSEY_BLOB_URL`, `IAMKIT_URL`, `IAMKIT_ISSUER`, `IAMKIT_AUDIENCE`, `IAMKIT_ENVIRONMENT_ID`, `IAMKIT_APPLICATION_ID`, `IAMKIT_RESOURCE_ID` |
| `odyssey-worker` | `DATABASE_URL`, `ODYSSEY_BLOB_URL` |
| `odyssey-gateway` | `DATABASE_URL`, `ODYSSEY_BLOB_URL`, `FLEET_CA_CERT`, `FLEET_CA_KEY`, `FLEET_GATEWAY_HOSTS` |

A real deployment also needs these, even though the processes start without them:

| Setting | Why |
| :--- | :--- |
| `ODYSSEY_CLUSTER=fleet` on the API **and** the worker | The default is `fake`: apps "run" in memory and nothing reaches a cluster. `ODYSSEY_BUILDER` then defaults to `fleet`; the two must agree between API and worker. |
| `FLEET_CA_CERT` and `FLEET_GATEWAY_URL` on the API | Without them clusters can be listed but not registered (`FLEET_DISABLED`). Set both or neither. `FLEET_GATEWAY_URL` is the `host:port` agents dial and goes into enrolment tokens. |
| `FLEET_GATEWAY_HOSTS` on the gateway | Names and IPs agents reach it by. They go into its serving certificate, so include the host of `FLEET_GATEWAY_URL`. |
| `IAMKIT_WEB_CLIENT_ID` | Without it the console shows "El inicio de sesión todavía no está configurado. Avísale a TI." |
| `ODYSSEY_CONSOLE_URL` | The console's public URL, for links in assistants' answers, the GitHub callback and commit statuses. The default is `http://localhost:5178`. |
| `IAMKIT_INTROSPECTION_CLIENT_ID` and its secret | `/mcp` is mounted only with them. |
| `ODYSSEY_ACCESS_SIGNING_KEY` (or `_FILE`) and `IAMKIT_ACCESS_CLIENT_ID` with its secret | The sign-in of protected apps. Without both, no app can be set to "Solo tu equipo" or "Toda la empresa". Create the key with `openssl rand -base64 32` (an Ed25519 seed of 32 bytes in base64). |
| `IAMKIT_SERVICE_ACCOUNT_SECRET` on the API and the worker | Names people in the activity trail and on versions. Without it names are empty. |
| `CORS_ORIGINS` | Only when the console is served from a different origin than the API. Empty keeps CORS closed. |

GitHub ([GitHub App](../it/github-app.md)) and AI for apps are optional and have their own sections in Configuration.

### Order and migrations

1. Postgres and IAMKit.
2. Provision IAMKit; create the fleet CA.
3. Start the API, then the worker and the gateway (the compose file waits for a healthy API before it starts them).

Migrations run by themselves: the API, the worker and the gateway each apply every module's migrations and River's tables when they start, under one Postgres advisory lock, so replicas that boot together do not collide. The repository has no separate migration command.

Check each process on its health port:

| Process | Endpoint |
| :--- | :--- |
| API | `GET :8095/health` |
| Worker | `GET :8096/health` (`WORKER_HEALTH_PORT`) |
| Gateway | `GET :8098/health` (`GATEWAY_HEALTH_PORT`) |

## The console

The console is a Vite and React app in `frontend/`. The `Dockerfile` does not build it, and no Go code serves it, so you host the static files yourself. The repository documents only the development server (`make web`), not a production recipe.

```sh
cd frontend
npx -y npm@11 install   # npm 10.9 crashes on this tree
npm run build           # type-check, then vite build, into frontend/dist
```

Two things in the code shape how you host it:

- The console calls the API with relative URLs (`/api/v1/…` and `/api/config`). Serve it from the same origin as the API, with your reverse proxy sending `/api` to the API. If you use another origin, set `CORS_ORIGINS` and note that the code does not support pointing the console at another address.
- It uses browser-history routes (`/auth/callback`, `/administracion/clusteres`, …), so the static host must answer unknown paths with `index.html`.

## TLS and public URLs

The API serves plain HTTP; the code has no TLS option for it. Terminate TLS in front of it. The gateway is the opposite: it terminates mutual TLS itself (TLS 1.3 minimum).

| Address | Who uses it | Notes |
| :--- | :--- | :--- |
| Console | People's browsers | Its origin is `APP_URL` at provisioning and `ODYSSEY_CONSOLE_URL` at runtime. |
| API | Browsers, assistants, GitHub, agents' sign-in gates, upload clients | Its base URL is `IAMKIT_AUDIENCE` without `/mcp`. These paths are public (no token): `/webhooks/github`, `/github/setup`, `/access/authorize`, `/access/callback`, `/.well-known/…`, `/uploads/<ticket>`, `/skills/odicea.tar.gz`, `/api/config`, `/health`. A proxy must let them through. |
| IAMKit | Browsers and the API | `IAMKIT_URL` is how the API reaches it; set `IAMKIT_PUBLIC_URL` when browsers use another address. A plain-`http` `IAMKIT_URL` on a public host name is refused for the access sign-in unless `ODYSSEY_ACCESS_ALLOW_HTTP=true`, which is for development only. |
| Gateway | Cluster agents | A TCP port (8443) that clusters reach **outbound**. Behind a load balancer it must pass TCP through, not terminate TLS, and keep the client IP. |
| Apps | People using the apps | Set on the agent in each cluster ([Clusters](../it/clusters.md#what-the-cluster-needs)). |

## Connect the first cluster

With the gateway up and the API configured with `ODYSSEY_CLUSTER=fleet`, `FLEET_CA_CERT` and `FLEET_GATEWAY_URL`, Ana Torres opens **Administración → Clústeres** and chooses **Conectar un clúster**. The rest, including the agent image, the overlay a real cluster needs and the checks, is in [Clusters](../it/clusters.md).

## What the repository does not give you

- Manifests, a chart or Terraform for the control plane.
- Published container images for the control plane or the agent.
- A production build or host for the console.
- A documented upgrade procedure.

## See also

- [Configuration](../../reference/configuration.md)
- [Clusters](../it/clusters.md)
- [GitHub App](../it/github-app.md)
- [People and roles](../it/people-and-roles.md)
- [Run Odicea locally](local-development.md)
- [Security model](../../explanation/security-model.md)
