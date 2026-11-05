import { useEffect, useRef } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './api'
import { releaseKeys } from './releases'
import type { EnvironmentUses } from './types'

/** The first wait between asks after the user changed something ("Preparando…", "Aplicando…"). */
export const USES_POLL_MIN_MS = 3000
/** The longest wait between asks. */
export const USES_POLL_MAX_MS = 30_000
/** The console stops asking this long after the user's change: past it, something else is wrong and a reload is the way. */
export const USES_POLL_WINDOW_MS = 5 * 60_000

const enc = encodeURIComponent

/** Under the environment's key, so every settings, resource, AI or access mutation that refreshes the environment refreshes this too. */
export const usesKeys = {
  get: (project: string, env: string) => [...releaseKeys.env(project, env), 'uses'] as const,
}

/**
 * True while the answer will change by itself because the app is working on
 * it: a resource preparing or being erased, the AI changing, a change on its
 * way to the app. Not while it waits for a person or for IT: a change that
 * failed to apply, an egress policy the cluster has not confirmed (IT's, can
 * take forever) or a section that could not be read.
 */
export function usesChanging(u: EnvironmentUses | undefined): boolean {
  if (!u) return false
  return (
    (u.access.pending && !u.access.failed) ||
    u.resources.some(r => r.status === 'preparing' || r.status === 'deleting') ||
    u.ai?.status === 'enabling' ||
    u.ai?.status === 'rotating' ||
    u.ai?.status === 'disabling'
  )
}

/**
 * How long to wait before asking again, `elapsed` ms after the user's change:
 * 3 s, doubling every 30 s (6, 12, 24) up to 30 s; `false` once the window is over.
 */
export function usesPollDelay(elapsed: number): number | false {
  if (elapsed < 0 || elapsed >= USES_POLL_WINDOW_MS) return false
  const step = Math.floor(elapsed / 30_000)
  return Math.min(USES_POLL_MIN_MS * 2 ** step, USES_POLL_MAX_MS)
}

/** "Lo que usa esta app" (GET …/uses): what the app depends on and what needs attention. */
export function useUses(project: string, env: string | undefined) {
  const client = useQueryClient()
  // When the user last changed something (a mutation that succeeded while this is on screen). Opening the page never polls.
  const changedAt = useRef<number | null>(null)
  useEffect(() => {
    if (!env) return
    const unsubscribe = client.getMutationCache().subscribe(e => {
      if (e.type !== 'updated' || e.action.type !== 'success') return
      changedAt.current = Date.now()
      // asks again now, so the polling starts from what the change did
      void client.invalidateQueries({ queryKey: usesKeys.get(project, env) })
    })
    return () => {
      unsubscribe()
      // another app or environment starts without a change of its own
      changedAt.current = null
    }
  }, [client, project, env])
  return useQuery({
    queryKey: usesKeys.get(project, env ?? ''),
    queryFn: ({ signal }) => api<EnvironmentUses>(`/projects/${enc(project)}/environments/${enc(env!)}/uses`, { signal }),
    enabled: !!env,
    refetchInterval: q => (changedAt.current !== null && usesChanging(q.state.data) ? usesPollDelay(Date.now() - changedAt.current) : false),
  })
}
