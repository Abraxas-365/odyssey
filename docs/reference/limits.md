# Limits

The numeric limits of Odicea: sizes, counts, lengths and time windows. Each row names the Go package whose constant defines it, so the value can be checked in code. Limits that an operator can change are in [Configuration](configuration.md).

> [!NOTE]
> Values are the defaults compiled into the release. Where a limit is configurable, the table says so and points to the variable.

## Requests and lists

| Limit | Value | Package |
| :--- | :--- | :--- |
| JSON request body | 1 MiB | `httpx` |
| Page size (`limit`) | default 20, maximum 100 (higher values are clamped) | `query` |
| List `offset` | 0 to 10 000 | `query` |
| Search text | 200 characters | `text` |
| Names and titles | 200 characters | `text` |
| Descriptions, purposes, justifications | 2000 characters | `text` |
| Email address | 254 characters | `text` |
| URL | 2048 characters | `text` |
| Server read timeout (everything except uploads) | 30 s | `server` |
| Server write timeout | 60 s | `server` |
| Server idle timeout | 120 s | `server` |
| Database session `statement_timeout` / `lock_timeout` | 30 s / 10 s (a value in `DATABASE_URL` wins) | `bootstrap` |

## Uploads and sources

| Limit | Value | Package |
| :--- | :--- | :--- |
| Compressed source archive | 100 MiB | `build` |
| Unpacked source | 500 MiB | `build` |
| Files in a source | 20 000 | `build` |
| Largest upload request (archive plus form) | 101 MiB | `httpx` |
| Time for a streamed upload to arrive | 15 min | `httpx` |
| Source display name | 200 characters | `build` |
| Upload link lifetime (single use) | 10 min | `release` |
| An expired upload link is remembered for | 24 h (to answer "expired", not "unknown") | `release` |
| Built source kept for | 7 days | `build` |
| Source that no build used kept for | 24 h | `build` |
| Compressed sources per organization | 10 GiB by default; set with `ODYSSEY_SOURCE_QUOTA_GIB` | `build` and `configuration` |
| Repository folder (`root`) of a source | 200 characters | `source` |
| GitHub repository name (`owner/name`) | 200 characters | `build` |
| Branch name | 255 characters | `build` |
| Commit title kept | 200 characters | `build` |
| Commit author kept | 100 characters | `build` |

