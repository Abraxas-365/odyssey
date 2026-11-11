# Known limitations

Where you may run into limits, what to expect and what you can do about it. It describes the current source version; your installed version may differ. It is not a roadmap.

For numeric limits (sizes, counts, time windows) see [Limits](../reference/limits.md). For how Odicea protects things, and where that protection stops, see [Security model](../explanation/security-model.md).

## Apps and builds

- **There is one environment per app.** Every app has a `production` environment. The API names `staging` and `preview` kinds, but the console and the API do not create them yet. To try a change safely, create a second app at the *Sandbox* tier.
- **Odicea builds with a `Dockerfile` or with Railpack, and nothing else.** If neither can work out how to build or start the app, the version fails with `UNSUPPORTED_APP` and the log says why. Add a `Dockerfile` to say exactly how.
- **A cluster builds only if it has a registry.** An agent started without a registry cannot build, and a build there fails with `NO_BUILDER`. Ask IT, or publish an already-built image with the assistant (`deploy_image`).
- **A folder upload is limited.** At most 100 MiB compressed, 500 MiB unpacked and 20 000 files, and each organization has a source quota (10 GiB by default). Leave out `node_modules`, build output and data files. See [Limits](../reference/limits.md).
- **`deploy_image` is not built, not scanned and takes one part.** The image must be pinned by digest and belongs to an app of a single part. See [Publish an app](../guides/publish.md).
- **An app has at most 8 parts**, and several parts need an agent that supports them. See [Several parts](../guides/several-parts.md).
- **A published version cannot be edited.** Variables, secrets and the access mode apply by publishing the same version again, which goes through the steps and the health check. See [Variables and secrets](../guides/variables-and-secrets.md).
- **Going back does not restore data.** The version returns, but a database keeps what it has now. Take your own backup before a risky change.
- **The app's address is fixed.** The slug cannot be changed after the app is created, and apps live under the company's apps domain. Custom domains per app are not offered. To use another name, create a new app.
- **Scans can make a version wait.** Several builds that finish together queue their scans, so a version can wait while another is scanned. The wait ends after ten minutes.

## Resources

- **Data does not move.** When apps move to another cluster, they are deployed from scratch there, and the data of their databases, caches and files stays behind. A move is refused (`RESOURCES_UNSUPPORTED`) when the target cluster cannot prepare one of the app's resources. See [Connect a cluster](../guides/it/clusters.md).
- **Storage only grows.** A smaller size is ignored, and growing needs a storage class that allows it. Otherwise the resource shows "Falló" with the reason.
- **At most 10 storage items per app environment.** Items being deleted still count.
- **Database engines are PostgreSQL, a cache, files and existing resources.** The catalog's recipes are fixed by the release; IT chooses what is offered, not new engines. See [The catalog](../guides/it/catalog.md).
- **Databases need the operator.** A cluster without the database operator or a storage class does not offer that kind. The dialog that adds storage says so and lists the reason.
- **Existing resources are only connected.** For "Ya existe en la empresa" Odicea passes the connection to the app. It does not create, size or delete it. Removing the connection does not delete its data.
- **Secrets from the company's store need External Secrets.** Without it on the cluster, an app that points to the store fails with a message that says so.

## Access

- **"Solo tu equipo" means your Odicea organization.** It does not check groups or teams inside IAMKit. To let another group in, its people must belong to the organization. See [People and roles](../guides/it/people-and-roles.md).
- **A protected app needs a cluster with the sign-in gate.** Otherwise the console offers only "Cualquiera con el enlace". See [Who can open it](../guides/who-can-open-it.md).
- **"Cualquiera con el enlace" has no sign-in, so the app does not know who uses it.** Never put private data in such an app.
- **The identity headers are not proof by themselves.** Another pod in the cluster can call an app directly and invent `X-Odicea-*`. An app that decides things about people must verify the signed assertion. See [Identity in your app](../guides/identity-in-your-app.md).
- **The sign-in gate runs as one replica.** Its memory of used tickets is per pod.
- **A sign-in lasts 8 hours in an app.** After it, the person is sent to sign in again.
- **The console is for the console's token only.** A token from an assistant opens the MCP endpoint, not the console's API.

## AI

