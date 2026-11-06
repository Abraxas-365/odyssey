import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import { AuthProvider, type Auth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import type { Attention, AttentionItem, InventoryPage as Page, InventoryRow } from '@/lib/types'

const api = vi.fn()
const apiFile = vi.fn()
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn() }))
vi.mock('sonner', () => ({ toast }))
vi.mock('@/lib/api', async orig => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: (...a: unknown[]) => api(...a),
  apiFile: (...a: unknown[]) => apiFile(...a),
}))

import { AttentionSection, allInOrder } from '@/features/inventory/attention-section'
import { InventoryPage } from './inventory'

function mount(ui: React.ReactElement, can: string[] = []) {
  const auth: Auth = { can: p => can.includes(p), user: { id: 'u1' }, logout: () => {}, switchOrganization: () => {} }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider value={auth}>
        <MemoryRouter>{ui}</MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

const item = (p: Partial<AttentionItem>): AttentionItem => ({
  kind: 'app_attention', scope: 'mine', severity: 'warning', sentence: 'Visitas: falta cargar un secreto.', link: '/apps/a1', app_id: 'a1', environment_id: null, cluster_id: null, at: null, ...p,
})
const attention = (p: Partial<Attention>): Attention => ({ items: [], unavailable: [], truncated: false, ...p })

describe('AttentionSection', () => {
  beforeEach(() => {
    api.mockReset()
  })

  it('shows nothing when nothing needs attention: only real signals appear (UX v4 §5.2)', async () => {
    api.mockResolvedValue(attention({}))
    const { container } = mount(<AttentionSection />)
    await waitFor(() => expect(api).toHaveBeenCalled())
    await waitFor(() => expect(container.textContent).toBe(''))
    expect(allInOrder(attention({}))).toBe(true)
    expect(allInOrder(attention({ unavailable: ['github'] }))).toBe(false)
    expect(allInOrder(attention({ items: [item({})] }))).toBe(false)
    expect(allInOrder(undefined)).toBe(false)
  })

  it('offers your assistant the fix for your own version that did not go online', async () => {
    api.mockResolvedValue(attention({
      items: [
        item({ kind: 'deployment_failed', severity: 'critical', sentence: 'Visitas: la última versión no se publicó.', app_slug: 'visitas' }),
        item({ kind: 'deployment_failed', scope: 'it', severity: 'critical', sentence: 'Cobros: la última versión no se publicó.', app_id: 'a2', app_slug: 'cobros', link: '/apps/a2' }),
      ],
    }))
    mount(<AttentionSection />)
    await screen.findByText('Visitas: la última versión no se publicó.')
    expect(screen.getAllByRole('link', { name: 'Ver qué pasó' })).toHaveLength(2)
    // only the person's own: TI sees what happened, the owner's assistant fixes it
    const fix = screen.getAllByRole('button', { name: /Pedir a mi asistente que lo arregle/ })
    expect(fix).toHaveLength(1)
    await userEvent.click(fix[0])
    expect(await screen.findByText(/La última versión de la app visitas de Odicea no se publicó/)).toBeTruthy()
  })

  it('words the publications under way and GitHub for TI', async () => {
    api.mockResolvedValue(attention({
      items: [
        item({ kind: 'publishing', severity: 'info', sentence: 'Visitas: se está publicando una versión nueva.' }),
        item({ kind: 'github_not_connected', scope: 'it', severity: 'info', sentence: 'GitHub no está conectado.', link: '/administracion/github', app_id: null }),
      ],
    }))
    mount(<AttentionSection />)
    expect((await screen.findByRole('link', { name: 'Ver progreso' })).getAttribute('href')).toBe('/apps/a1')
    expect(screen.getByRole('link', { name: 'Conectar GitHub' }).getAttribute('href')).toBe('/administracion/github')
  })

  it('never says "Todo en orden" when a source could not be read', async () => {
    api.mockResolvedValue(attention({ unavailable: ['clusters'] }))
    mount(<AttentionSection />)
    expect(await screen.findByText('No pudimos revisar todo.')).toBeTruthy()
    expect(screen.getByText(/los clústeres/)).toBeTruthy()
    expect(screen.queryByText('Todo en orden')).toBeNull()
  })

  it('lists each item with its sentence and one link, marking TI’s', async () => {
    api.mockResolvedValue(attention({
      items: [
        item({ kind: 'cluster_disconnected', scope: 'it', severity: 'critical', sentence: 'El clúster «Lima» no responde.', link: '/administracion/clusteres', app_id: null, cluster_id: 'c1' }),
        item({}),
      ],
    }))
    mount(<AttentionSection />)
    expect(await screen.findByText('El clúster «Lima» no responde.')).toBeTruthy()
    expect(screen.getByText('Visitas: falta cargar un secreto.')).toBeTruthy()
    expect(screen.getAllByText('TI')).toHaveLength(1)
    expect(screen.getByRole('link', { name: 'Ver clústeres' }).getAttribute('href')).toBe('/administracion/clusteres')
    expect(screen.getByRole('link', { name: 'Resolver' }).getAttribute('href')).toBe('/apps/a1')
    expect(screen.queryByText('Todo en orden')).toBeNull()
  })

  it('folds a long list and opens it on request', async () => {
    api.mockResolvedValue(attention({ items: Array.from({ length: 8 }, (_, i) => item({ app_id: `a${i}`, sentence: `App ${i}: falta algo.`, link: `/apps/a${i}` })) }))
    mount(<AttentionSection />)
    expect(await screen.findByText('App 0: falta algo.')).toBeTruthy()
    expect(screen.queryByText('App 7: falta algo.')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Ver 2 más' }))
    expect(screen.getByText('App 7: falta algo.')).toBeTruthy()
  })

  it('shows the API error with a retry', async () => {
    api.mockImplementation(async () => {
      throw new ApiError('Algo falló', 500, 'INTERNAL')
    })
    mount(<AttentionSection />)
    expect(await screen.findByRole('alert')).toBeTruthy()
  })
})

const row = (p: Partial<InventoryRow> & { name?: string; id?: string }): InventoryRow => ({
  app: { id: p.id ?? 'a1', slug: 'visitas', name: p.name ?? 'Visitas', tier: 'production' },
  environment: { id: `e-${p.id ?? 'a1'}`, slug: 'production', kind: 'production' },
  owner: 'u1', access: 'team', url: '', status: 'online', online: { image: 'r/x@sha256:1', since: null }, resources: [], ai: false,
  secrets: { total: 0, missing: [] }, scan: { state: 'clean', critical: 0, high: 0, scanned_at: null }, cluster: { id: 'c1', name: 'Lima' },
  needs_attention: false, attention: [], unavailable: [], ...p,
})
const page = (items: InventoryRow[], patch: Partial<Page> = {}): Page => ({ items, page: { total: items.length, limit: 25, offset: 0 }, truncated: false, unavailable: [], ...patch })

describe('InventoryPage', () => {
  beforeEach(() => {
    api.mockReset()
    apiFile.mockReset()
    toast.success.mockReset()
    toast.warning.mockReset()
  })

  it('says only IT can see it, without asking the API (it would be a 403)', () => {
    mount(<InventoryPage />, [])
    expect(screen.getByText('Solo TI puede ver el inventario')).toBeTruthy()
    expect(api).not.toHaveBeenCalled()
  })

  it('lists rows with their scan in words and what could not be read', async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path.startsWith('/clusters') ? { items: [], page: { total: 0, limit: 100, offset: 0 } } : page([
        row({ scan: { state: 'vulnerable', critical: 2, high: 1, scanned_at: null }, needs_attention: true }),
        row({ id: 'a2', name: 'Cotizador', scan: { state: 'unknown', critical: 0, high: 0, error: 'Trivy no respondió', scanned_at: null }, unavailable: ['cluster'] }),
      ])),
    )
    mount(<InventoryPage />, [permissions.inventoryRead])
    expect(await screen.findByText('2 críticas, 1 alta')).toBeTruthy()
    expect(screen.getByText('Necesita atención')).toBeTruthy()
    expect(screen.getByText('No se pudo escanear')).toBeTruthy()
    expect(screen.getByText('Trivy no respondió')).toBeTruthy()
    expect(screen.getByText('No pudimos leer el clúster ahora.')).toBeTruthy()
    expect(screen.getAllByText('Tú')).toHaveLength(2)
  })

  it('narrows by tier and asks the API for it, from the first page', async () => {
    api.mockImplementation((path: string) => Promise.resolve(path.startsWith('/clusters') ? { items: [], page: { total: 0, limit: 100, offset: 0 } } : page([row({})])))
    mount(<InventoryPage />, [permissions.inventoryRead])
    await screen.findByText('Sin vulnerabilidades graves')
    await userEvent.click(screen.getByRole('radio', { name: 'Sandbox' }))
    await waitFor(() => expect(api.mock.calls.some(c => String(c[0]).startsWith('/inventory') && String(c[0]).includes('tier=sandbox') && String(c[0]).includes('offset=0'))).toBe(true))
  })

  it('says the clusters could not be read instead of an empty list, on screen and in the CSV', async () => {
    api.mockImplementation((path: string) => Promise.resolve(path.startsWith('/clusters') ? { items: [], page: { total: 0, limit: 100, offset: 0 } } : page([], { unavailable: ['clusters'] })))
    apiFile.mockResolvedValue({ blob: new Blob(['x']), headers: new Headers({ 'X-Inventory-Truncated': 'false', 'X-Inventory-Unavailable': 'clusters' }) })
    URL.createObjectURL = vi.fn(() => 'blob:x')
    URL.revokeObjectURL = vi.fn()
    mount(<InventoryPage />, [permissions.inventoryRead])
    expect(await screen.findByText('No pudimos leer los clústeres ahora.')).toBeTruthy()
    expect(screen.getByText('No se pudo comprobar este clúster')).toBeTruthy()
    expect(screen.queryByText('No hay entornos que coincidan')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /Descargar CSV/ }))
    await waitFor(() => expect(toast.warning).toHaveBeenCalledWith(expect.stringContaining('no pudimos leer los clústeres ahora')))
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('downloads the CSV of the filters, without the page', async () => {
    api.mockImplementation((path: string) => Promise.resolve(path.startsWith('/clusters') ? { items: [], page: { total: 0, limit: 100, offset: 0 } } : page([row({})])))
    apiFile.mockResolvedValue({ blob: new Blob(['x']), headers: new Headers({ 'X-Inventory-Truncated': 'false' }) })
    URL.createObjectURL = vi.fn(() => 'blob:x')
    URL.revokeObjectURL = vi.fn()
    mount(<InventoryPage />, [permissions.inventoryRead])
    await screen.findByText('Sin vulnerabilidades graves')
    await userEvent.click(screen.getByRole('radio', { name: 'Necesitan atención' }))
    await userEvent.click(screen.getByRole('button', { name: /Descargar CSV/ }))
    await waitFor(() => expect(apiFile).toHaveBeenCalled())
    const path = String(apiFile.mock.calls[0][0])
    expect(path).toContain('format=csv')
    expect(path).toContain('needs_attention=true')
    expect(path).not.toContain('limit=')
    expect(path).not.toContain('offset=')
  })
})
