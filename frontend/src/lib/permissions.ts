// Generated permission names mirror iam/permissions.json (the backend's
// internal/permission is generated from the same file). A test keeps them in sync.
export const permissions = {
  projectsRead: 'odyssey:projects:read',
  projectsWrite: 'odyssey:projects:write',
  deploymentsRead: 'odyssey:deployments:read',
  deploymentsCreate: 'odyssey:deployments:create',
  deploymentsRollback: 'odyssey:deployments:rollback',
  clustersRead: 'odyssey:clusters:read',
  clustersManage: 'odyssey:clusters:manage',
  integrationsManage: 'odyssey:integrations:manage',
  accessPublic: 'odyssey:access:public',
  secretsExternal: 'odyssey:secrets:external',
} as const

export type Permission = (typeof permissions)[keyof typeof permissions]
