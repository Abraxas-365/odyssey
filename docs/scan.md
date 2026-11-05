# Escaneo de imágenes — scan (cut 3c)

Every image Odicea builds is scanned for known vulnerabilities, in the
customer's own cluster, right after the build. The result feeds IT's rules
(`scan.*`, `docs/policy.md`), the inventory (`docs/inventory.md`) and the app
page ("Seguridad").

## Flow

1. The build succeeds (`build.succeeded`). The control plane starts the
   `build.scan` workflow (`build.WorkflowScan`, one step `scan`).
2. If the build's cluster lists the capability **`odicea.scan`**, the workflow
   asks the agent (`fleet.v1` `ScanReport`/`ScanStatus`) to scan the build's
   image digest. Otherwise it records a scan with
   `error = "Este clúster no revisa vulnerabilidades."` (`ScanUnsupportedMessage`).
3. The agent runs **one Trivy Job per build** in the `builds` namespace
   (`agent/controllers/build/scan.go`). Its pod has three Trivy steps and a
   counter, in order: `trivy-db` and `trivy-javadb` (init containers that bring
   the vulnerability database and the Java index up to date in the cache, TLS
   verified), `trivy` (init: `trivy image --scanners vuln --skip-db-update
   --skip-java-db-update` on `<repository>@<digest>`, with the registry's
   credentials) and `summary` (the agent's image: it counts Trivy's JSON report
   by severity, reading it as a stream and refusing one past 256 MiB, and
   writes the counts to its termination message). The report lives in a
   512 MiB `emptyDir`. The Job is not owned by the build (the build is released
   right away; the scan outlives it), runs once (no retries) and is the scan's
   whole state: the agent reads the outcome from it.
4. Trivy's databases (about 2 GB) are cached in the PVC `odyssey-trivy-cache`
   (8 Gi, `ReadWriteOnce`).
5. **Scans run one at a time per cluster.** The cache has a single writer, so
   the agent never runs two scan pods together: every scan Job is created
   *suspended* (`spec.suspend`) and a queue (`ScanQueue`, leader only, every
   10 s) resumes the oldest one when no scan (or warm-up) Job is running, so
   scans go first in, first out. The wait is outside the pod: a suspended Job
   has no pod and its `activeDeadlineSeconds` (15 min, the scan's own time
   limit) starts when it is resumed. The agent's role needs `patch` on `jobs`
   for this. Consequence for users: with several builds finishing together,
   the last scan starts after the others; the deployment's `authorize_scan`
   waits at most 10 minutes (below) and then proceeds with `scan.known = false`.
   A scan takes about a minute once the database is cached, so the queue is
   short in practice; if a cluster routinely builds more than ~10 images in 10
   minutes, those deployments will see `known = false`.
6. **First scan and warm-up.** The first download is minutes long (the database
   is ~1.7 GB). The agent therefore creates a **warm-up Job** (`scan-warmup`,
   the two download steps only, in the same queue) when it starts and again
   daily, so the first build's scan does not carry the download. While it runs,
   scans queue behind it. If the download fails the scan fails with the
   download's error (`known = false`), never a silent clean result.
7. If the agent cannot even create a scan Job (the API server refuses it) it
   retries every 10 s for 5 minutes after the build ended, then gives up and
   reports `error = "the scan could not be started: …"`, so the control plane
   records "never" at once instead of waiting 30 minutes. This never delays the
   build's cleanup.
8. The workflow records `build.Scan{digest, critical, high, medium, low, tool,
   db_updated_at, error, scanned_at}` on the build and publishes
   **`build.scanned`** (golden `internal/build/testdata/events/build.scanned.v1.json`).
   A succeeded build always ends up with a scan, even when it could not be done
   (`error` says why): `nil` means "not yet", an error means "never".
   The scan is kept only if its digest is the build's own
   (`ScanMismatchMessage` otherwise).

### Why the database is downloaded in its own step (Trivy `--insecure`)

The dev registry speaks plain HTTP, so Trivy needs `--insecure` to pull the
image. In Trivy 0.76.2 that flag (and `TRIVY_NON_SSL`) is **global**:
`pkg/commands/artifact/run.go` builds one HTTP transport from it
(`xhttp.NewTransport(Options{Insecure: …})`, `InsecureSkipVerify`) that the
image pull *and* the database download (`pkg/oci/artifact.go`) share, and the
`name.Insecure` option also lets the database reference fall back to plain
HTTP. There is no registry-only option (`registry.mirrors` shares the
transport). So the database is downloaded by steps that never get `--insecure`,
and the scan step runs with `--skip-db-update --skip-java-db-update`, so
`--insecure` reaches only the registry. Over TLS registries (production) the
scan step gets no `--insecure` at all. A test pins this in the manifest
(`TestScanJob`).

