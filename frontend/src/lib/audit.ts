import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api, params } from './api'
import type { AuditEntryList, AuditPersonList, AuditQuery, AuditSummary, AuditVerification } from './types'

/**
 * The company log (UX-6, IT only: `odyssey:audit:read`). Every read of
 * /audit lives here; pages ask only when the user can (anyone else gets 403).
 */
export const auditKeys = {
  all: ['audit'] as const,
  entries: (q: AuditQuery) => ['audit', 'entries', q] as const,
  summary: ['audit', 'summary'] as const,
  people: ['audit', 'people'] as const,
  verify: ['audit', 'verify'] as const,
}

/** Re-hashing the whole chain is not free: once every 5 minutes is plenty. */
export const VERIFY_EVERY_MS = 5 * 60_000

/** GET /audit?type=a&type=b…: the API repeats `type` (OR). */
export function auditQuery(q: AuditQuery): string {
  const { type, ...rest } = q
  const s = params(rest as Record<string, string | number | undefined>)
  if (!type?.length) return s
  const t = type.map(v => `type=${encodeURIComponent(v)}`).join('&')
  return s ? `${s}&${t}` : `?${t}`
}

export function useAuditEntries(q: AuditQuery, enabled = true) {
  return useQuery({
    queryKey: auditKeys.entries(q),
    queryFn: ({ signal }) => api<AuditEntryList>(`/audit${auditQuery(q)}`, { signal }),
    placeholderData: keepPreviousData,
    enabled,
  })
}

export function useAuditSummary(enabled = true) {
  return useQuery({ queryKey: auditKeys.summary, queryFn: ({ signal }) => api<AuditSummary>('/audit/summary', { signal }), enabled })
}

export function useAuditPeople(enabled = true) {
  return useQuery({ queryKey: auditKeys.people, queryFn: ({ signal }) => api<AuditPersonList>('/audit/people', { signal }), enabled })
}

/** The seal: the server re-hashes the chain; `verified_at` says when. */
export function useAuditVerification(enabled = true) {
  return useQuery({
    queryKey: auditKeys.verify,
    queryFn: ({ signal }) => api<AuditVerification>('/audit/verify', { signal }),
    staleTime: VERIFY_EVERY_MS,
    refetchInterval: VERIFY_EVERY_MS,
    enabled,
  })
}
