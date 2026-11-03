import { useEffect, useRef } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api, params } from './api'
import { PUBLISHING_POLL_MS, releaseKeys } from './releases'
import type {
  ConnectRepository,
  GitHubBranch,
  GitHubInfo,
  GitHubInstallation,
  GitHubInstallationClaimed,
  GitHubPush,
  GitHubRepository,
  List,
  PushStatus,
  RepositoryConnection,
} from './types'

const enc = encodeURIComponent
const repoPath = (project: string) => `/projects/${enc(project)}/repository`

export const githubKeys = {
  all: ['github'] as const,
  info: ['github', 'info'] as const,
  installations: ['github', 'installations'] as const,
  repositories: (search: string, limit: number) => ['github', 'repositories', search, limit] as const,
  branches: (installation: string, repository: number) => ['github', 'branches', installation, repository] as const,
  repository: (project: string) => ['projects', 'detail', project, 'repository'] as const,
  pushes: (project: string) => ['projects', 'detail', project, 'repository', 'pushes'] as const,
}

/** Is GitHub set up on this Odicea at all? Everyone with projects:read can ask. */
export function useGitHubInfo() {
  return useQuery({
    queryKey: githubKeys.info,
    queryFn: ({ signal }) => api<GitHubInfo>('/github', { signal }),
    staleTime: 5 * 60_000,
  })
}

/** The GitHub accounts linked to the organization. */
export function useInstallations(enabled = true) {
  return useQuery({
    queryKey: githubKeys.installations,
    queryFn: ({ signal }) => api<{ items: GitHubInstallation[] }>('/github/installations', { signal }),
    enabled,
  })
}

/** Asks for the GitHub URL where the App is installed (the caller sends the browser there). */
export function useInstallURL() {
  return useMutation({
    mutationFn: (returnTo?: string) => api<{ url: string }>(`/github/install${params({ return_to: returnTo })}`),
  })
}

/** Second step of linking an account: redeems the `github_claim` GitHub brought back. */
export function useClaimInstallation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (claim: string) => api<GitHubInstallationClaimed>('/github/installations', { method: 'POST', body: { claim } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: githubKeys.installations }),
  })
}

export function useUnlinkInstallation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api<void>(`/github/installations/${enc(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: githubKeys.installations })
      void qc.invalidateQueries({ queryKey: ['github', 'repositories'] })
    },
  })
}

/** Repositories the linked accounts reach, filtered by name. `limit` grows with "Mostrar más". */
export function useRepositories(search: string, limit: number, enabled = true) {
  return useQuery({
    queryKey: githubKeys.repositories(search, limit),
    queryFn: ({ signal }) => api<List<GitHubRepository>>(`/github/repositories${params({ search, limit })}`, { signal }),
    enabled,
    placeholderData: keepPreviousData,
  })
}

export function useBranches(installation: string | undefined, repository: number | undefined) {
  return useQuery({
    queryKey: githubKeys.branches(installation ?? '', repository ?? 0),
    queryFn: ({ signal }) => api<{ items: GitHubBranch[] }>(`/github/installations/${enc(installation!)}/repositories/${repository}/branches`, { signal }),
    enabled: !!installation && !!repository,
  })
}

/** The app's repository; `null` (not an error) when it has none: the API answers 404. */
export function useProjectRepository(project: string, enabled = true) {
  return useQuery({
    queryKey: githubKeys.repository(project),
    queryFn: async ({ signal }) => {
      try {
        return await api<RepositoryConnection>(repoPath(project), { signal })
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null
        throw e
      }
    },
    enabled,
  })
}

/** A push that is still on its way to becoming a version. */
export const pushActive = (s: PushStatus | undefined) => s === 'queued' || s === 'importing'

/**
 * Newest push first. Polls like the deployments do while the newest is
 * queued or importing, and refreshes the environment once it settles: a
 * deployed push is a new version that the overview has to pick up.
 */
export function usePushes(project: string, enabled = true) {
  const qc = useQueryClient()
  const query = useQuery({
    queryKey: githubKeys.pushes(project),
    queryFn: ({ signal }) => api<List<GitHubPush>>(`${repoPath(project)}/pushes${params({ limit: 10 })}`, { signal }),
    enabled,
    refetchInterval: q => (pushActive(q.state.data?.items[0]?.status) ? PUBLISHING_POLL_MS : false),
  })
  const active = pushActive(query.data?.items[0]?.status)
  const was = useRef(active)
  useEffect(() => {
    if (was.current && !active) void qc.invalidateQueries({ queryKey: releaseKeys.environments(project) })
    was.current = active
  }, [active, project, qc])
  return query
}

/** Connects (or reconnects) an app to a repository branch; the branch head is queued to publish. */
export function useConnectRepository() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ project, body }: { project: string; body: ConnectRepository }) =>
      api<RepositoryConnection>(repoPath(project), { method: 'PUT', body }),
    onSuccess: (connection, { project }) => {
      qc.setQueryData(githubKeys.repository(project), connection)
      void qc.invalidateQueries({ queryKey: githubKeys.pushes(project) })
      void qc.invalidateQueries({ queryKey: releaseKeys.environments(project) })
    },
  })
}

export function useDisconnectRepository(project: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api<void>(repoPath(project), { method: 'DELETE' }),
    onSuccess: () => {
      qc.setQueryData(githubKeys.repository(project), null)
      qc.removeQueries({ queryKey: githubKeys.pushes(project) })
    },
  })
}

/** "Publicar ahora": queues the branch head. */
export function usePublishNow(project: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api<GitHubPush>(`${repoPath(project)}/publish`, { method: 'POST' }),
    onSuccess: () => qc.invalidateQueries({ queryKey: githubKeys.pushes(project) }),
  })
}

/** The connection body for a picked repository and branch; the API fills the port (8080) and health path. */
export function connectBody(repo: GitHubRepository, branch: string, root: string): ConnectRepository {
  return { installation_id: repo.installation_id, repository_id: repo.id, branch, root: root.trim() }
}

/** Plain words for where a push is (UX v3: no "importing", no "webhook"). */
export const pushStatusText: Record<PushStatus, string> = {
  queued: 'En cola para publicarse',
  importing: 'Descargando tu código…',
  deployed: 'Publicado',
  failed: 'No se pudo publicar',
  skipped: 'Reemplazado por un cambio más nuevo',
}

/** Where to manage the App on GitHub: the user's settings or the organization's. */
export function manageOnGitHubURL(i: Pick<GitHubInstallation, 'account' | 'account_type'>): string {
  return i.account_type === 'Organization'
    ? `https://github.com/organizations/${enc(i.account)}/settings/installations`
    : 'https://github.com/settings/installations'
}

/** Only console paths, never another site: GitHub's return path comes through the URL. */
export function safeReturnTo(path: string | undefined, fallback = '/configuracion/github'): string {
  return path && path.startsWith('/') && !path.startsWith('//') && !path.includes('\\') ? path : fallback
}
