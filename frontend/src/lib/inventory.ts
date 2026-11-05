import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api, apiFile, params } from './api'
import type { Attention, InventoryFilter, InventoryPage } from './types'

/** The Inicio list and the sidebar look again every 30 s (a cluster dropping, an approval arriving). */
export const ATTENTION_POLL_MS = 30_000

export const inventoryKeys = {
  all: ['inventory'] as const,
  list: (f: InventoryFilter) => ['inventory', 'list', f] as const,
  attention: ['attention'] as const,
}

/** GET /inventory?tier=a&tier=b…: the API repeats `tier` (OR). */
export function inventoryQuery(f: InventoryFilter & { format?: 'csv' }): string {
  const { tier, ...rest } = f
  const q = params(rest as Record<string, string | number | boolean | undefined>)
  if (!tier?.length) return q
  const t = tier.map(v => `tier=${encodeURIComponent(v)}`).join('&')
  return q ? `${q}&${t}` : `?${t}`
}

/** IT only (`odyssey:inventory:read`): ask only when the user can. */
export function useInventory(filter: InventoryFilter, enabled = true) {
  return useQuery({
    queryKey: inventoryKeys.list(filter),
    queryFn: ({ signal }) => api<InventoryPage>(`/inventory${inventoryQuery(filter)}`, { signal }),
    placeholderData: keepPreviousData,
    enabled,
  })
}

/** "Necesita tu atención": the caller's own, plus IT's when they hold the permissions. */
export function useAttention() {
  return useQuery({
    queryKey: inventoryKeys.attention,
    queryFn: ({ signal }) => api<Attention>('/attention', { signal }),
    refetchInterval: ATTENTION_POLL_MS,
  })
}

/** Downloads every row that matches the filters (the API's CSV, not the page on screen). */
export async function downloadInventoryCsv(filter: InventoryFilter): Promise<{ truncated: boolean; clustersUnavailable: boolean }> {
  const { limit: _l, offset: _o, ...rest } = filter
  const { blob, headers } = await apiFile(`/inventory${inventoryQuery({ ...rest, format: 'csv' })}`)
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `inventario-${new Date().toISOString().slice(0, 10)}.csv`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
  return {
    truncated: headers.get('X-Inventory-Truncated') === 'true',
    clustersUnavailable: (headers.get('X-Inventory-Unavailable') ?? '').split(',').includes('clusters'),
  }
}
