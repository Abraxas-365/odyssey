import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api, params } from './api'
import type { AccessChange, AccessMode, BuildLogLine, BuildLogPage, CurrentRelease, Deployment, DeployRequest, DeploySourceConfig, EnvironmentList, EnvironmentSettings, List } from './types'

/** While something is being published the console polls every 2 s (plan 1a-a1). */
export const PUBLISHING_POLL_MS = 2000

const enc = encodeURIComponent
const envPath = (project: string, env: string) => `/projects/${enc(project)}/environments/${enc(env)}`

export const releaseKeys = {
  environments: (project: string) => ['projects', 'detail', project, 'environments'] as const,
  env: (project: string, env: string) => ['projects', 'detail', project, 'environments', env] as const,
  current: (project: string, env: string) => [...releaseKeys.env(project, env), 'current'] as const,
  deployments: (project: string, env: string) => [...releaseKeys.env(project, env), 'deployments'] as const,
  settings: (project: string, env: string) => [...releaseKeys.env(project, env), 'settings'] as const,
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

/** Who can open the app: revision 0 is the default (the team). */
export function useSettings(project: string, env: string | undefined) {
  return useQuery({
    queryKey: releaseKeys.settings(project, env ?? ''),
    queryFn: ({ signal }) => api<EnvironmentSettings>(`${envPath(project, env!)}/settings`, { signal }),
    enabled: !!env,
  })
}

/**
 * Changes who can open the app. When it is online a deployment applies it, so
 * everything under the environment is refreshed (the current state starts
 * polling by itself).
 */
export function useSetAccess(project: string, env: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (mode: AccessMode) => api<AccessChange>(`${envPath(project, env)}/access`, { method: 'PUT', body: { mode } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }),
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

/** Publishes a folder already packed as .tar.gz: multipart `config` first, then `source` (docs/api.md §11). */
export const useDeploySource = (project: string, env: string) =>
  useDeploymentWrite(project, env, ({ config, source, name }: { config: DeploySourceConfig; source: Blob; name: string }) => {
    const form = new FormData()
    form.append('config', new Blob([JSON.stringify(config)], { type: 'application/json' }), 'config.json')
    form.append('source', source, `${name}.tar.gz`)
    return api<Deployment>(`${envPath(project, env)}/deployments`, { method: 'POST', body: form })
  })

/** While a build runs its log is polled every second (plan 1b). */
export const BUILD_LOG_POLL_MS = 1000

/**
 * A build's whole log, read page by page: each poll asks only for the lines
 * after the last one it has, until the build has finished and the log ends.
 */
export function useBuildLog(project: string, build: string | null | undefined, enabled = true) {
  const [lines, setLines] = useState<BuildLogLine[]>([])
  const [finished, setFinished] = useState(false)
  const [error, setError] = useState<unknown>()
  const after = useRef(0)
  useEffect(() => {
    setLines([])
    setFinished(false)
    setError(undefined)
    after.current = 0
    if (!build || !enabled) return
    const ctrl = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const tick = async () => {
      try {
        for (;;) {
          const page = await api<BuildLogPage>(`/projects/${enc(project)}/builds/${enc(build)}/logs${params({ after: after.current })}`, { signal: ctrl.signal })
          if (ctrl.signal.aborted) return // another build's page, arrived late
          if (page.lines.length) setLines(prev => [...prev, ...page.lines])
          after.current = page.next
          setError(undefined)
          if (page.finished) {
            setFinished(true)
            return
          }
          if (page.lines.length < 1000) break // caught up: wait for more
        }
      } catch (e) {
        if (ctrl.signal.aborted) return
        setError(e)
        // Not allowed, gone or malformed will not change by asking again.
        if (e instanceof ApiError && e.status >= 400 && e.status < 500 && e.status !== 429) return
      }
      timer = setTimeout(tick, BUILD_LOG_POLL_MS)
    }
    void tick()
    return () => {
      ctrl.abort()
      clearTimeout(timer)
    }
  }, [project, build, enabled])
  return { lines, finished, error }
}

/** "Volver a esta": deploys again the release of `deployment`. */
export const useRollback = (project: string, env: string) =>
  useDeploymentWrite(project, env, (deployment: string) => api<Deployment>(`${envPath(project, env)}/deployments/${enc(deployment)}/rollback`, { method: 'POST' }))
