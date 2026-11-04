import { useQuery } from '@tanstack/react-query'
import { api } from './api'
import type { Assistant } from './types'

/** How to connect an AI assistant to this Odicea; the same for everyone with projects:read. */
export function useAssistant() {
  return useQuery({
    queryKey: ['assistant'] as const,
    queryFn: ({ signal }) => api<Assistant>('/assistant', { signal }),
    staleTime: 5 * 60_000,
  })
}