**The build does not wait for the scan**; it is `ready` as soon as the image is
pushed. Only the deployment's `authorize_scan` step waits (below).

## What the numbers are

Counts of vulnerabilities by severity (critical, high, medium, low). A CVE is
counted once per package (the same CVE in two layers of one package is one
finding; in two packages it is two). `UNKNOWN` severities are not counted. The
tool is named with its version (`trivy 0.76.2`) and the database's build time.

## `authorize_scan`: the deployment's second gate

After the build, the deploy workflow's step `authorize_scan` judges the
deployment again with the scan in `scan.*` (same rules, same verdicts and same
approval machinery as `authorize`; `docs/policy.md`, `docs/approvals.md`):

- It **polls** for the scan up to **10 minutes** after the build finished
  (`release.ScanWait`). If it does not arrive, it goes on with
  `scan.known = false` (logged).
- `scan.known = false` also for: a scan that could not be done (`error`), a
  cluster without `odicea.scan`, a scan of another digest. In all those cases
  `scan.checked = true` (the scan was looked at; at the first judgment, before
  the build, `scan.checked` is false).
  **An unknown scan is not a clean one**: a rule that cares says
  `scan.known && scan.critical > 0`, and the built-in
  `production_critical_vulnerabilities` does not match an unknown scan.
  **That rule alone is therefore bypassable**: the builder controls the image
  and can make its scan fail (`known = false`, the image goes live
  unreviewed). To close it, enable the built-in **`production_requires_scan`**
  (*Producción exige revisión de vulnerabilidades*, approval,
  `scan.checked && !scan.known`, off by default; `docs/policy.md`): a
  production build whose scan did not produce a result waits for IT's approval.
  It is only judged at this step, never at `authorize`.
- **deny** → the deployment fails with `POLICY_DENIED` naming the rule
  (shown as the build's refusal; nothing reached the cluster).
- **approval** → the request is held at `step = authorize_scan` and the run
  parks (`approval_scan` acts on the decision). A rule that already asked at
  `authorize` is not asked again.
- A request this step opened earlier is withdrawn when the verdict no longer
  asks (denied, or allowed after a rule was switched off).

## What is not scanned (documented limits)

- **`deploy_image`** (an image published as it was): nothing is built here, so
  it has no scan (`scan.known = false`; the inventory says "Sin escaneo").
- Config-only deployments (no build) and auto-rollbacks.
- A cluster whose agent runs with `ODYSSEY_SCAN=false` (air-gapped clusters
  that cannot download Trivy's database) or without a Trivy image: it does not
  list `odicea.scan`.
- Only OS packages and language dependencies Trivy's `vuln` scanner knows
  (no secrets, misconfigurations or licenses).
- A scan is a point in time: the inventory shows the scan of the version online,
  it is not re-run when the database updates.

## Agent configuration

| Env | Meaning |
|---|---|
| `ODYSSEY_SCAN` | `false` turns scans off (and the capability) |
| `ODYSSEY_SCAN_IMAGE` | Trivy's image pinned by digest (default: the agent's pinned one) |

The agent's role gains `create`/`get` on `persistentvolumeclaims` (the cache) and
`patch` on `jobs` (resuming queued scans) and on `odysseybuilds` (recording a
scan it gave up starting).

Limits that keep the queue moving:

- Each database download has its own 5-minute limit: a cluster that cannot
  reach the database fails its scans in minutes (`known = false`), it does not
  hold the queue for a whole scan deadline. Air-gapped clusters should run
  with `ODYSSEY_SCAN=false`.
- A failed warm-up (quota, RBAC) is logged; scans still start and download
  the databases themselves.
- A resumed scan Job still not failed 5 minutes past its time limit (its pod
  stuck on a lost node) no longer holds the queue.
- Queued Jobs need Job `suspend` (Kubernetes ≥ 1.24; its clock for
  `activeDeadlineSeconds` starts when the Job is resumed).

## Console

The app page shows "Seguridad" for the version online: "Escaneando…" (the
console asks again every 10 s until the scan arrives), counts of críticas/altas,
"Sin vulnerabilidades graves", or why it could not be done. The inventory and
Inicio show the same words.
