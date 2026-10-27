// Wire types for /api/v1 (docs/api.md). snake_case on purpose: these are the
// exact JSON shapes; don't remap them.

export interface Page { total: number; limit: number; offset: number }
export interface List<T> { items: T[]; page: Page }

export type ProjectStatus = 'active' | 'archived'

/** docs/api.md §11. The UI calls a project an "app". */
export interface Project {
  id: string
  organization_id: string
  name: string
  slug: string
  description: string
  status: ProjectStatus
  created_by: string
  updated_by: string
  created_at: string
  updated_at: string
  archived_at?: string | null
}

export interface ProjectCreate { name: string; slug?: string; description?: string }
export interface ProjectPatch { name?: string; description?: string }
export interface ProjectFilter { status?: ProjectStatus; search?: string; limit?: number; offset?: number }
