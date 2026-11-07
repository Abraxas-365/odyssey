import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { api, params } from './api'
import { PUBLISHING_POLL_MS, releaseKeys } from './releases'
import type { DetectedServices, PendingApproval, ServiceList, ServiceStateList, SetServices } from './types'

const enc = encodeURIComponent
const path = (project: string) => `/projects/${enc(project)}/services`

export const serviceKeys = {
  list: (project: string) => ['projects', 'detail', project, 'services'] as const,
  states: (project: string, env: string) => [...releaseKeys.env(project, env), 'service-states'] as const,
}

/**
 * How each part of what the environment serves stands in its cluster. Only
 * asked for an app of several parts (`enabled`); it polls while one of them
 * is starting (an unknown phase may never change: it is not polled).
 */
export function useServiceStates(project: string, env: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: serviceKeys.states(project, env ?? ''),
    queryFn: ({ signal }) => api<ServiceStateList>(`/projects/${enc(project)}/environments/${enc(env!)}/services`, { signal }),
    enabled: enabled && !!env,
    refetchInterval: q => (q.state.data?.items.some(s => s.phase === 'progressing') ? PUBLISHING_POLL_MS * 2 : false),
  })
}

/** The app's services, in order (every app has at least one). */
export function useServices(project: string | undefined) {
  return useQuery({
    queryKey: serviceKeys.list(project ?? ''),
    queryFn: ({ signal }) => api<ServiceList>(path(project!), { signal }),
    enabled: !!project,
    staleTime: 60_000,
  })
}

/**
 * Replaces the whole list. IT's rules may hold it (202 `pending_approval`):
 * then nothing changed yet and the list stays as it was.
 */
export function useSetServices(project: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SetServices) => api<ServiceList | PendingApproval>(path(project), { method: 'PUT', body }),
    onSuccess: out => {
      if ('items' in out) qc.setQueryData(serviceKeys.list(project), out)
      void qc.invalidateQueries({ queryKey: ['approvals'] })
    },
  })
}

/** Proposes services from a folder's file names (never contents); writes nothing. */
export function useDetectFolder() {
  return useMutation({
    mutationFn: (body: { paths: string[]; root?: string }) => api<DetectedServices>('/services/detect', { method: 'POST', body }),
  })
}

/** At most this many paths go to `POST /services/detect` (the API's limit). */
export const MAX_DETECT_PATHS = 20_000

/** Proposes services from a GitHub branch's file names; writes nothing. */
export function useDetectRepository() {
  return useMutation({
    mutationFn: ({ installation, repository, branch, root }: { installation: string; repository: number; branch?: string; root?: string }) =>
      api<DetectedServices>(`/github/installations/${enc(installation)}/repositories/${repository}/services${params({ branch, root })}`),
  })
}

/**
 * The same proposal as a query, for a repository being picked (create or
 * connect): it follows the repository, branch and folder as they change.
 */
export function useRepositoryProposal(pick: { installation: string; repository: number; branch: string; root: string } | undefined) {
  return useQuery({
    queryKey: ['github', 'proposal', pick?.installation, pick?.repository, pick?.branch, pick?.root] as const,
    queryFn: ({ signal }) =>
      api<DetectedServices>(`/github/installations/${enc(pick!.installation)}/repositories/${pick!.repository}/services${params({ branch: pick!.branch || undefined, root: pick!.root || undefined })}`, { signal }),
    enabled: !!pick,
    staleTime: 60_000,
    retry: false,
  })
}

/**
 * Saves a list right before the first publication (create/connect/upload),
 * outside a hook because the app may have been created a moment ago. A 202
 * means IT holds it: nothing changed yet.
 */
export async function saveServices(qc: QueryClient, project: string, body: SetServices): Promise<ServiceList | PendingApproval> {
  const out = await api<ServiceList | PendingApproval>(path(project), { method: 'PUT', body })
  if ('items' in out) qc.setQueryData(serviceKeys.list(project), out)
  void qc.invalidateQueries({ queryKey: ['approvals'] })
  return out
}
