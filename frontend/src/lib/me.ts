import { useMutation, useQuery, useQueryClient, type UseQueryResult } from '@tanstack/react-query'
import { api } from './api'
import type { Me } from './types'

export const meKey = ['me'] as const

/**
 * The person using the console, about themselves (GET /me): their role, and
 * whether their AI assistant is connected. The only source of "conectado"
 * (UX v4 §6.1): nothing else in the console decides it. Read it through
 * knownMe: a failed refetch keeps the old answer, which is no longer known.
 */
export function useMe() {
  return useQuery({
    queryKey: meKey,
    queryFn: ({ signal }) => api<Me>('/me', { signal }),
    staleTime: 60_000,
    refetchOnWindowFocus: true,
  })
}

/**
 * What /me says now: undefined while it has not answered or when its last
 * answer failed (React Query keeps the previous data on a failed refetch;
 * an old "connected" is not something we know any more).
 */
export function knownMe(q: Pick<UseQueryResult<Me>, 'data' | 'isError'>): Me | undefined {
  return q.isError ? undefined : q.data
}

/**
 * Ends an assistant's sessions and consent (DELETE /me/assistants/:id). /me
 * is refreshed without holding the mutation: the dialog closes at once.
 */
export function useDisconnectAssistant() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api<void>(`/me/assistants/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: meKey })
    },
  })
}
