// Wire types for /api/v1, generated from api/openapi.yaml (`make api-types`
// → openapi.gen.ts; `npm run check` fails while it is stale). snake_case on
// purpose: these are the exact JSON shapes; don't remap them. Add aliases
// here, never hand-written shapes.
import type { components, operations } from './openapi.gen'

type Schemas = components['schemas']

export type Page = Schemas['Page']
export interface List<T> { items: T[]; page: Page }

export type ErrorBody = Schemas['ErrorBody']
export type ApiErrorType = Schemas['ErrorType']

/** docs/api.md §11. The UI calls a project an "app". */
export type Project = Schemas['Project']
export type ProjectStatus = Schemas['ProjectStatus']
export type ProjectCreate = Schemas['ProjectCreate']
export type ProjectPatch = Schemas['ProjectPatch']
/** How much the app matters (IT's rules ask for it); only projects:tier changes it. */
export type ProjectTier = Schemas['ProjectTier']
export type ProjectList = Schemas['ProjectList']

/** Query of GET /projects (the console filters one status at a time). */
type ListProjectsQuery = NonNullable<operations['listProjects']['parameters']['query']>
export type ProjectFilter = Omit<ListProjectsQuery, 'status'> & { status?: ProjectStatus }

/** An app's place to run; every app is born with `production`. */
export type Environment = Schemas['Environment']
export type EnvironmentList = Schemas['EnvironmentList']

/** A release is an image + config; a deployment, one attempt to put it online (UI: "versión"). */
export type Release = Schemas['Release']
export type Deployment = Schemas['Deployment']
export type DeploymentStatus = Schemas['DeploymentStatus']
export type DeploymentStep = Schemas['DeploymentStep']
export type CurrentRelease = Schemas['CurrentRelease']
/** Who can open an app (cut 1e): the environment's settings and the answer to changing them. */
export type AccessMode = Schemas['AccessMode']
export type EnvironmentSettings = Schemas['EnvironmentSettings']
export type AccessChange = Schemas['AccessChange']
/** Variables and secrets (cut 2a): a change is a settings revision; a secret is only ever a reference. */
export type SettingsChange = Schemas['SettingsChange']

/** "Lo que usa esta app" (cut 2e): what the app depends on, and the closed list of what needs attention. */
export type EnvironmentUses = Schemas['EnvironmentUses']
export type UsesAttention = EnvironmentUses['attention'][number]
export type UsesAttentionKind = UsesAttention['kind']

/** Databases, caches and files of an environment (docs/api.md §11, docs/resources.md). */
export type Resource = Schemas['Resource']
export type ResourceKind = Schemas['ResourceKind']
export type ResourceChange = Schemas['ResourceChange']
export type ResourceList = operations['listResources']['responses'][200]['content']['application/json']
export type CreateResource = operations['createResource']['requestBody']['content']['application/json']
/** The app's own access to the organization's AI (docs/api.md §11, docs/ai.md); never carries the key. */
export type AppAI = Schemas['AI']
export type SecretView = Schemas['SecretView']
export type ClusterSecretKey = Schemas['ClusterSecretKey']
export type SetEnvironmentVariables = NonNullable<operations['setEnvironmentVariables']['requestBody']>['content']['application/json']
export type PutEnvironmentSecret = NonNullable<operations['putEnvironmentSecret']['requestBody']>['content']['application/json']
export type DeployRequest = Schemas['DeployRequest']
export type DeploySourceConfig = Schemas['DeploySourceConfig']
/** The image built from an uploaded folder, and its log. */
export type Build = Schemas['Build']
export type BuildLogPage = Schemas['BuildLogPage']
export type BuildLogLine = BuildLogPage['lines'][number]

/** The organization's Kubernetes clusters (TI: Infraestructura → Clústeres). */
export type Cluster = Schemas['Cluster']
export type ClusterStatus = Schemas['ClusterStatus']
export type ClusterCreate = Schemas['ClusterCreate']
export type ClusterCreated = Schemas['ClusterCreated']
export type EnrollmentToken = Schemas['EnrollmentToken']
export type ClusterList = Schemas['ClusterList']
export type ClusterMoved = Schemas['ClusterMoved']
export type ClusterEgress = Schemas['ClusterEgress']
export type ClusterEgressSet = Schemas['ClusterEgressSet']

export type AuditEntry = Schemas['AuditEntry']
export type AuditEntryList = Schemas['AuditEntryList']
export type AuditVerification = Schemas['AuditVerification']
export type AuditQuery = NonNullable<operations['listAuditEntries']['parameters']['query']>

export type PublicConfig = Schemas['PublicConfig']

/** "Conecta tu asistente": the MCP address, the downloadable skill, and each assistant's steps. */
export type Assistant = Schemas['Assistant']
export type AssistantClient = Schemas['AssistantClient']

/** GitHub: the App's installations on the organization's accounts, and each app's repository. */
export type GitHubInfo = Schemas['GitHubInfo']
export type GitHubInstallation = Schemas['GitHubInstallation']
export type GitHubInstallationClaimed = Schemas['GitHubInstallationClaimed']
export type GitHubRepository = Schemas['GitHubRepository']
export type GitHubBranch = Schemas['GitHubBranch']
export type RepositoryConnection = Schemas['RepositoryConnection']
export type ConnectRepository = Schemas['ConnectRepository']
export type GitHubPush = Schemas['GitHubPush']
export type PushStatus = Schemas['PushStatus']
export type GitRef = Schemas['GitRef']

/** IT's rules (cut 3a, docs/policy.md): what a rule does, and the dry-run's question and answer. */
export type PolicyRule = Schemas['PolicyRule']
export type PolicyDraft = Schemas['PolicyRuleDraft']
export type PolicyEffect = Schemas['PolicyEffect']
export type PolicyAction = Schemas['PolicyAction']
export type PolicyInput = Schemas['PolicyInput']
export type PolicyEvaluation = Schemas['PolicyEvaluation']
