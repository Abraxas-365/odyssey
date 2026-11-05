import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './api'
import { projectKeys } from './projects'
import type { PolicyDraft, PolicyEvaluation, PolicyInput, PolicyRule, Project, ProjectTier } from './types'

const enc = encodeURIComponent

export const policyKeys = {
  rules: ['policy', 'rules'] as const,
}

/** IT's rules (the first read also gives the organization its built-in ones). Needs policy:manage. */
export function usePolicyRules(enabled = true) {
  return useQuery({
    queryKey: policyKeys.rules,
    queryFn: ({ signal }) => api<{ items: PolicyRule[] }>('/policy/rules', { signal }),
    enabled,
  })
}

function useRulesWrite<V, R>(fn: (v: V) => Promise<R>) {
  const qc = useQueryClient()
  return useMutation({ mutationFn: fn, onSuccess: () => qc.invalidateQueries({ queryKey: policyKeys.rules }) })
}

export const useCreateRule = () => useRulesWrite((body: PolicyDraft) => api<PolicyRule>('/policy/rules', { method: 'POST', body }))
export const useReplaceRule = () => useRulesWrite(({ id, body }: { id: string; body: PolicyDraft }) => api<PolicyRule>(`/policy/rules/${enc(id)}`, { method: 'PUT', body }))
export const useDeleteRule = () => useRulesWrite((id: string) => api<void>(`/policy/rules/${enc(id)}`, { method: 'DELETE' }))

/** Dry run: nothing is recorded and nothing is stopped. */
export const useEvaluatePolicy = () => useMutation({ mutationFn: (body: PolicyInput) => api<PolicyEvaluation>('/policy/evaluate', { method: 'POST', body }) })

/** IT's change of an app's tier (needs projects:tier); the answer is the whole project. */
export function useSetTier(id: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (tier: ProjectTier) => api<Project>(`/projects/${enc(id)}/tier`, { method: 'PUT', body: { tier } }),
    onSuccess: project => {
      qc.setQueryData(projectKeys.detail(project.id), project)
      void qc.invalidateQueries({ queryKey: ['projects', 'list'] })
    },
  })
}