The same size limits apply to code imported from GitHub. See [errors](errors.md#github) for `SOURCE_TOO_LARGE` and [errors](errors.md#publishing-and-uploads) for `SOURCE_QUOTA`.

## Apps and versions

| Limit | Value | Package |
| :--- | :--- | :--- |
| App name (slug) | 3 to 40 characters: lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen | `project` |
| Parts (services) per app | 8 | `project` and `detect` |
| Part name | 20 characters | `project` |
| Part folder inside the code | 200 characters | `project` |
| Part route (path) | 100 characters | `project` |
| Replicas per part | 1 to 10 | `release` |
| Image reference | 512 characters | `release` |
| Health path | 200 characters | `release` |
| Variables in a version's settings | 100 | `release` |
| Variable value | 4096 characters | `release` |
| Version message | 200 characters | `release` |
| Default listening port | 8080 | `release` |
| Parts detected from a folder listing | 8 (depth 3), from up to 20 000 paths | `detect` |

## Variables and secrets

| Limit | Value | Package |
| :--- | :--- | :--- |
| Variables, secrets and open secret requests of an environment, together | 100 | `release` |
| Secret value (before sealing) | 64 KiB | `secretbox` |
| Secret note | 200 characters | `release` |
| External secret store name | 253 characters | `release` |
| External secret key | 512 characters | `release` |
| External secret property | 256 characters | `release` |
| A secret value waits for its cluster for | 7 days, then is dropped | `fleet` |

## Resources

| Limit | Value | Package |
| :--- | :--- | :--- |
| Resources per environment (those being deleted still count) | 10 | `resource` |
| Resources one app connects to | 10 | `release` |
| Parameters in one request | 32 | `resource` |
| Resource name | 30 characters | `resourcekind` |
| Variable prefix of a resource | 40 characters | `resourcekind` |
| Volume size | 1 to 100 GiB whole numbers (a recipe may narrow it) | `resourcekind` |
| Database instances | 5 | `resourcespec` |
| Time an agent waits for a resource to be ready | 10 min | `resourcekind` |

## Builds and scans

| Limit | Value | Package |
| :--- | :--- | :--- |
| Longest a build may run | 25 min | `build` |
| Longest a cluster may be asked to wait for a build | 1 h | `fleet` |
| Lines returned by one log read | 1000 | `build` |
| Build log kept per build | 5 MiB (then "log cut") | `fleet` |
| Longest stored log line | 4 KiB | `fleet` |
| Longest wait for a vulnerability scan | 30 min | `build` |
| How long a publication waits for its scan before treating it as unknown | 10 min | `release` |
| A scan result is reused for | 7 days | `build` |
| Failed publication still needs attention for | 24 h | `inventory` |

## Rules and approvals

| Limit | Value | Package |
| :--- | :--- | :--- |
| IT rules per organization | 100 | `policy` |
| Length of a rule's expression | 2000 characters | `policy` |
| Time to evaluate one rule | 100 ms | `policycel` |
| Cost budget of one rule evaluation | 10 000 | `policycel` |
| An approval request waits for a decision | 7 days, then it expires | `approval` |
| A publication waits for its approval | 8 days | `releasesvc` |
| Note on a decision | 500 characters | `approval` |
| A rejected request stays on its owner's list | 7 days | `inventory` |
| Expired requests are checked every | 5 min | `approvalmodule` |

## Catalog

| Limit | Value | Package |
| :--- | :--- | :--- |
| Teams one offering may name | 50 | `catalog` |
| Recipe id | 48 characters | `catalog` |
| Recipe title | 80 characters | `catalog` |
| Recipe description | 500 characters | `catalog` |

## Inventory

| Limit | Value | Package |
| :--- | :--- | :--- |
| Apps one inventory read looks at (more is reported as truncated) | 500 | `inventory` |
| Entries of each source of the attention list | 100 | `inventory` |

## Clusters and egress

| Limit | Value | Package |
| :--- | :--- | :--- |
| Cluster name | 60 characters | `fleet` |
| Enrollment token lifetime | 24 h | `fleet` |
| Cluster certificate lifetime (agents renew after two thirds) | 30 days | `fleet` |
| Gateway serving certificate lifetime | 7 days | `bootstrap` |
| Egress allow-list entries | 100 | `fleet` |

## Sign-in for protected apps

| Limit | Value | Package |
| :--- | :--- | :--- |
| Sign-in ticket lifetime | 60 s | `access` |
| Time to finish a sign-in | 10 min | `access` |

## MCP tools

| Limit | Value | Package |
| :--- | :--- | :--- |
| MCP request body | 1 MiB | `mcpx` |
| Token introspection answers are cached for | 15 s | `httpx` |
| Timeout verifying a token | 5 s | `httpx` |
| `wait_seconds` of a publish tool | up to 45 s | `releasemcp` |
| Log lines per call (`get_build_logs`) | default 200, maximum 1000 | `releasemcp` |
| Versions per call | default 10, maximum 50 | `releasemcp` |
| Log pages read to find the tail of a long log | 20 | `releasemcp` |
| Pages of ready versions a rollback scans for a target | 10 | `releasemcp` |
| Pages of repositories listed from GitHub | 50 | `sourcemcp` |
| Wait for a GitHub import to settle | 10 s, polled every 1 s | `sourcemcp` |
| Paths accepted by `detect_services` | 20 000 | `detect` |

## See also

- [Configuration](configuration.md)
- [Errors](errors.md)
- [MCP tools](mcp-tools.md)
