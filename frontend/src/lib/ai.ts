import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './api'
import { releaseKeys } from './releases'
import type { AppAI } from './types'

/** While the AI is turning on, renewing or turning off the console asks again every 3 s. */
export const AI_POLL_MS = 3000

const enc = encodeURIComponent
const path = (project: string, env: string) => `/projects/${enc(project)}/environments/${enc(env)}/ai`

export const aiKeys = {
  get: (project: string, env: string) => [...releaseKeys.env(project, env), 'ai'] as const,
}

/** The app's AI (the key itself is never in the answer); polls while a change is running. */
export function useAI(project: string, env: string | undefined) {
  return useQuery({
    queryKey: aiKeys.get(project, env ?? ''),
    queryFn: ({ signal }) => api<AppAI>(path(project, env!), { signal }),
    enabled: !!env,
    refetchInterval: q => {
      const s = q.state.data?.status
      return s === 'enabling' || s === 'rotating' || s === 'disabling' ? AI_POLL_MS : false
    },
  })
}

/** A change is a settings revision and may start a deployment, so everything under the environment is refreshed. */
function useAIWrite(project: string, env: string, method: 'PUT' | 'POST' | 'DELETE', suffix = '') {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api<AppAI>(`${path(project, env)}${suffix}`, { method }),
    onSuccess: () => qc.invalidateQueries({ queryKey: releaseKeys.env(project, env) }),
  })
}

export const useEnableAI = (project: string, env: string) => useAIWrite(project, env, 'PUT')
export const useDisableAI = (project: string, env: string) => useAIWrite(project, env, 'DELETE')
export const useRotateAI = (project: string, env: string) => useAIWrite(project, env, 'POST', '/rotate')
