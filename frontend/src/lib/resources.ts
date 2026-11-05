import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './api'
import { releaseKeys } from './releases'
import type { CreateResource, CreateResourceResult, PendingApproval, Resource, ResourceChange, ResourceList } from './types'

/** While a resource is being prepared or erased the console asks again every 3 s. */
export const RESOURCES_POLL_MS = 3000

const enc = encodeURIComponent
const path = (project: string, env: string) => `/projects/${enc(project)}/environments/${enc(env)}/resources`

export const resourceKeys = {
  list: (project: string, env: string) => [...releaseKeys.env(project, env), 'resources'] as const,
}

/** The environment's resources (oldest first) and the kinds its cluster can make; polls while one is changing. */
export function useResources(project: string, env: string | undefined) {
  return useQuery({
    queryKey: resourceKeys.list(project, env ?? ''),
    queryFn: ({ signal }) => api<ResourceList>(path(project, env!), { signal }),
    enabled: !!env,
    refetchInterval: q => (q.state.data?.items.some(r => r.status === 'deleting' || r.phase === 'provisioning') ? RESOURCES_POLL_MS : false),
  })
}

/** Connecting changes the settings and may start a deployment, so everything under the environment is refreshed. */
function useResourceWrite<V, R = ResourceChange | Resource | PendingApproval>(project: string, env: string, fn: (v: V) => Promise<R>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => Promise.all([qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }), qc.invalidateQueries({ queryKey: ['approvals'] })]),
  })
}

export const useCreateResource = (project: string, env: string) =>
  useResourceWrite(project, env, (body: CreateResource) => api<CreateResourceResult>(path(project, env), { method: 'POST', body }))

export const useResizeResource = (project: string, env: string) =>
  useResourceWrite(project, env, ({ name, storage_gi }: { name: string; storage_gi: number }) =>
    api<Resource>(`${path(project, env)}/${enc(name)}`, { method: 'PATCH', body: { storage_gi } }))

export const useDeleteResource = (project: string, env: string) =>
  useResourceWrite(project, env, (name: string) => api<Resource | PendingApproval>(`${path(project, env)}/${enc(name)}`, { method: 'DELETE' }))

export const useConnectResource = (project: string, env: string) =>
  useResourceWrite(project, env, ({ name, prefix }: { name: string; prefix?: string }) =>
    api<ResourceChange | PendingApproval>(`${path(project, env)}/${enc(name)}/connection`, { method: 'PUT', body: prefix ? { prefix } : {} }))

export const useDisconnectResource = (project: string, env: string) =>
  useResourceWrite(project, env, (name: string) => api<ResourceChange>(`${path(project, env)}/${enc(name)}/connection`, { method: 'DELETE' }))
