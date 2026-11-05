import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, params } from './api'
import type { Cluster, ClusterCreate, ClusterCreated, ClusterEgress, ClusterEgressSet, ClusterList, ClusterMoved, ClusterStatus, EnrollmentToken } from './types'

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

export function useClusters(status?: ClusterStatus, enabled = true) {
  return useQuery({
    queryKey: clusterKeys.list(status),
    queryFn: ({ signal }) => api<ClusterList>(`/clusters${params({ status, limit: 100 })}`, { signal }),
    placeholderData: keepPreviousData,
    enabled,
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

/** Revokes a cluster; `moveTo` also sends its apps there, in the same step. */
export function useRevokeCluster() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: ({ id, moveTo }: { id: string; moveTo?: string }) =>
      api<Cluster>(`/clusters/${enc(id)}/revoke`, { method: 'POST', body: moveTo ? { move_to: moveTo } : undefined }),
    onSuccess: cluster => invalidate(cluster),
  })
}

/** Sends the apps left on a revoked cluster to an active one. */
export function useMoveClusterApps() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: ({ id, to }: { id: string; to: string }) =>
      api<ClusterMoved>(`/clusters/${enc(id)}/move`, { method: 'POST', body: { to } }),
    onSuccess: () => invalidate(),
  })
}

/** Where the apps of `from` can go: the other active clusters. */
export function moveTargets(from: Pick<Cluster, 'id'>, clusters: Cluster[]) {
  return clusters.filter(c => c.id !== from.id && c.status === 'active')
}

/** "1 app" / "3 apps". */
export function appCount(n: number) {
  return n === 1 ? '1 app' : `${n} apps`
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

// ---- Salida a internet (2d, docs/egress.md) ----

export const egressKey = (id: string) => ['clusters', 'egress', id] as const

/** What the apps of a cluster may reach, and whether its agent applied it. */
export function useClusterEgress(id: string | undefined, enabled = true) {
  return useQuery({
    queryKey: egressKey(id ?? ''),
    queryFn: ({ signal }) => api<ClusterEgress>(`/clusters/${enc(id!)}/egress`, { signal }),
    enabled: !!id && enabled,
    // Until the agent answers, look again soon (it applies a change in seconds).
    refetchInterval: q => (q.state.data?.state === 'pending' ? WAITING_POLL_MS : false),
  })
}

export function useSetClusterEgress() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: ClusterEgressSet }) =>
      api<ClusterEgress>(`/clusters/${enc(id)}/egress`, { method: 'PUT', body }),
    onSuccess: (egress, { id }) => qc.setQueryData(egressKey(id), egress),
  })
}

/** What the card says of a state, in plain words. */
export const egressStates: Record<ClusterEgress['state'], { label: string; tone: 'ok' | 'warn' | 'bad' | 'neutral' }> = {
  applied: { label: 'Aplicada en el clúster', tone: 'ok' },
  pending: { label: 'Pendiente: el clúster todavía no la confirma', tone: 'warn' },
  failed: { label: 'El clúster no pudo aplicarla', tone: 'bad' },
  unsupported: { label: 'Este clúster no puede aplicarla: actualiza su agente', tone: 'neutral' },
}

const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/

function validIPv6(s: string) {
  if (!/^[0-9a-fA-F:.]+$/.test(s) || !s.includes(':')) return false
  const dbl = s.split('::')
  if (dbl.length > 2) return false
  let tail = s
  let groups = 0
  // an embedded IPv4 tail counts as two groups
  const last = s.slice(s.lastIndexOf(':') + 1)
  if (last.includes('.')) {
    if (!IPV4.test(last)) return false
    tail = s.slice(0, s.lastIndexOf(':') + 1) + '0:0'
  }
  const parts = (tail.includes('::') ? tail.split('::') : [tail]).flatMap(p => (p === '' ? [] : p.split(':')))
  for (const p of parts) {
    if (!/^[0-9a-fA-F]{1,4}$/.test(p)) return false
    groups++
  }
  return dbl.length === 2 ? groups < 8 : groups === 8
}

/**
 * Checks one allow-list entry: an address or a CIDR (IPv4 or IPv6). Returns a
 * plain-Spanish problem, or undefined when it is fine. The server validates
 * again and has the last word; this only saves a round trip.
 */
