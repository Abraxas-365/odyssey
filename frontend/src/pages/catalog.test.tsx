import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, type Auth } from '@/lib/auth'
import { catalogEntry as entry } from '@/lib/catalog.fixture'
import { permissions } from '@/lib/permissions'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))

import { CatalogPage, offeringSummary, specLine } from './catalog'

const basic = entry({}, { limits: { storage_gi: { max: 50 } }, preapproved: { storage_gi: { max: 10 } } })
const prod = entry(
  { recipe: 'postgres-produccion', title: 'PostgreSQL de producción', description: 'Con réplica. Sin respaldos automáticos todavía.', spec: { postgres: { instances: 2, major: 17, cpu: '1', memory: '2Gi' } } },
  { active: false, tiers: [], defaults: [] },
)
const redis = entry({ recipe: 'redis-basico', kind: 'redis', title: 'Redis básico' })

function mount(can: boolean) {
  const auth: Auth = { can: p => can && p === permissions.catalogManage, user: { id: 'u1' }, logout: () => {}, switchOrganization: () => {} }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider value={auth}>
        <MemoryRouter>
          <CatalogPage />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

describe('CatalogPage', () => {
  beforeEach(() => { api.mockReset() })

  it('says only IT manages the catalog, without asking the API', () => {
    mount(false)
    expect(screen.getByText('Solo TI administra el catálogo')).toBeTruthy()
    expect(api).not.toHaveBeenCalled()
  })

  it('lists each kind with its options, ranges in plain words and what production lacks', async () => {
    api.mockResolvedValue({ items: [basic, prod, redis] })
    mount(true)
    const dbs = await screen.findByRole('list', { name: 'Bases de datos' })
    expect(within(dbs).getByText('PostgreSQL básico')).toBeTruthy()
    expect(within(dbs).getByText(/Tamaño: de 1 a 50 GB; al instante de 1 a 10 GB, más allá con aprobación\./)).toBeTruthy()
    expect(within(dbs).getByText(/Sin respaldos automáticos todavía/)).toBeTruthy()
    expect(within(dbs).getByText('Apagada')).toBeTruthy()
    expect(screen.getByRole('list', { name: 'Cachés' })).toBeTruthy()
    // no ids or units of the API on screen
    expect(document.body.textContent).not.toMatch(/storage_gi|GiB|preapproved|postgres-basico/)
  })

  it('saves a narrower offering as the PUT body, only what narrows', async () => {
    api.mockImplementation((_path: string, init?: { method?: string; body?: unknown }) =>
      init?.method === 'PUT' ? Promise.resolve({ ...basic, offering: { ...basic.offering, ...(init.body as object) } }) : Promise.resolve({ items: [basic, prod, redis] }),
    )
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: 'Editar PostgreSQL básico' }))
    const save = screen.getByRole('button', { name: 'Guardar' })
    expect(save.hasAttribute('disabled')).toBe(true)
    fireEvent.change(screen.getByLabelText('Al instante hasta'), { target: { value: '20' } })
    await userEvent.click(save)
    await waitFor(() => expect(api.mock.calls.some(c => c[1]?.method === 'PUT')).toBe(true))
    const put = api.mock.calls.find(c => c[1]?.method === 'PUT')!
    expect(put[0]).toBe('/catalog/recipes/postgres-basico/offering')
    expect(put[1].body).toEqual({ version: 1, active: true, tiers: ['sandbox', 'internal', 'production'], defaults: ['sandbox', 'internal', 'production'], limits: { storage_gi: { max: 50 } }, preapproved: { storage_gi: { max: 20 } } })
  })

  it('refuses a pre-approved range outside the limits before sending', async () => {
    api.mockResolvedValue({ items: [basic] })
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: 'Editar PostgreSQL básico' }))
    fireEvent.change(screen.getByLabelText('Al instante hasta'), { target: { value: '60' } })
    expect(screen.getByText(/dentro del límite \(1–50 GB\)/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Guardar' }).hasAttribute('disabled')).toBe(true)
  })

  it('cannot make a second default of a kind in one level', async () => {
    api.mockResolvedValue({ items: [basic, { ...prod, offering: { ...prod.offering, active: true, tiers: ['production'] } }] })
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: 'Editar PostgreSQL de producción' }))
    expect(screen.getAllByText(/ya lo es «PostgreSQL básico»/).length).toBeGreaterThan(0)
  })
})

describe('catalog lines', () => {
  it('reads the engine settings and an offering in one line each', () => {
    expect(specLine(prod)).toBe('2 instancias · PostgreSQL 17 · 1 CPU · 2 GB de memoria')
    expect(specLine(basic)).toBeUndefined()
    expect(offeringSummary(prod)).toEqual(['No se ofrece: ninguna app puede crearla.'])
    expect(offeringSummary(entry())).toEqual(['Se ofrece en apps Sandbox · Interna · Producción.', 'Es la opción por defecto en Sandbox · Interna · Producción.', 'Tamaño: de 1 a 100 GB, todo al instante.'])
  })
})
