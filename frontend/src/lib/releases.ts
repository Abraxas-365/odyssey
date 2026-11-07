import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api, params } from './api'
import { fromBase64, sealSecret } from './secretbox'
import type {
  AccessChange,
  AccessMode,
  Build,
  BuildLogLine,
  BuildLogPage,
  CurrentRelease,
  Deployment,
  DeployRequest,
  DeploySourceConfig,
  EnvironmentList,
  EnvironmentSettings,
  List,
  PendingApproval,
  PutEnvironmentSecret,
  SecretView,
  SetEnvironmentVariables,
  SettingsChange,
} from './types'

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
    // 202 {status: "pending_approval"} when IT's rules hold it (lib/approvals `isPending`)
    mutationFn: (mode: AccessMode) => api<AccessChange | PendingApproval>(`${envPath(project, env)}/access`, { method: 'PUT', body: { mode } }),
    onSuccess: () => Promise.all([qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }), qc.invalidateQueries({ queryKey: ['approvals'] })]),
  })
}

/** Sets and removes plain variables in one revision (a config deployment applies it when the app is online). */
export function useSetEnv(project: string, env: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SetEnvironmentVariables) => api<SettingsChange>(`${envPath(project, env)}/env`, { method: 'PATCH', body }),
    onSuccess: () => qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }),
  })
}

/**
 * Declares a secret: `managed` is a request a person answers by loading the
 * value (200, nothing applied yet); `external` points at the vault (202, a
 * revision applied like any change).
 */
export function usePutSecret(project: string, env: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, body }: { name: string; body: PutEnvironmentSecret }) =>
      api<SecretView | SettingsChange>(`${envPath(project, env)}/secrets/${enc(name)}`, { method: 'PUT', body }),
    onSuccess: () => qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }),
  })
}

/**
 * Changes who reads a secret without writing its value again (MS-4: PUT
 * …/secrets/{name}/services). Empty = the whole app. IT's rules may hold it
 * (202 `pending_approval`).
 */
export function useScopeSecret(project: string, env: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, services }: { name: string; services: string[] }) =>
      api<SettingsChange | PendingApproval>(`${envPath(project, env)}/secrets/${enc(name)}/services`, { method: 'PUT', body: { services } }),
    onSuccess: () => Promise.all([qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }), qc.invalidateQueries({ queryKey: ['approvals'] })]),
  })
}

export function useRemoveSecret(project: string, env: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => api<SettingsChange>(`${envPath(project, env)}/secrets/${enc(name)}`, { method: 'DELETE' }),
    onSuccess: () => qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }),
  })
}

/**
 * Loads a managed secret's value. The value is sealed here, in the browser,
 * to the cluster's key (lib/secretbox): the API only ever receives the
 * ciphertext. The key and the version come from settings read right now, not
 * from the page, so a key that changed or a value someone else loaded meanwhile
 * fails closed (SECRET_KEY_CHANGED / 409) instead of being overwritten.
 */
export function useLoadSecret(project: string, env: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, value }: { name: string; value: string }) => loadSecretValue(project, env, name, value),
    // the variables hold the plaintext: drop the mutation as soon as nobody
    // observes it (the dialog also resets it when it closes)
    gcTime: 0,
    onSettled: () => qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }),
  })
}

/** useLoadSecret's work, on its own so a test proves only ciphertext leaves. */
export async function loadSecretValue(project: string, env: string, name: string, value: string): Promise<SettingsChange> {
  const settings = await api<EnvironmentSettings>(`${envPath(project, env)}/settings`)
  const key = settings.secret_key
  if (!key) throw new ApiError('El clúster de esta app todavía no puede guardar secretos. Pídele a TI que lo habilite.', 422, 'BUSINESS', 'SECRETS_UNAVAILABLE')
  // the API's next_version counts past a removed one (the cluster takes only newer versions);
  // a requested secret is in settings.secrets too, so a missing name was removed meanwhile
  const known = settings.secrets.find(s => s.name === name)
  if (!known) throw new ApiError('Este secreto ya no existe. Recarga la página.', 404, 'NOT_FOUND', 'NOT_FOUND')
  const version = known.next_version
  const sealed = await sealSecret(fromBase64(key.public_key), { cluster: key.cluster_id, environment: env, app: key.app, name, version }, value)
  return api<SettingsChange>(`${envPath(project, env)}/secrets/${enc(name)}/value`, { method: 'PUT', body: { version, key_id: key.key_id, sealed } })
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
 * A build, for its scan: the scan lands after the build and never blocks it,
 * so while it is missing (null) the console asks again, a few times, then stops.
 */
export const SCAN_POLL_MS = 10_000
export function useBuild(project: string, build: string | null | undefined) {
  return useQuery({
    queryKey: ['projects', 'detail', project, 'builds', build ?? ''] as const,
    queryFn: ({ signal }) => api<Build>(`/projects/${enc(project)}/builds/${enc(build!)}`, { signal }),
    enabled: !!build,
    refetchInterval: q => (q.state.data && q.state.data.status === 'succeeded' && !q.state.data.scan && q.state.dataUpdateCount < 30 ? SCAN_POLL_MS : false),
  })
}

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
