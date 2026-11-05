import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './api'
import type { Approval, ApprovalList, ApprovalStatus, PendingApproval } from './types'

/** Requests are read-mostly: the bell and the Bandeja look again every 30 s. */
export const APPROVALS_POLL_MS = 30_000

const enc = encodeURIComponent

export interface ApprovalFilter {
  status?: ApprovalStatus[]
  mine?: boolean
  decidable?: boolean
  limit?: number
}

export const approvalKeys = {
  all: ['approvals'] as const,
  list: (f: ApprovalFilter) => ['approvals', 'list', f] as const,
  one: (id: string) => ['approvals', 'one', id] as const,
}

/** GET /approvals query string: `status` repeats (OR), booleans only when true. */
export function approvalsQuery(f: ApprovalFilter): string {
  const q: string[] = []
  for (const s of f.status ?? []) q.push(`status=${enc(s)}`)
  if (f.mine) q.push('mine=true')
  if (f.decidable) q.push('decidable=true')
  if (f.limit) q.push(`limit=${f.limit}`)
  return q.length ? `?${q.join('&')}` : ''
}

export function useApprovals(filter: ApprovalFilter = {}, enabled = true) {
  return useQuery({
    queryKey: approvalKeys.list(filter),
    queryFn: ({ signal }) => api<ApprovalList>(`/approvals${approvalsQuery(filter)}`, { signal }),
    placeholderData: keepPreviousData,
    refetchInterval: APPROVALS_POLL_MS,
    enabled,
  })
}

/** One request, for the deep link `/bandeja?id=<id>` (the MCP's get_request answers it). */
export function useApproval(id: string | null) {
  return useQuery({
    queryKey: approvalKeys.one(id ?? ''),
    queryFn: ({ signal }) => api<Approval>(`/approvals/${enc(id ?? '')}`, { signal }),
    refetchInterval: APPROVALS_POLL_MS,
    enabled: !!id,
    retry: false,
  })
}

/** The bell: pending requests the viewer can decide now. Only asked for people who may decide. */
export const inboxFilter: ApprovalFilter = { decidable: true, status: ['pending'], limit: 5 }

function useDecision(path: 'approve' | 'reject' | 'cancel') {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, note }: { id: string; note?: string }) =>
      api<Approval>(`/approvals/${enc(id)}/${path}`, { method: 'POST', body: path === 'cancel' ? undefined : { note: note?.trim() ?? '' } }),
    // approving runs the action and a deployment continues: everything may have changed
    onSettled: () => qc.invalidateQueries(),
  })
}

export const useApprove = () => useDecision('approve')
export const useReject = () => useDecision('reject')
export const useCancelRequest = () => useDecision('cancel')

/**
 * The 202 of a held action (docs/approvals.md): the API answers
 * `{status: "pending_approval", request_id, message}` instead of the usual
 * body. Every call that can be held checks it before reading its result.
 */
export function isPending(out: unknown): out is PendingApproval {
  return typeof out === 'object' && out !== null && (out as { status?: unknown }).status === 'pending_approval'
}

/** What the console tells the person asking: calm, no codes, nothing failed. */
export const pendingNotice = 'Le pedimos a TI que lo apruebe. Te avisamos cuando lo decidan.'
