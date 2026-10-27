import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, params } from './api'
import type { List, Project, ProjectCreate, ProjectFilter, ProjectPatch } from './types'

export const projectKeys = {
  all: ['projects'] as const,
  list: (f: ProjectFilter) => ['projects', 'list', f] as const,
  detail: (id: string) => ['projects', 'detail', id] as const,
}

export function useProjects(filter: ProjectFilter = {}) {
  return useQuery({
    queryKey: projectKeys.list(filter),
    queryFn: ({ signal }) => api<List<Project>>(`/projects${params({ ...filter })}`, { signal }),
    placeholderData: keepPreviousData,
  })
}

export function useProject(id: string | undefined) {
  return useQuery({
    queryKey: projectKeys.detail(id ?? ''),
    queryFn: ({ signal }) => api<Project>(`/projects/${encodeURIComponent(id!)}`, { signal }),
    enabled: !!id,
  })
}

// Writes return the full resource (docs/api.md §6): seed the detail cache with
// it and invalidate lists instead of refetching the detail.
function useProjectWrite<V>(fn: (v: V) => Promise<Project>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: project => {
      qc.setQueryData(projectKeys.detail(project.id), project)
      void qc.invalidateQueries({ queryKey: ['projects', 'list'] })
    },
  })
}

export const useCreateProject = () => useProjectWrite((body: ProjectCreate) => api<Project>('/projects', { method: 'POST', body }))
export const useUpdateProject = (id: string) => useProjectWrite((body: ProjectPatch) => api<Project>(`/projects/${encodeURIComponent(id)}`, { method: 'PATCH', body }))
export const useArchiveProject = (id: string) => useProjectWrite(() => api<Project>(`/projects/${encodeURIComponent(id)}/archive`, { method: 'POST' }))

/** Same folding as the API (internal/text): lowercase ASCII DNS label, ≤ 40. */
export function slugify(name: string) {
  return name
    .normalize('NFD')
    .replace(/[\u0300-\u036f]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
    .replace(/-+$/g, '')
}
