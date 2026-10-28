import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, params } from './api'
import type { Cluster, ClusterCreate, ClusterCreated, ClusterList, ClusterStatus, EnrollmentToken } from './types'

const enc = encodeURIComponent

export const clusterKeys = {
  all: ['clusters'] as const,
  list: (status?: ClusterStatus) => ['clusters', 'list', status ?? 'all'] as const,
  detail: (id: string) => ['clusters', 'detail', id] as const,
}

/** While an agent is expected to connect, the console checks every 3 s. */
export const WAITING_POLL_MS = 3000
/** Otherwise every 30 s: "Conectado" means heard from in the last 90 s. */
export const CLUSTERS_POLL_MS = 30_000

export function useClusters(status?: ClusterStatus) {
  return useQuery({
    queryKey: clusterKeys.list(status),
    queryFn: ({ signal }) => api<ClusterList>(`/clusters${params({ status, limit: 100 })}`, { signal }),
    placeholderData: keepPreviousData,
    refetchInterval: q => (q.state.data?.items.some(c => awaitingAgent(c)) ? WAITING_POLL_MS : CLUSTERS_POLL_MS),
  })
}

/**
 * Whether an agent may still connect to a pending cluster: its token has
 * not expired (an abandoned one must not keep the console polling fast).
 */
export function awaitingAgent(c: Pick<Cluster, 'status' | 'token_expires_at'>, now = Date.now()) {
  return c.status === 'pending' && !!c.token_expires_at && Date.parse(c.token_expires_at) > now
}

/** One cluster; polls while `poll` (waiting for its agent). */
export function useCluster(id: string | undefined, poll = false) {
  return useQuery({
    queryKey: clusterKeys.detail(id ?? ''),
    queryFn: ({ signal }) => api<Cluster>(`/clusters/${enc(id!)}`, { signal }),
    enabled: !!id,
    refetchInterval: poll ? WAITING_POLL_MS : false,
  })
}

function useInvalidate() {
  const qc = useQueryClient()
  return (cluster?: Cluster) => {
    if (cluster) qc.setQueryData(clusterKeys.detail(cluster.id), cluster)
    void qc.invalidateQueries({ queryKey: clusterKeys.all })
  }
}

/** Registers a cluster; the response carries its token, shown only once. */
export function useCreateCluster() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (body: ClusterCreate) => api<ClusterCreated>('/clusters', { method: 'POST', body }),
    onSuccess: created => invalidate(created.cluster),
  })
}

/** A new enrolment token (replaces the pending one; re-enrols an active cluster). */
export function useIssueToken() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (id: string) => api<EnrollmentToken>(`/clusters/${enc(id)}/enrollment-token`, { method: 'POST' }),
    onSuccess: () => invalidate(),
  })
}

export function useRevokeCluster() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (id: string) => api<Cluster>(`/clusters/${enc(id)}/revoke`, { method: 'POST' }),
    onSuccess: cluster => invalidate(cluster),
  })
}

/** The four states TI sees (plan 1a-a3 §Consola). */
export type ClusterState = 'connected' | 'offline' | 'waiting' | 'revoked'

export function clusterState(c: Pick<Cluster, 'status' | 'connected'>): ClusterState {
  if (c.status === 'revoked') return 'revoked'
  if (c.status === 'pending') return 'waiting'
  return c.connected ? 'connected' : 'offline'
}

/**
 * Step 1 of connecting a cluster: store the token, the gateway and the CA
 * pin in the agent's Secret. The agent enrols with them on start and keeps
 * its identity there; the token is spent. The token goes in through stdin
 * from printf (a shell builtin), so it never shows in a process list.
 */
export function secretCommand(token: EnrollmentToken) {
  return [
    'kubectl create namespace odyssey-system --dry-run=client -o yaml | kubectl apply -f -',
    `printf '%s' '${token.token}' | kubectl -n odyssey-system create secret generic odyssey-agent \\`,
    '  --from-file=token=/dev/stdin \\',
    `  --from-literal=gateway=${token.gateway} \\`,
    `  --from-literal=ca-pin=${token.ca_pin} \\`,
    '  --dry-run=client -o yaml | kubectl apply -f -',
  ].join('\n')
}

/** Step 2: the agent's manifests (CRD, RBAC, Deployment), from the Odyssey repository. */
export const AGENT_INSTALL = 'kubectl apply -k agent/install'
