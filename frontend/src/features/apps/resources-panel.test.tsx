import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { CatalogOffer, EnvironmentCatalog } from '@/lib/types'
import { catalogEntry } from '@/lib/catalog.fixture'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ can: () => true }) }))

import { ResourcesPanel } from './resources-panel'

const size = { name: 'storage_gi', label: 'Tamaño', unit: 'GiB', min: 1, max: 50, default: 1, preapproved_min: 1, preapproved_max: 10, mutability: 'grows' as const }
const offer = (over: Partial<CatalogOffer> = {}): CatalogOffer => ({
  kind: 'postgres', recipe: 'postgres-basico', recipe_version: 1, title: 'PostgreSQL básico', description: 'Una base de datos para empezar.', existing: false, default: true,
  params: [size], variables: [{ name: 'DATABASE_URL', secret: true, meaning: 'cómo conectarse' }], verdict: 'allow', rules: [], rules_verdict: 'allow', ...over,
})

function serve(catalog: EnvironmentCatalog) {
  api.mockImplementation((path: string, init?: { method?: string }) => {
    if (init?.method === 'POST') return Promise.resolve({ status: 'pending_approval', approval_id: 'a1', rules: ['catalog:preapproved'] })
    if (path === '/projects/p/services') return Promise.resolve({ items: [{ name: 'web', kind: 'web' }] })
    if (path.endsWith('/catalog')) return Promise.resolve(catalog)
    if (path.endsWith('/resources')) return Promise.resolve({ items: [], available_kinds: ['postgres', 'redis'] })
    if (path.endsWith('/ai')) return Promise.resolve({ status: 'off', available: true, enabled: false, services: [], scoped_settings: true, variables: [], base_url: '' })
    return Promise.resolve({ scoped_settings: true })
  })
}

function mount(technical = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><MemoryRouter><ResourcesPanel project="p" env="e" canWrite busy={false} technical={technical} /></MemoryRouter></QueryClientProvider>)
}
const posts = () => api.mock.calls.filter(c => c[1]?.method === 'POST')

describe('AddResourceDialog from the catalog (1b)', () => {
  beforeEach(() => { api.mockReset() })

  it('creates the default option at once, sending recipe and params', async () => {
    serve({ tier: 'internal', recipes: [offer()], unavailable: [] })
    mount()
    await userEvent.click(await screen.findByRole('button', { name: /Agregar/ }))
    expect(await screen.findByText('Al instante')).toBeTruthy()
    expect(screen.getByText(/Al instante hasta 10 GB/)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Agregar' }))
    await waitFor(() => expect(posts()).toHaveLength(1))
    expect(posts()[0][0]).toBe('/projects/p/environments/e/resources')
    expect(posts()[0][1].body).toMatchObject({ kind: 'postgres', recipe: 'postgres-basico', params: { storage_gi: 1 } })
    expect('storage_gi' in posts()[0][1].body).toBe(false)
  })

  it('says a size beyond the pre-approved range asks IT, and refuses one beyond the limit', async () => {
    serve({ tier: 'internal', recipes: [offer()], unavailable: [] })
    mount()
    await userEvent.click(await screen.findByRole('button', { name: /Agregar/ }))
    const field = await screen.findByLabelText('Tamaño (GB)')
    fireEvent.change(field, { target: { value: '20' } })
    expect(screen.getByText('Pide aprobación')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Pedir aprobación' })).toBeTruthy()
    fireEvent.change(field, { target: { value: '60' } })
    expect(screen.getByText('Tu empresa permite entre 1 y 50 GB.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Agregar' }).hasAttribute('disabled')).toBe(true)
    fireEvent.change(field, { target: { value: '20' } })
    await userEvent.click(screen.getByRole('button', { name: 'Pedir aprobación' }))
    await waitFor(() => expect(posts()).toHaveLength(1))
    expect(posts()[0][1].body.params).toEqual({ storage_gi: 20 })
  })

  it('lets one pick among several options, and shows what production lacks', async () => {
    serve({ tier: 'production', recipes: [offer(), offer({ recipe: 'postgres-produccion', title: 'PostgreSQL de producción', description: 'Con réplica. Sin respaldos automáticos todavía.', default: false, verdict: 'approval', rules_verdict: 'approval' })], unavailable: [] })
    mount()
    await userEvent.click(await screen.findByRole('button', { name: /Agregar/ }))
    await userEvent.click(await screen.findByRole('radio', { name: /PostgreSQL de producción/ }))
    expect(screen.getByText(/Sin respaldos automáticos todavía/)).toBeTruthy()
    expect(screen.getByText(/Las reglas de TI piden aprobarlo/)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Pedir aprobación' }))
    await waitFor(() => expect(posts()).toHaveLength(1))
    expect(posts()[0][1].body.recipe).toBe('postgres-produccion')
  })

  it('only offers the kinds the catalog offers here, and says why nothing is', async () => {
    serve({ tier: 'sandbox', recipes: [], unavailable: [{ kind: 'postgres', reason: 'TI no ofrece bases de datos en apps Sandbox.' }] })
    mount()
    await userEvent.click(await screen.findByRole('button', { name: /Agregar/ }))
    expect(await screen.findByText('TI no ofrece bases de datos en apps Sandbox.')).toBeTruthy()
    expect(screen.queryByLabelText('Nombre')).toBeNull()
  })

  it('names the recipe and the variables only in technical mode', async () => {
    serve({ tier: 'internal', recipes: [offer()], unavailable: [] })
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: /Agregar/ }))
    await userEvent.click(await screen.findByText('Qué recibe la app'))
    expect(screen.getByText('postgres-basico@1')).toBeTruthy()
  })
})

