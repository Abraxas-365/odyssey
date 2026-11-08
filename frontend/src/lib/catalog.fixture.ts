import type { CatalogEntry } from './types'

/** A catalog entry for tests: postgres-basico v1 offered everywhere as the default, with the size parameter. */
export function catalogEntry(over: Partial<CatalogEntry> = {}, offering: Partial<CatalogEntry['offering']> = {}): CatalogEntry {
  return {
    recipe: 'postgres-basico', version: 1, versions: [1], kind: 'postgres', title: 'PostgreSQL básico', description: 'Una base de datos.',
    engine: 'builtin', params: [{ name: 'storage_gi', label: 'Tamaño', unit: 'GiB', min: 1, max: 100, default: 1, mutability: 'grows' }], spec: {}, requires: [],
    ...over,
    offering: {
      recipe: over.recipe ?? 'postgres-basico', version: 1, active: true, tiers: ['sandbox', 'internal', 'production'], defaults: ['sandbox', 'internal', 'production'],
      limits: {}, preapproved: {}, teams: [], revision: 0, updated_by: '', created_at: '', updated_at: '', ...offering,
    },
  }
}