export function cidrProblem(entry: string): string | undefined {
  const [addr, bits, extra] = entry.split('/')
  if (extra !== undefined) return 'Tiene demasiadas barras'
  const v4 = IPV4.test(addr)
  if (!v4 && !validIPv6(addr)) return 'No es una dirección IP válida'
  if (bits !== undefined) {
    if (!/^\d{1,3}$/.test(bits)) return 'El tamaño de la red debe ser un número'
    const n = Number(bits)
    if (n > (v4 ? 32 : 128)) return `El tamaño de la red llega hasta ${v4 ? 32 : 128}`
    if (n === 0) return 'Eso abriría todo: para eso elige «Internet pública»'
  }
  return undefined
}

/** Reads the editor's text (one entry per line, or separated by commas/spaces). */
export function parseAllowList(text: string): { entries: string[]; problems: { entry: string; problem: string }[] } {
  const entries: string[] = []
  const problems: { entry: string; problem: string }[] = []
  for (const raw of text.split(/[\s,;]+/)) {
    if (!raw) continue
    const problem = cidrProblem(raw)
    if (problem) problems.push({ entry: raw, problem })
    else if (!entries.includes(raw)) entries.push(raw)
  }
  return { entries, problems }
}

// The ranges that are never a public destination: private networks (where the
// cluster's own pods, Services and nodes and the company's networks live), and
// the other reserved ones. docs/egress.md; agent/egress reservedV4/reservedV6.
const INTERNAL_V4 = [
  '10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', '100.64.0.0/10', '169.254.0.0/16', '127.0.0.0/8',
  '0.0.0.0/8', '192.0.0.0/24', '198.18.0.0/15', '224.0.0.0/4', '240.0.0.0/4',
]
const INTERNAL_V6 = ['fc00::/7', 'fe80::/10', '::1/128', '64:ff9b::/96', 'ff00::/8']

function v4Number(s: string): bigint {
  return s.split('.').reduce((n, p) => n * 256n + BigInt(p), 0n)
}

/** An IPv6 address (already valid) as a number; an embedded IPv4 tail counts as two groups. */
function v6Number(s: string): bigint {
  let text = s
  const last = s.slice(s.lastIndexOf(':') + 1)
  if (last.includes('.')) {
    const n = v4Number(last)
    text = s.slice(0, s.lastIndexOf(':') + 1) + (n >> 16n).toString(16) + ':' + (n & 0xffffn).toString(16)
  }
  const [head, tail] = text.split('::')
  const h = head ? head.split(':') : []
  const t = tail === undefined ? [] : tail ? tail.split(':') : []
  const groups = tail === undefined ? h : [...h, ...Array<string>(8 - h.length - t.length).fill('0'), ...t]
  return groups.reduce((n, g) => n * 65536n + BigInt(parseInt(g, 16)), 0n)
}

/** The first and last address an entry covers, and its family. Call it on a valid entry. */
function range(entry: string): { lo: bigint; hi: bigint; v6: boolean } {
  const [addr, bits] = entry.split('/')
  const v6 = !IPV4.test(addr)
  const size = v6 ? 128n : 32n
  const n = v6 ? v6Number(addr) : v4Number(addr)
  const keep = bits === undefined ? size : BigInt(bits)
  const host = (1n << (size - keep)) - 1n
  return { lo: n & ~host, hi: n | host, v6 }
}

/**
 * Says why an allow entry is internal (inside, or covering, a private or
 * reserved range) — the cluster's pods, Services or nodes, or the company's
 * network — or undefined when it is public. Call it on a valid entry. An
 * IPv4-mapped IPv6 address counts as its IPv4 one.
 */
export function internalProblem(entry: string): string | undefined {
  let { lo, hi, v6 } = range(entry)
  const mapped = 0xffff00000000n
  if (v6 && lo >> 32n === 0xffffn && hi >> 32n === 0xffffn) {
    lo -= mapped
    hi -= mapped
    v6 = false
  }
  const against = v6 ? INTERNAL_V6 : INTERNAL_V4
  const hit = against.find(r => {
    const x = range(r)
    return lo <= x.hi && hi >= x.lo
  })
  return hit ? 'Esta dirección es interna del clúster o de tu red: las apps podrán llegar a ella.' : undefined
}

/** The entries that need the person's confirmation before saving, with the reason. */
export function internalEntries(entries: string[]): { entry: string; problem: string }[] {
  return entries.flatMap(entry => {
    const problem = internalProblem(entry)
    return problem ? [{ entry, problem }] : []
  })
}