- **Consumption is not shown.** The console cannot say what an app spent, because the AI gateway does not report usage per app key. Ask IT about the company's limits. See [AI for your apps](../guides/ai-for-apps.md).
- **One key per app, and it does not expire.** Rotate or turn off the AI to replace it.
- **It needs a company AI gateway and a cluster that announces `odicea.ai`.** Otherwise turning AI on is refused with `AI_UNAVAILABLE`.
- **The four variable names are reserved while AI is on.** `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL` cannot be set by your app. Remove yours before turning it on.

## Clusters

- **Egress rules need a network plugin that enforces them.** Odicea writes the policies and reports "Aplicada en el clúster", but it cannot prove that traffic is blocked. Test with a blocked address on every new cluster. See [Egress](../guides/it/egress.md).
- **`internet` mode does not close the Kubernetes API of a managed cluster that has a public address.** Use "Solo direcciones permitidas" there.
- **Revoking a cluster is permanent.** To use it again, register it as a new cluster. Apps left on a revoked cluster keep running but cannot be updated (`CLUSTER_REVOKED`) until IT moves them.
- **Apps live on one cluster at a time.** An environment is placed on the organization's first connected active cluster when it first needs one. The console does not let you choose per app; IT moves apps only off a revoked cluster.
- **Scans are queued, one at a time per cluster.** A cluster that cannot download the vulnerability database (air-gapped) should turn scans off. Then versions are shown "Sin escaneo".
- **Base manifests assume the local setup.** A real cluster needs its own overlay (ingress, sign-in address, registry). Re-applying the base removes your configuration. See [Connect your first real cluster](../tutorials/first-real-cluster.md).

## Governance

- **Not everything is judged by IT's rules.** Changing a variable or a secret, creating storage without connecting it, rotating an AI key and requests that change nothing are not judged. See [IT's rules](../guides/it/rules.md).
- **Going back is not held by default.** There is no built-in rule for it. IT can write one.
- **A scan can be bypassed.** The built-in "no critical vulnerabilities in production" rule does not stop an image that was not scanned, and a builder can make a scan fail. Turn on "Producción exige revisión de vulnerabilidades" to require one. See [Security model](../explanation/security-model.md#scanning).
- **An approval runs with the requester's old permissions.** A person deactivated after asking is not detected for up to seven days. IT can reject the request, and a new deny rule applies when it runs. See [Approvals](../guides/it/approvals.md).
- **The log is tamper-evident, not tamper-proof.** Someone with write access to the database could rebuild it. Protect and back up the database. See [Inventory and audit](../guides/it/inventory-and-audit.md).
- **Only TI sees the log and the inventory.** Others see "Solo TI puede ver el registro de la empresa".
- **One inventory read looks at 500 apps.** The page says when it left some out. Narrow it with the filters.

## Console

- **The console is in Spanish only.** The documentation is in English and quotes the Spanish labels. See [Console tour](../guides/console-tour.md).
- **Technical details are off by default.** "Detalles técnicos" adds more detail to the same screens. It never changes what is possible.
- **A browser must be able to run the upload.** The console packs the folder in the browser. Very large folders warn at 100 MiB, 500 MiB or 20 000 files.
- **The console signs out of every tab together.** Signing out in one tab ends the session in the others in the same browser.

## Assistants

- **The console offers Claude Code, Codex, VS Code, Cursor and Antigravity.** Cursor and Antigravity appear only when IT has registered them. See [Connect an assistant](../guides/connect-an-assistant.md).
- **The assistant uploads from your computer.** `deploy_folder` gives the assistant a command that tars and uploads the folder. It must be able to run shell commands and reach Odicea. A sandbox that cannot will fail.
- **An assistant never sees a secret.** It can only declare the name and give you a link. You type the value in the console.
- **An assistant can do what you can, and no more.** If a tool answers that it needs IT's approval, it stops. Do not ask it to work around that.
- **The skill is installed on your computer.** After an upgrade of Odicea, install it again from "Tu asistente" so it matches the tools. See [MCP tools](../reference/mcp-tools.md).

## Documentation and reporting problems

If the documentation and the behavior disagree, the code and [AGENTS.md](../../AGENTS.md) win, and the page should be fixed. Say which page, what it says and what you saw.

When you report a problem, include:

- the version or commit you run and how you installed it;
- what you did, what you expected and what happened;
- the code in the error (for example `POLICY_DENIED`) and the request id the console shows with "Detalles técnicos";
- for a build, the relevant lines of the build log.

Do not post secrets, tokens, enrolment tokens, upload links or the contents of your database. Replace real hostnames and people with `example.com` and made-up names.
