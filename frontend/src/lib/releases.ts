import { useEffect, useRef } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './api'
import type { CurrentRelease, Deployment, DeployRequest, EnvironmentList, List } from './types'

/** While something is being published the console polls every 2 s (plan 1a-a1). */
export const PUBLISHING_POLL_MS = 2000

const enc = encodeURIComponent
const envPath = (project: string, env: string) => `/projects/${enc(project)}/environments/${enc(env)}`

export const releaseKeys = {
  environments: (project: string) => ['projects', 'detail', project, 'environments'] as const,
  env: (project: string, env: string) => ['projects', 'detail', project, 'environments', env] as const,
  current: (project: string, env: string) => [...releaseKeys.env(project, env), 'current'] as const,
  deployments: (project: string, env: string) => [...releaseKeys.env(project, env), 'deployments'] as const,
}

export function useEnvironments(project: string) {
  return useQuery({
    queryKey: releaseKeys.environments(project),
    queryFn: ({ signal }) => api<EnvironmentList>(`/projects/${enc(project)}/environments`, { signal }),
    staleTime: 5 * 60_000, // environments change rarely
  })
}

/** What the environment serves; polls while a deployment is in progress. */
export function useCurrent(project: string, env: string | undefined) {
  return useQuery({
    queryKey: releaseKeys.current(project, env ?? ''),
    queryFn: ({ signal }) => api<CurrentRelease>(`${envPath(project, env!)}/current`, { signal }),
    enabled: !!env,
    refetchInterval: q => (q.state.data?.active ? PUBLISHING_POLL_MS : false),
  })
}

/**
 * The environment's history, newest first. It polls along with the current
 * state and refreshes once more when a deployment settles, so the final
 * status (and an automatic restore) shows without a reload.
 */
export function useDeployments(project: string, env: string | undefined, publishing: boolean) {
  const qc = useQueryClient()
  const was = useRef(publishing)
  useEffect(() => {
    if (was.current && !publishing && env) void qc.invalidateQueries({ queryKey: releaseKeys.deployments(project, env) })
    was.current = publishing
  }, [publishing, project, env, qc])
  return useQuery({
    queryKey: releaseKeys.deployments(project, env ?? ''),
    queryFn: ({ signal }) => api<List<Deployment>>(`${envPath(project, env!)}/deployments?limit=20`, { signal }),
    enabled: !!env,
    refetchInterval: publishing ? PUBLISHING_POLL_MS : false,
  })
}

function useDeploymentWrite<V>(project: string, env: string, fn: (v: V) => Promise<Deployment>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }),
  })
}

export const useDeploy = (project: string, env: string) =>
  useDeploymentWrite(project, env, (body: DeployRequest) => api<Deployment>(`${envPath(project, env)}/deployments`, { method: 'POST', body }))

/** "Volver a esta": deploys again the release of `deployment`. */
export const useRollback = (project: string, env: string) =>
  useDeploymentWrite(project, env, (deployment: string) => api<Deployment>(`${envPath(project, env)}/deployments/${enc(deployment)}/rollback`, { method: 'POST' }))