describe('ResizeDialog within the recipe offering', () => {
  beforeEach(() => { api.mockReset() })

  const db = {
    id: 'r1', organization_id: 'o', project_id: 'p', environment_id: 'e', name: 'db', kind: 'postgres', storage_gi: 8, recipe: 'postgres-basico', recipe_version: 1,
    params: { storage_gi: 8 }, generation: 1, status: 'active', created_by: 'u', created_at: '', updated_at: '', phase: 'ready', message: '', connected: true, prefix: '', services: [], variables: [],
  }
  function serveResize(resource = db) {
    api.mockImplementation((path: string, init?: { method?: string }) => {
      if (init?.method === 'PATCH') return Promise.resolve({ status: 'pending_approval', approval_id: 'a1', rules: ['catalog:preapproved'] })
      if (path === '/catalog/recipes') return Promise.resolve({ items: [catalogEntry({}, { limits: { storage_gi: { max: 50 } }, preapproved: { storage_gi: { max: 10 } } })] })
      if (path === '/projects/p/services') return Promise.resolve({ items: [] })
      if (path.endsWith('/resources')) return Promise.resolve({ items: [resource], available_kinds: ['postgres'] })
      if (path.endsWith('/ai')) return Promise.resolve({ status: 'off', available: true, enabled: false, services: [], scoped_settings: true, variables: [], base_url: '' })
      return Promise.resolve({ scoped_settings: true })
    })
  }

  it('uses IT limits, says beyond the instant range it asks, and sends the size', async () => {
    serveResize()
    mount()
    await userEvent.click(await screen.findByRole('button', { name: 'Cambiar tamaño' }))
    expect(await screen.findByText(/Al instante hasta 10 GB/)).toBeTruthy()
    const field = screen.getByLabelText('Tamaño (GB)')
    fireEvent.change(field, { target: { value: '9' } })
    expect((screen.getByRole('button', { name: 'Ampliar' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByText('Pide aprobación')).toBeNull()
    fireEvent.change(field, { target: { value: '60' } })
    expect(screen.getByText('Tu empresa permite hasta 50 GB.')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Ampliar' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(field, { target: { value: '20' } })
    expect(screen.getByText(/más de lo que TI dejó ampliar al instante/)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Pedir aprobación' }))
    await waitFor(() => expect(api.mock.calls.some(c => c[1]?.method === 'PATCH')).toBe(true))
    expect(api.mock.calls.find(c => c[1]?.method === 'PATCH')?.[1].body).toEqual({ storage_gi: 20 })
  })

  it('leaves a resource of another recipe version to the server', async () => {
    serveResize({ ...db, recipe_version: 2 })
    mount()
    await userEvent.click(await screen.findByRole('button', { name: 'Cambiar tamaño' }))
    const field = await screen.findByLabelText('Tamaño (GB)')
    fireEvent.change(field, { target: { value: '60' } })
    expect(await screen.findByText(/te lo diremos al guardar/)).toBeTruthy()
    expect(screen.queryByText(/Al instante hasta/)).toBeNull()
    expect((screen.getByRole('button', { name: 'Ampliar' }) as HTMLButtonElement).disabled).toBe(false)
  })
})

describe('a resource the company already has (stage 2)', () => {
  beforeEach(() => { api.mockReset() })

  const reportes = {
    id: 'r2', organization_id: 'o', project_id: 'p', environment_id: 'e', name: 'reportes', kind: 'postgres', storage_gi: 0, recipe: 'org-base-de-reportes', recipe_version: 1, existing: true,
    params: {}, generation: 1, status: 'active', created_by: 'u', created_at: '', updated_at: '', phase: 'ready', message: '', connected: false, prefix: '', services: [], variables: [],
  }
  function serveExisting(resource: Record<string, unknown> = reportes) {
    api.mockImplementation((path: string, init?: { method?: string }) => {
      if (init?.method === 'DELETE') return Promise.resolve({ status: 'applied' })
      if (path === '/projects/p/services') return Promise.resolve({ items: [] })
      if (path.endsWith('/catalog')) return Promise.resolve({ tier: 'internal', recipes: [offer({ recipe: 'org-base-de-reportes', title: 'Base de reportes', description: 'La de BI.', existing: true, params: [] })], unavailable: [] })
      if (path.endsWith('/resources')) return Promise.resolve({ items: [resource], available_kinds: ['postgres'] })
      if (path.endsWith('/ai')) return Promise.resolve({ status: 'off', available: true, enabled: false, services: [], scoped_settings: true, variables: [], base_url: '' })
      return Promise.resolve({ scoped_settings: true })
    })
  }

  it('shows it without a size and with nothing to resize', async () => {
    serveExisting()
    mount()
    expect(await screen.findByText('Base de datos · Ya existe en la empresa')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Cambiar tamaño' })).toBeNull()
    expect(screen.queryByText(/0 GB/)).toBeNull()
  })

  it('removes it from the app saying the company’s data is not touched, without typing its name', async () => {
    serveExisting()
    mount()
    await userEvent.click(await screen.findByRole('button', { name: 'Quitar reportes' }))
    expect(await screen.findByText(/La base de datos de la empresa no se toca/)).toBeTruthy()
    expect(screen.queryByText(/se borran para siempre/)).toBeNull()
    expect(screen.queryByLabelText(/para confirmar/)).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Quitar de la app' }))
    await waitFor(() => expect(api.mock.calls.some(c => c[1]?.method === 'DELETE')).toBe(true))
  })

  it('says a failed connection is for IT, in plain words', async () => {
    serveExisting({ ...reportes, phase: 'failed', message: 'SecretSyncedError: key rds/reportes not found' })
    mount()
    expect(await screen.findByText(/No se pudo conectar la base de datos de la empresa. Avísale a TI/)).toBeTruthy()
    expect(screen.queryByText(/SecretSyncedError/)).toBeNull()
  })

  it('marks the option as one Odicea only connects when adding it', async () => {
    serveExisting()
    mount()
    await userEvent.click(await screen.findByRole('button', { name: /Agregar/ }))
    expect(await screen.findByText(/Ya existe en la empresa: Odicea solo la conecta/)).toBeTruthy()
    expect(screen.queryByLabelText('Tamaño (GB)')).toBeNull()
    expect(screen.getByRole('radio', { name: 'Solo agregarla' })).toBeTruthy()
  })
})
