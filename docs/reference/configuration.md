# Configuration

Every environment variable each Odicea binary reads, grouped by area, with defaults and what each one does. Use it to set up a deployment or to find out why a process refuses to start.

Odicea has four binaries: `odyssey-api`, `odyssey-worker`, `odyssey-gateway` and `odyssey-agent`. They read only the environment (the agent also has command-line flags, listed [below](#odyssey-agent)). A blank value counts as unset. Fixed values that are not configurable (sizes, timeouts, expiries) are in [Limits](limits.md); the Make variables that feed these in local development are in [Make targets](make-targets.md).

## Which process reads what

| Area | `odyssey-api` | `odyssey-worker` | `odyssey-gateway` | `odyssey-agent` |
| :--- | :---: | :---: | :---: | :---: |
| [Database](#database) | yes | yes | yes | no |
| [IAMKit and sign-in](#iamkit-and-sign-in) | yes | only the [directory](#directory-service-account) | no | no |
| [Cluster and builder](#cluster-and-builder) | yes | yes | no | no |
| [Blobs](#blobs-and-uploads) | yes | yes | yes (read-only use) | no |
| [Fleet CA and gateway](#fleet-ca-and-gateway) | CA certificate + public address | no | CA certificate and key + listener | gateway address and CA pin come from its Secret |
| [GitHub](#github) | yes | yes | no | no |
| [AI for apps](#ai-for-apps-freerouter) | yes | yes | no | no |
| [MCP](#mcp-assistants) | yes | no | no | no |
| [Access sign-in](#access-sign-in-protected-apps) | yes | no | no | the gate and the agent, [own variables](#access-gate-odyssey-agent-access) |

> [!IMPORTANT]
> The API and the worker are two halves of one control plane and must be configured alike. These must have the same value in both: `ODYSSEY_CLUSTER`, `ODYSSEY_BUILDER`, `ODYSSEY_BLOB_URL` (the gateway needs the same store too), `ODYSSEY_FREEROUTER_URL`/`_KEY` and the `GITHUB_*` App settings. The API reads the cluster and the builder to ask for its secret key and live build logs; the worker applies the deployments and runs the builds.

## Database

| Variable | Default | Binaries | Meaning |
| :--- | :--- | :--- | :--- |
| `DATABASE_URL` | none (required) | api, worker, gateway | Postgres connection string. Every process shares one database. Migrations run at start. |

Every pooled connection gets `statement_timeout=30s`, `lock_timeout=10s`, `idle_in_transaction_session_timeout=60s` and `TimeZone=UTC`, unless the URL already sets that parameter. The pool holds at most 20 open connections. The worker and the workflow engine use `LISTEN`/`NOTIFY` on the same URL, so avoid a transaction-mode pooler in front of it.

## Process settings

| Variable | Default | Binary | Meaning |
| :--- | :--- | :--- | :--- |
| `PORT` | `8095` | api | HTTP port of the API (console API, `/mcp`, webhooks, `/health`). |
| `CORS_ORIGINS` | empty (CORS closed) | api | Comma-separated browser origins allowed to call the API. |
| `ODYSSEY_CONSOLE_URL` | `http://localhost:5178` | api, worker | The console's base URL, used for links in MCP answers, the GitHub setup callback and commit statuses. Set it in production: the API warns when MCP tools would link to a `localhost` console. |
| `WORKER_HEALTH_PORT` | `8096` | worker | Port of `GET /health` (answers 503 when the outbox statistics cannot be read, for example when the database is unreachable). |
| `GATEWAY_HEALTH_PORT` | `8098` | gateway | Port of `GET /health`. |

## IAMKit and sign-in

Odicea uses IAMKit for people, sign-in, roles and permissions. The API requires these; `make seed` (local) or `make iam-provision` (any IAMKit) writes them. See [Permissions](permissions.md).

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `IAMKIT_URL` | none (required) | How the API reaches IAMKit. |
| `IAMKIT_PUBLIC_URL` | `IAMKIT_URL` | How browsers reach IAMKit, when that differs. Served to the console in the public config. |
| `IAMKIT_ISSUER` | none (required) | The issuer of IAMKit's tokens (its `JWT_ISSUER`). |
| `IAMKIT_AUDIENCE` | none (required) | The token audience. It is the MCP endpoint's URL (`https://<odicea>/mcp`) and is immutable once the IAMKit resource exists. With MCP on, the API refuses to start unless its path is `/mcp`. The API's public base URL is this value without `/mcp`. |
| `IAMKIT_ENVIRONMENT_ID` | none (required) | The IAMKit environment Odicea lives in. |
| `IAMKIT_APPLICATION_ID` | none (required) | The Odicea application in IAMKit. |
| `IAMKIT_RESOURCE_ID` | none (required) | The IAMKit resource that holds the permission catalog from `iam/permissions.json`. |
| `IAMKIT_WEB_CLIENT_ID` | empty | The console's public OAuth client (code + PKCE). Served to the console; also treated as a first-party client when listing assistant connections. |

The API stops at start with `missing required environment: …` listing every required variable that is unset.

### Directory (service account)

Names the people in the activity trail and on versions. Optional: without it names stay empty and the API and the worker log a warning.

| Variable | Default | Binaries | Meaning |
| :--- | :--- | :--- | :--- |
| `IAMKIT_SERVICE_ACCOUNT_SECRET` | empty | api, worker | The backend's IAMKit service-account secret (`ik_svc_…`). Needs `IAMKIT_URL` and `IAMKIT_ENVIRONMENT_ID` in the same process. `make seed` provisions the account (`odicea-backend`, with `iam:members:read`). |
| `IAMKIT_SERVICE_ACCOUNT_SECRET_FILE` | empty | api, worker | File holding the secret. Setting both is an error. |

> [!NOTE]
> The worker reads `IAMKIT_URL` and `IAMKIT_ENVIRONMENT_ID` only for the directory; it needs no other IAMKit variable.

## Cluster and builder

| Variable | Default | Binaries | Meaning |
| :--- | :--- | :--- | :--- |
| `ODYSSEY_CLUSTER` | `fake` | api, worker | Where apps run. `fake`: in memory, apps "run" without a cluster (development and tests; an image repository containing `unhealthy` simulates a failed health check, `reject` a refused apply). `fleet`: the organization's enrolled clusters, through their agents. Anything else is refused. **Must agree between API and worker.** |
| `ODYSSEY_BUILDER` | `fake` with `ODYSSEY_CLUSTER=fake`, `fleet` with `fleet` | api, worker | Who builds uploaded sources. `fake`: a scripted log and a placeholder image. `fleet`: the environment's cluster, through its agent. `none`: every build fails with `NO_BUILDER`. `fake` is refused with `ODYSSEY_CLUSTER=fleet`, and `fleet` is refused with `fake`. **Must agree between API and worker.** |
| `ODYSSEY_SOURCE_QUOTA_GIB` | `10` | api (worker validates it) | Compressed uploaded sources an organization keeps at once, in GiB (a whole number ≥ 1). When full, the oldest already-built sources are dropped early; if none can go, the upload is refused with `SOURCE_QUOTA`. |

## Blobs and uploads

| Variable | Default | Binaries | Meaning |
| :--- | :--- | :--- | :--- |
| `ODYSSEY_BLOB_URL` | none (required) | api, worker, gateway | Where uploaded sources and build logs live: `file://<dir>?create_dir=1&no_tmp_dir=1` on one machine, `s3://bucket?region=…` in production (AWS credentials come from the usual `AWS_*` variables). The API, the worker and the gateway must point at the same store: the API writes, the worker reads and the gateway serves sources to the agents. |

The size limits of an upload are not configurable; see [Limits](limits.md#uploads-and-sources).

## Fleet CA and gateway

The fleet CA signs every agent's certificate. Only the gateway holds its key; the API holds the certificate to verify.

| Variable | Default | Binaries | Meaning |
| :--- | :--- | :--- | :--- |
| `FLEET_CA_CERT` | none | api, gateway | Path to the CA certificate (PEM). Required by the gateway. In the API it must be set together with `FLEET_GATEWAY_URL`; with neither, clusters can be listed but not registered. |
| `FLEET_GATEWAY_URL` | none | api | `host:port` agents dial, written into enrolment tokens. Set together with `FLEET_CA_CERT` or not at all (otherwise the API refuses to start). |
| `FLEET_CA_KEY` | none (required) | gateway | Path to the CA private key (PEM). Never give it to the API or the worker. |
| `FLEET_GATEWAY_HOSTS` | none (required) | gateway | Comma-separated names and IPs agents reach the gateway by: they go into its serving certificate. |
| `FLEET_GATEWAY_LISTEN` | `:8443` | gateway | Listen address of the agents' mTLS gRPC tunnel. |

Create the CA once with `odyssey-gateway ca init <cert.pem> <key.pem>` (valid ten years; it refuses to overwrite, because replacing the CA disconnects every cluster). `make setup` does it for local development. The gateway's own certificate is signed by that CA, valid seven days and renewed daily. Several gateway replicas can run behind a TCP (not TLS-terminating) load balancer. See [Clusters](../guides/it/clusters.md).

## GitHub

GitHub is off until `GITHUB_APP_ID` is set; then every other required variable must be set too, or the process refuses to start (`GITHUB_APP_ID is set; also required: …`). Secrets are files, never values. Without it, GitHub endpoints answer `GITHUB_NOT_CONFIGURED` and the console says so. Read by the API and the worker.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `GITHUB_APP_ID` | unset (GitHub off) | The App's numeric id. |
| `GITHUB_APP_SLUG` | required with the id | The App's name in `github.com/apps/<slug>`. |
| `GITHUB_APP_PRIVATE_KEY_FILE` | required with the id | RSA private key (PEM) that signs the App's JWTs. |
| `GITHUB_WEBHOOK_SECRET_FILE` | required with the id | File with the secret that signs webhook deliveries. |
| `GITHUB_CLIENT_ID` | required with the id | OAuth client id that verifies who came back from installing the App. |
| `GITHUB_CLIENT_SECRET_FILE` | required with the id | File with that client's secret. |
| `GITHUB_API_URL` | `https://api.github.com` | GitHub's API URL. For GitHub Enterprise Server: `https://<host>/api/v3`. |
| `GITHUB_URL` | `https://github.com` | GitHub's web URL. |
| `ODYSSEY_STATE_KEY_FILE` | derived from the webhook secret | File with the key that signs the install state (at least 16 bytes). Without it a key is derived from `GITHUB_WEBHOOK_SECRET_FILE`. |

`odyssey-api github-app create` registers the App through GitHub's manifest flow and writes these secrets (`make github-app`). Its flags: `--public-url` (default `http://localhost:8095`), `--webhook-url` (default `<public-url>/webhooks/github`), `--org` (default your personal account), `--name` (default `Odicea`), `--out` (default `.dev-secrets/github`), `--github-url`, `--api-url`, `--listen` (default `127.0.0.1:0`) and `--force`. See [GitHub for IT](../guides/it/github-app.md).

## AI for apps (freerouter)

Optional. The API and the worker create and revoke each app's own key at the organization's AI gateway with the platform's service key. Without both URL and key the capability is absent: apps cannot turn the AI on and the console says so. See [AI for apps](../ai.md).

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `ODYSSEY_FREEROUTER_URL` | empty | freerouter's API address, where keys are made and revoked. |
| `ODYSSEY_FREEROUTER_KEY` | empty | The platform's service key at freerouter. |
| `ODYSSEY_FREEROUTER_KEY_FILE` | empty | File holding the key, instead of the value. Setting both is an error. |
| `ODYSSEY_FREEROUTER_APP_URL` | `ODYSSEY_FREEROUTER_URL` | The address apps reach the gateway by. `OPENAI_BASE_URL` (with `/v1`) and `ANTHROPIC_BASE_URL` are built from it. |

URL and key go together: setting only one stops the start.

## MCP (assistants)

`/mcp` is mounted only when the introspection client is set; otherwise the API logs `MCP is off`. See [MCP tools](mcp-tools.md).

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `IAMKIT_INTROSPECTION_CLIENT_ID` | empty (MCP off) | Confidential IAMKit client the API uses to introspect every assistant token (revocation takes effect within seconds). |
| `IAMKIT_INTROSPECTION_CLIENT_SECRET` | empty | Its secret. Required with the id. |
| `IAMKIT_INTROSPECTION_CLIENT_SECRET_FILE` | empty | File with the secret. Setting both is an error. |
| `ODYSSEY_MCP_CLIENTS` | `claude_code,codex,cursor,vscode,antigravity` | Assistants the console offers under "Conecta tu asistente", in this order. Unknown keys are ignored. |
| `IAMKIT_MCP_CLIENT_CURSOR_ID` | empty | OAuth client IAMKit registered for Cursor. Without it Cursor is not offered. |
| `IAMKIT_MCP_CLIENT_ANTIGRAVITY_ID` | empty | The same for Antigravity. |

## Access sign-in (protected apps)

The sign-in that apps set to "team" use (see [access](../access.md)). It is on only when the signing key and the IAMKit access client are both set; with only one, the API logs a warning and `/access/*` answers 404, so no app can be protected.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `ODYSSEY_ACCESS_SIGNING_KEY` | empty | Ed25519 seed (32 bytes) in base64 that signs access tickets. |
| `ODYSSEY_ACCESS_SIGNING_KEY_FILE` | empty | File holding it. Setting both is an error. `make access-key` writes one for local development. |
| `ODYSSEY_ACCESS_PREVIOUS_KEYS` | empty | Comma-separated seeds of earlier keys. Only their public halves are published, so gates with a cached key set keep verifying during a rotation. |
| `IAMKIT_ACCESS_CLIENT_ID` | empty | The confidential authorization-code IAMKit client of the access sign-in; its redirect is `<API>/access/callback`. |
| `IAMKIT_ACCESS_CLIENT_SECRET` | empty | Its secret. Required with the id. |
| `IAMKIT_ACCESS_CLIENT_SECRET_FILE` | empty | File with the secret. Setting both is an error. |
| `ODYSSEY_ACCESS_ALLOW_HTTP` | `false` | `true` accepts apps on plain `http` at any host (otherwise only `localhost`, `*.localhost`, `127.0.0.1` and `*.k3d.internal`) and a plain-`http` `IAMKIT_URL` on a public host name. Development only. |

Without `ODYSSEY_ACCESS_ALLOW_HTTP`, the sign-in refuses a plain-`http` `IAMKIT_URL` unless it is local or private (a single-label name, `*.svc`, `*.svc.cluster.local`, `*.internal`).

## `odyssey-agent`

The data-plane agent runs in each cluster and dials out to the gateway. Flags win over the environment; each flag below names its variable. The agent also takes the `controller-runtime` zap logging flags and finds the cluster through `--kubeconfig`, `KUBECONFIG`, the in-cluster service account or `~/.kube/config`.

| Variable (flag) | Default | Meaning |
| :--- | :--- | :--- |
| `POD_NAMESPACE` (`--secret-namespace`) | `odyssey-system` | Namespace of the agent's Secrets. In the install it comes from the downward API. |
| `POD_NAME` | none | The agent's own pod: build pods run the agent's image for their prepare step. |
| `ODYSSEY_SERVICE_ACCOUNT` | none | The agent's service account. Required in a cluster: the agent refuses to start there without it (the install passes `spec.serviceAccountName`). |
| `ODYSSEY_AGENT_SECRET` (`--secret`) | `odyssey-agent` | The Secret with the enrolment token, then the agent's identity. Empty means no tunnel. |
| `ODYSSEY_GATEWAY_DIAL` (`--gateway-dial`) | none | `host:port` to dial when it differs from the gateway address in the Secret (an agent run outside the cluster). |
| `ODYSSEY_AGENT_HEALTH_ADDR` (`--health-addr`) | `:8097` | Liveness (`/healthz`) and readiness (`/readyz`) address. |
| `ODYSSEY_APPS_DOMAIN` (`--domain`) | `apps.odyssey.localhost` | Base domain of the apps' hosts. |
| `ODYSSEY_APPS_URL_SCHEME` (`--url-scheme`) | `http` | Scheme of the reported app URLs. |
| `ODYSSEY_APPS_URL_PORT` (`--url-port`) | `0` (default port) | Port of the reported app URLs. |
| `ODYSSEY_INGRESS_CLASS` (`--ingress-class`) | cluster default | Ingress class of the apps' Ingresses. |
| `ODYSSEY_INGRESS_NAMESPACE` (`--ingress-namespace`) | `kube-system` | Namespace of the ingress controller's pods, the only ones that reach an app from outside its namespace. |
| `ODYSSEY_INGRESS_POD_LABEL` (`--ingress-pod-label`) | `app.kubernetes.io/name=traefik` | `key=value` label of those pods. |
| `ODYSSEY_DNS_NAMESPACE` (`--dns-namespace`) | `kube-system` | Namespace of the cluster's DNS pods. |
| `ODYSSEY_DNS_POD_LABEL` (`--dns-pod-label`) | `k8s-app=kube-dns` | `key=value` label of the DNS pods. |
| `ODYSSEY_OPERATOR_NAMESPACE` (`--operator-namespace`) | `cnpg-system` | Namespace of the CloudNativePG operator, which may reach the databases. |

Flags without a variable: `--ready-deadline` (default `3m`: a rollout without progress for this long fails), `--health-grace` (default `2m`: a running pod failing its health check for this long fails the app; keep it below the deadline), `--metrics-addr` (default `0`, off), `--leader-elect` (default off: elect a leader to run more than one replica) and `--leader-election-namespace`.

### Building

The agent builds uploaded sources only when `ODYSSEY_REGISTRY` is set; the cluster is then a builder. See [Clusters](../guides/it/clusters.md).

| Variable (flag) | Default | Meaning |
| :--- | :--- | :--- |
| `ODYSSEY_REGISTRY` (`--registry`) | empty (does not build) | Registry built images go to, as the nodes reach it (`host[:port][/path]`). |
| `ODYSSEY_REGISTRY_INSECURE` (`--registry-insecure`) | `false` | `true` or `1`: talk to the registry over plain HTTP (a local registry). |
| `ODYSSEY_BUILD_PLATFORM` (`--build-platform`) | `linux/<the agent's arch>` | Platform images are built for. |
| `ODYSSEY_AGENT_IMAGE` (`--agent-image`) | the agent's own pod image | The agent's image, used by each build's prepare step. Needed outside a cluster. |
| `ODYSSEY_SCAN` (`--no-scan`) | scanning on | `false` turns the vulnerability scan of built images off (a cluster that cannot download Trivy's database). |
| `ODYSSEY_SCAN_IMAGE` (`--scan-image`) | the agent's pinned Trivy image | Trivy's image, pinned by digest. |
| `ODYSSEY_SOURCE_ADDR` (`--source-addr`) | `:8099` | Address the agent serves sources to build pods on. |
| `ODYSSEY_SOURCE_URL` (`--source-url`) | `http://odyssey-agent-source.odyssey-system.svc:8099` | URL build pods reach that server at. |

The build pods run `odyssey-agent builder …` with variables the agent sets for them (`ODYSSEY_WORK`, `ODYSSEY_IMAGE`, `ODYSSEY_CACHE_REF`, `ODYSSEY_CACHE_KEY`, `ODYSSEY_FRONTEND`, `ODYSSEY_PLATFORM`, `ODYSSEY_REGISTRY_INSECURE`, `ODYSSEY_REUSE_IMAGE`, `ODYSSEY_SOURCE_URL`, `ODYSSEY_SOURCE_SIZE`, `ODYSSEY_SOURCE_TOKEN_FILE`, `ODYSSEY_SCAN_REPORT`, `ODYSSEY_SCAN_TOOL`, `ODYSSEY_SCAN_DB_METADATA`). You never set these yourself.

### Access gate (`odyssey-agent access`)

The sign-in gate is another Deployment of the agent image. The agent itself needs `ODYSSEY_ACCESS_ENABLED` to know the gate is installed.

| Variable (flag) | Default | Process | Meaning |
| :--- | :--- | :--- | :--- |
| `ODYSSEY_ACCESS_ENABLED` (`--access-enabled`) | `false` | agent | `true` or `1`: the gate is installed, so apps may require sign-in. |
| `ODYSSEY_ACCESS_SECRET` (`--secret`) | `odyssey-access` | agent, gate | The Secret with the cookie key. The agent creates it; the gate only reads it. |
| `ODYSSEY_ACCESS_SIGNING_SECRET` (`--signing-secret`) | `odyssey-access-signing` | agent, gate | The Secret with the identity-assertion signing key (keys `assertion-key`, and `assertion-key-previous` while rotating). |
| `ODYSSEY_ACCESS_JWKS_URL_INTERNAL` (`--access-jwks-url-internal`) | `http://odyssey-access.odyssey-system.svc:8099/jwks` | agent | The gate's `/jwks` as apps reach it; injected into protected apps as `ODICEA_AUTH_JWKS_URL`. |
| `ODYSSEY_ACCESS_VERIFY_URL` (`--access-verify-url`) | `http://odyssey-access.odyssey-system.svc:8098/verify` | agent | The gate's `/verify` URL as Traefik reaches it. |
| `ODYSSEY_ACCESS_ISSUER` (`--issuer`) | none (required) | gate | The control plane's **public** base URL: the `iss` claim and where browsers sign in. |
| `ODYSSEY_ACCESS_JWKS_URL` (`--jwks-url`) | `<issuer>/.well-known/odicea-access-keys` | gate | Where the gate fetches ticket keys, when the cluster reaches the control plane by another address than the browser. |
| `ODYSSEY_ACCESS_INSECURE_ISSUER` | `false` | gate | `true` allows a non-local `http` issuer or JWKS URL. Otherwise `https` is required except for `localhost`, `127.0.0.1`, `*.localhost` and `host.k3d.internal`. |
| `ODYSSEY_ACCESS_LISTEN` (`--listen`) | `:8098` | gate | The gate's address (accepts only the ingress controller). |
| `ODYSSEY_ACCESS_KEYS_LISTEN` (`--keys-listen`) | `:8099` | gate | Address of `GET /jwks`, the public keys of identity assertions (open to the whole cluster). |
| `ODYSSEY_ACCESS_HEALTH_LISTEN` (`--health-listen`) | `:8097` | gate | Liveness address. |

Protected apps receive `ODICEA_AUTH_JWKS_URL`, `ODICEA_AUTH_ISSUER`, `ODICEA_AUTH_AUDIENCE` and `ODICEA_AUTH_HEADER` from the agent; see [Identity in your app](../guides/identity-in-your-app.md).

## Local stack (`.env`)

`make setup` copies `.env.example` to `.env` with random secrets, and `make seed` appends the `IAMKIT_*` ids. These variables are read by `docker-compose.yml`, the Makefile and the scripts, not by the binaries. The compose file maps them onto the binary variables above.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `POSTGRES_PASSWORD` | random from `make setup` | Password of the shared Postgres (the API's and IAMKit's databases). |
| `DB_PORT` | `5438` | Host port of Postgres. |
| `IAMKIT_SRC` | `../iamkit` | Checkout IAMKit is built from (a clone next to this repository). |
| `IAMKIT_PORT` | `8085` | Host port of IAMKit. |
| `OIDC_HMAC_SECRET`, `IAMKIT_ENCRYPTION_KEY` | random from `make setup` | IAMKit's own secrets. |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5178` | IAMKit's allowed origins (not the API's `CORS_ORIGINS`). |
| `ODYSSEY_PORT` | `8095` | Host port of the API; the Makefile passes it as `PORT`. |
| `ODYSSEY_MCP_URL` | `http://localhost:$ODYSSEY_PORT/mcp` | The MCP resource audience `make seed` registers. |
| `MCP_PRESETS` | `claude_code,codex,cursor,vscode,antigravity` | Assistants `make seed` admits to MCP. Empty means no MCP resource. |
| `ACCESS_API_URL` | `$ODYSSEY_MCP_URL` without `/mcp` | API base URL for the access client's redirect. |
| `DEMO_EMAIL`, `DEMO_PASSWORD` | `ti@demo.local`, random from `make setup` | The demo user created by `make seed`, with the first role of the manifest (TI). |
| `GATEWAY_PORT` | `8443` | Host port of the compose gateway. |
| `ODYSSEY_UID`, `ODYSSEY_GID` | your user, from `make setup` | The compose gateway runs as the owner of `secrets/fleet/` so the CA key stays `0600`. |

For the full local run see [Local development](../guides/operations/local-development.md).

## Test variables

| Variable | Used by | Meaning |
| :--- | :--- | :--- |
| `ODYSSEY_TEST_DATABASE_URL` | `make test-db` | Admin connection string of an existing Postgres; each test creates and drops its own database. Without it, tests start a Postgres container per package. |
| `ODYSSEY_TEST_REQUIRE_DB` | `make test-db`, CI | When set, a missing database fails the test instead of skipping it. |
| `ODYSSEY_TEST_POSTGRES_IMAGE` | tests | Postgres image for the throwaway container. |
| `ODYSSEY_TEST_KUBE`, `ODYSSEY_TEST_KUBECONFIG`, `ODYSSEY_TEST_APPS_PORT`, `ODYSSEY_TEST_BUILD`, `ODYSSEY_TEST_CNPG_KUBECONFIG`, `ODYSSEY_TEST_ESO_KUBECONFIG` | `make test-kube`, `test-kube-k3d`, `test-build`, `test-cnpg`, `test-eso` | Switch on and point the cluster end-to-end tests. See [Make targets](make-targets.md#testing). |

## See also

- [Install Odicea](../guides/operations/install.md)
- [Make targets](make-targets.md)
- [Limits](limits.md)
- [Permissions](permissions.md)
