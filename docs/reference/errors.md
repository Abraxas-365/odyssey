# Errors

Every error the API returns has the same body, a closed set of `type`s and a `code` that can be more specific. This page lists the shape, the types and the codes clients see, with what each means and what to do about it.

## Error body

Every failure, including the web framework's own 404, 405 and 413, answers with this shape:

```json
{
  "error": {
    "type": "VALIDATION",
    "code": "VALIDATION",
    "message": "slug must be 3-40 characters: …",
    "status_code": 400,
    "details": { "field": "slug" },
    "request_id": "6f1c…"
  }
}
```

| Field | Meaning |
| :--- | :--- |
| `type` | The kind of error, from the closed set [below](#types-and-statuses). Branch on this. |
| `code` | Equal to `type` unless a more specific code applies. New codes are added over time; handle an unknown `code` by its `type`. |
| `message` | For people. Many are in Spanish. Never parse it. |
| `status_code` | The HTTP status, repeated for clients that only read the body. |
| `details` | Optional. `field` names the request field a validation error is about (a JSON path such as `items[2].url`). Some codes add more, listed below. |
| `request_id` | The response's `X-Request-ID`. Give it to IT when a `5xx` persists. |

> [!NOTE]
> For `5xx` the message is always `an unexpected error occurred` and `details` is dropped. The cause is only in the server log next to the `request_id`.

Assistants on MCP get a different shape, described in [MCP tools](mcp-tools.md#errors): `code`, `message`, `field`, `next_step` and `request_id`, with the same codes.

## Types and statuses

One `type` maps to one status, with a few `VALIDATION` and `AUTHORIZATION` codes that carry their own.

| Status | `type` | `code` | Meaning |
| :--- | :--- | :--- | :--- |
| 400 | `VALIDATION` | `VALIDATION` | Malformed body, unknown field, bad enum, id or query parameter. `details.field` names the field. |
| 401 | `AUTHORIZATION` | `UNAUTHORIZED` | No valid token. Sign in again. |
| 403 | `AUTHORIZATION` | `FORBIDDEN` | Valid token, missing permission. See [Permissions](permissions.md). |
| 404 | `NOT_FOUND` | `NOT_FOUND` | The id does not exist, belongs to another organization, or the route is unknown. |
| 405 | `VALIDATION` | `METHOD_NOT_ALLOWED` | The route does not accept that method. |
| 409 | `CONFLICT` | `CONFLICT` | A uniqueness conflict, a serialization failure (retry), or one of the domain conflicts below. |
| 413 | `VALIDATION` | `PAYLOAD_TOO_LARGE` | The body is over the route's limit. See [Limits](limits.md). |
| 415 | `VALIDATION` | `UNSUPPORTED_MEDIA_TYPE` | The body is not JSON, or is compressed. |
| 422 | `BUSINESS` | `BUSINESS` | Valid input that breaks a rule. Usually has a specific code. |
| 500 | `INTERNAL` | `INTERNAL` | Something unexpected. Retry; if it persists, report the `request_id`. |
| 502, 503 | `EXTERNAL` | `EXTERNAL` | A service Odicea depends on did not answer. Retry shortly. |

Clients should branch on `code` first and fall back to `type`.

## Actions held or refused by IT's rules

A rule that asks for IT's approval is **not an error**: the action answers `202` with `{"status":"pending_approval","request_id":"…","message":"…"}` and nothing has happened yet. See [Approvals](../guides/it/approvals.md). Over MCP the same outcome is a normal result with `status: "pending_approval"`.

| Code | Status | Meaning | What to do |
| :--- | :---: | :--- | :--- |
| `POLICY_DENIED` | 403 | A rule forbids the action. `details.rules` lists the rule ids. | Do not retry. Talk to IT, who can change the rule. |
| `POLICY_INVALID` | 422 | A rule's expression does not compile. `details.field` is `expression`; the message carries the compiler's reason. | Fix the expression. |
| `POLICY_BUILTIN` | 422 | A built-in rule can only be switched off, not edited or deleted. | Switch it off and write your own rule. |
| `POLICY_TOO_MANY` | 422 | The organization reached the maximum number of rules. | Delete a rule first. See [Limits](limits.md#rules-and-approvals). |
| `APPROVAL_SELF` | 403 | Nobody decides their own request. | Ask another person with the approval permission. |
| `APPROVAL_DECIDED` | 409 | The request is no longer pending. `details.status` says what it became. | Reload the request. |
| `APPROVAL_NOT_USABLE` | 409 | The approval does not cover this action, or was already used. | Ask again. |
| `APPROVAL_OUTDATED` | 422 | An approval from before the resource catalog no longer describes what would be created. | Ask again. |
| `CONFIRMATION_REQUIRED` | 409 | MCP only. The person must say yes first (a production rollback, or making an app public). | Ask the person, then repeat with `confirm: true`. See [MCP tools](mcp-tools.md#confirmation). |
| `CONFIRMATION_DECLINED` | 409 | MCP only. The person said no. | Do nothing. |
| `APPROVAL_REQUIRED` | 409 | A legacy refusal that the API no longer sends for governed actions. MCP still words a next step for it. | Show the message; IT can authorize the action. |

## Apps

| Code | Status | Meaning | What to do |
| :--- | :---: | :--- | :--- |
| `PROJECT_ARCHIVED` | 422 | The app is archived and takes no changes or publications. | Unarchive it. |
| `PROJECT_TIER_RESOURCES` | 409 | Moving to that tier would strand resources: the catalog does not offer their recipe there. `details.resources` lists them. | Offer the recipe in that tier in the catalog first. |
| `SERVICES_UNAVAILABLE` | 422 | The app's cluster cannot run several services yet. | Ask IT to update the cluster's agent. |
| `SERVICE_SETTINGS_UNAVAILABLE` | 422 | The cluster cannot give a setting to only some services. | Apply it to the whole app, or ask IT to update the agent. |
| `IMAGE_NEEDS_ONE_SERVICE` | 422 | An app with several services cannot be published from one image. | Publish its code so each service is built. |
| `VARIABLE_TAKEN` | 409 | Another resource or setting already uses that variable name. `details.name` has it. | Choose another name or a different prefix. |

## Publishing and uploads

| Code | Status | Meaning | What to do |
| :--- | :---: | :--- | :--- |
| `UPLOAD_TICKET_USED` | 409 | The single-use upload link was already used. `details.deployment_id` is the publication it started. | Ask for a new link. |
| `UPLOAD_TICKET_EXPIRED` | 422 | The upload link's 10 minutes ran out. | Ask for a new link. |
| `SOURCE_INVALID` | 400 or 413 | The uploaded folder is not a usable source (`details.field` is `source`) or it is over the size limits. | Upload the folder again; see [Limits](limits.md#uploads-and-sources). |
| `SOURCE_QUOTA` | 422 | The organization's uploaded sources fill its quota and none can be dropped. | Wait for older sources to expire or ask IT to raise `ODYSSEY_SOURCE_QUOTA_GIB`. |
| `SOURCE_EXPIRED` | 422 | The uploaded source was removed before it was built. | Publish again. |
| `ALREADY_CURRENT` | 422 | A rollback to the version that is already online. | Choose another version. |
| `NOT_ROLLBACKABLE` | 422 | A rollback to a version that never went live. | Choose a version that was online. |

### Why a publication failed

When a version fails, the version carries an `error` with a `code`, a plain-language `message` and sometimes a technical `detail`. These codes appear there rather than as a response to a request.

| Code | Meaning |
| :--- | :--- |
| `UNSUPPORTED_APP` | The builder could not tell what kind of app it is or how to start it. Check the folder has the whole code (for example a `package.json` with a `start` script, `requirements.txt` or `go.mod`). |
| `BUILD_FAILED` | A build step failed. The technical detail shows which. |
| `BUILD_TIMEOUT` | The build took too long and was stopped. See [Limits](limits.md#builds-and-scans). |
| `PUSH_FAILED` | The app was built but the image could not be stored. Ask IT. |
| `NO_BUILDER` | The app's cluster is not set up to build. Ask IT to configure a registry. |
| `NO_CLUSTER` | There is no cluster to build or run in. Ask IT to connect one. |
| `SOURCE_ROOT_MISSING` | A service's folder is not in the code. Check each service's folder. |
| `BUILD_INTERNAL` | Something failed on Odicea's side while building. Try again. |
| `APPLY_FAILED` | The cluster could not apply the app's configuration. |
| `IMAGE_PULL_FAILED` | The cluster could not download the image. Check it exists and the cluster can reach its registry. |
| `APP_CRASHED` | The app exits when it starts. |
| `HEALTH_CHECK_FAILED` | The app started but does not answer on its health path. |
| `NOT_READY` | The app did not become ready in time. |
| `RESOURCE_NOT_READY` | A database, cache or volume of the app was not ready in time. |
| `PROMOTE_FAILED` | The app ran but recording it as the current version failed. |
| `POLICY_DENIED` | A rule of IT refused the publication. |

## GitHub

| Code | Status | Meaning | What to do |
| :--- | :---: | :--- | :--- |
| `GITHUB_NOT_CONFIGURED` | 422 | This installation has no GitHub app. | Ask IT to set it up. See [GitHub app](../guides/it/github-app.md). |
| `GITHUB_NOT_CONNECTED` | 404 | MCP only. No GitHub account is connected to the organization. | Connect one in the console (needs a browser). |
| `GITHUB_STATE_INVALID` | 422 | The setup link expired, or is forged or not the user's. | Start the connection again from Odicea. |
| `GITHUB_NOT_YOURS` | 401 | The GitHub user does not administer that installation. | Have the account or organization owner install it. |
| `GITHUB_INSTALLATION_TAKEN` | 409 | Another Odicea organization already linked that GitHub account. | Use a different account. |
| `GITHUB_NO_ACCESS` | 404 | The installation no longer reaches the repository. | Ask IT to check the installation. |
| `GITHUB_BRANCH_MISSING` | 404 | The connected branch no longer exists. | Reconnect to an existing branch. |
| `GITHUB_UNAVAILABLE` | 502 | GitHub did not answer. Odicea retries. | Publish again later. |
| `SOURCE_TOO_LARGE` | 422 | The repository is over the size limits. | See [Limits](limits.md#uploads-and-sources). |
| `SOURCE_IMPORT_FAILED` | 422 or 502 | Downloading or repacking the code failed. | Try again. |
| `DEPLOY_REFUSED` | 422 | The publication could not start. The detail says why. | Read the detail, fix and publish again. |

## Secrets, resources and AI

| Code | Status | Meaning | What to do |
| :--- | :---: | :--- | :--- |
| `SECRETS_UNAVAILABLE` | 422 | The cluster cannot store secrets yet. | Ask IT to update the cluster's agent. |
| `EXTERNAL_SECRETS_UNAVAILABLE` | 422 | The cluster has no external secrets manager. | Ask IT to install it. |
| `SECRET_STORE_UNKNOWN` | 422 | The cluster does not have the named secret store. `details.name` is the secret. | Ask IT to check the store. |
| `SECRET_KEY_CHANGED` | 422 | The cluster's key changed. | Load the secret's value again. |
| `SECRET_VERSION_CONFLICT` | 409 | A newer value of the secret is on its way. | Reload and retry. |
| `RESOURCES_UNAVAILABLE` | 422 | The cluster cannot provision that kind at all. `details.kind` has it. | Ask IT to update the cluster's agent. |
| `RESOURCES_UNSUPPORTED` | 422 | The cluster lacks what that kind needs. `details.kind` and `details.missing` say what. | Ask IT to enable it. |
| `RESOURCE_LIMIT` | 409 | An environment can have up to 10 resources. | Delete one first. See [Limits](limits.md#resources). |
| `RESOURCE_IN_USE` | 409 | The resource is connected to the app. `details.name` has it. | Disconnect it first. |
| `RESOURCE_DELETING` | 409 | The resource is being deleted. | Wait, or choose another. |
| `RESOURCE_CHANGED` | 409 | The resource changed while the request was reviewed. | Try again. |
| `AI_UNAVAILABLE` | 422 | The cluster cannot give apps AI yet, or the organization has not connected AI. | Ask IT. |
| `AI_NOT_ENABLED` | 409 | The app's AI is not on. | Enable it first. |
| `AI_BUSY` | 409 | Another AI change for the app is in progress. | Wait for it to finish. |

## Catalog

| Code | Status | Meaning | What to do |
| :--- | :---: | :--- | :--- |
| `CATALOG_UNAVAILABLE` | 422 | Nothing fulfils that kind in the app's tier, or it is offered only to other teams. | Ask IT. |
| `CATALOG_OUT_OF_LIMITS` | 422 | A parameter is outside the offering's hard limits. `details.field` is `params.<name>`. | Choose a value inside the stated range. |
| `CATALOG_CHOOSE_RECIPE` | 422 | The kind has several recipes in the tier and no default. `details.recipes` lists them. | Name one with `recipe`. |
| `CATALOG_DEFAULT_TAKEN` | 409 | Another recipe of the kind is already the default in that tier. | Remove the default from the other first. |
| `CATALOG_RECIPE_CHANGED` | 422 | The recipe is no longer offered as it was when asked. | Ask again. |
| `CATALOG_TEAMS_UNAVAILABLE` | 422 | An offering names teams but Odicea cannot read the company's groups. | Connect Odicea to the IAMKit directory. |
| `CATALOG_STORE_UNKNOWN` | 422 | No cluster reads from the named secret store. `details.store` has it. | Check the name, or configure the store in the cluster. |

## Clusters and fleet

| Code | Status | Meaning | What to do |
| :--- | :---: | :--- | :--- |
| `NO_CLUSTER` | 422 | The organization has no connected cluster. | Connect one. See [Clusters](../guides/it/clusters.md). |
| `FLEET_DISABLED` | 422 | This installation cannot connect clusters: the gateway is not configured. | See [Configuration](configuration.md#fleet-ca-and-gateway). |
| `CLUSTER_REVOKED` | 422 | The cluster was disconnected for good. | Register a new one. |
| `CLUSTER_NOT_REVOKED` | 422 | Apps only move from a revoked cluster. | Revoke it first. |
| `INVALID_MOVE_TARGET` | 422 | Apps can only move to another connected cluster of the organization. | Choose an active cluster. |
| `PLACEMENT_MOVED` | 409 | The environment's cluster changed while the request ran. | Retry. |
| `ACCESS_UNAVAILABLE` | 422 | The cluster cannot protect apps with sign-in. | Ask IT to enable it. |
| `INVALID_ENROLLMENT_TOKEN` | 401 | The connection token is invalid or expired. | Generate a new one in the console. |
| `CLUSTER_CERTIFICATE_REJECTED` | 403 | The cluster's certificate is no longer valid. | Reconnect the cluster from the console. |
| `CERTIFICATE_RENEWAL_TOO_EARLY` | 422 | The certificate is too young to renew. | Retry later. |
| `NOTHING_TO_RECOVER` | 404 | No current certificate exists for that key. | Enroll the cluster again. |
| `TUNNEL_SUPERSEDED` | 409 | Another connection of the same cluster took over. | None; the newer one stays. |

## Sign-in for apps

| Code | Status | Meaning | What to do |
| :--- | :---: | :--- | :--- |
| `ACCESS_NOT_CONFIGURED` | 404 | The access sign-in is not configured on this installation. | See [Configuration](configuration.md#access-sign-in-protected-apps). |
| `ACCESS_FLOW_INVALID` | 400 | The sign-in expired or is not valid. | Open the app again. |
| `ACCESS_BAD_RETURN` | 400 | The address to return to is not allowed. `details.field` is `return`. | Open the app from its own link. |
| `ACCESS_BAD_MODE` | 400 | The app is not protected with that mode. | Open the app from its own link. |

## Internal

| Code | Status | Meaning |
| :--- | :---: | :--- |
| `WORKFLOW_FINISHED` | 409 | A background run already finished when something tried to signal it. The platform handles it; clients rarely see it. |

## See also

- [MCP tools](mcp-tools.md)
- [Permissions](permissions.md)
- [Limits](limits.md)
- [Approvals](../guides/it/approvals.md)
