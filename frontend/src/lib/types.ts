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
export type DeployRequest = Schemas['DeployRequest']

export type AuditEntry = Schemas['AuditEntry']
export type AuditEntryList = Schemas['AuditEntryList']
export type AuditVerification = Schemas['AuditVerification']
export type AuditQuery = NonNullable<operations['listAuditEntries']['parameters']['query']>

export type PublicConfig = Schemas['PublicConfig']
