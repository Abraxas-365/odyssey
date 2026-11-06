import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, type Auth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import type { AppState, Attention, Project } from '@/lib/types'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))

import { HomePage } from './home'

const project = (p: Partial<Project>): Project => ({
  id: 'a1', organization_id: 'o1', name: 'Visitas', slug: 'visitas', description: '', status: 'active', tier: 'internal',
  created_by: 'u1', updated_by: 'u1', created_at: '2026-11-01T10:00:00Z', updated_at: '2026-11-01T10:00:00Z', archived_at: null, ...p,
})
const state = (p: Partial<AppState>): AppState => ({
  app_id: 'a1', status: 'online', url: 'https://visitas.apps.acme.com/', since: null, access: { mode: 'organization', pending: null }, cluster: null, unavailable: [],
  last_change: { at: new Date(Date.now() - 2 * 3600_000).toISOString(), reason: 'deploy', status: 'ready', requested_by: 'u2', requested_by_name: 'Ana Torres', requested_via: 'assistant:claude_code' },
  ...p,
})
const page = <T,>(items: T[]) => ({ items, page: { total: items.length, limit: 50, offset: 0 } })

function routes(projects: Project[], states: AppState[], attention: Attention = { items: [], unavailable: [], truncated: false }) {
  api.mockImplementation((path: string, init?: { method?: string; body?: { name: string } }) => {
    if (init?.method === 'POST' && path === '/projects') return Promise.resolve(project({ id: 'new1', name: init.body!.name, slug: 'nueva' }))
    if (path.startsWith('/projects')) return Promise.resolve(page(projects))
    if (path.startsWith('/app-states')) return Promise.resolve({ items: states })
    if (path === '/attention') return Promise.resolve(attention)
    return Promise.resolve({})
  })
}

function Where() {
  const l = useLocation()
  return <p data-testid="where">{l.pathname + l.search}</p>
}

function mount(can: string[] = [permissions.projectsWrite, permissions.deploymentsRead]) {
  const auth: Auth = { can: p => can.includes(p), user: { id: 'u1', name: 'Pedro Ruiz' }, logout: () => {}, switchOrganization: () => {} }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider value={auth}>
        <MemoryRouter initialEntries={['/']}>
          <Routes>
            <Route path="/" element={<HomePage />} />
            <Route path="*" element={<Where />} />
          </Routes>
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

describe('Inicio (UX v4 §5.2)', () => {
  beforeEach(() => { api.mockReset(); localStorage.clear() })

  it('lists the apps as two lines a person reads, with the greeting as a small line', async () => {
    routes([project({})], [state({})])
    mount()
    const list = await screen.findByRole('list', { name: 'Apps' })
    const row = within(list).getAllByRole('listitem')[0]
    expect(within(row).getByRole('link', { name: 'Visitas' })).toHaveAttribute('href', '/apps/a1')
    expect(await within(row).findByText('En línea')).toBeInTheDocument()
    expect(within(row).getByText('visitas.apps.acme.com')).toBeInTheDocument()
    expect(within(row).getByText('Ana Torres publicó hace 2 horas')).toBeInTheDocument()
    expect(within(row).getByRole('img', { name: 'Canal: Claude Code' })).toBeInTheDocument()
    expect(within(row).getByLabelText('Quién puede abrirla')).toBeInTheDocument()
    expect(await screen.findByText(/Hola, Pedro/)).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText(/todo en orden/)).toBeInTheDocument())
    // no attention section when nothing needs it
    expect(screen.queryByText('Necesita tu atención')).toBeNull()
  })

  it('says nothing about an app it could not read', async () => {
    routes([project({})], [])
    mount()
    const row = (await screen.findAllByRole('listitem'))[0]
    await waitFor(() => expect(within(row).getByText('Actualizada', { exact: false })).toBeInTheDocument())
    expect(within(row).queryByText('En línea')).toBeNull()
    expect(within(row).queryByRole('button', { name: 'Copiar la dirección' })).toBeNull()
    expect(within(row).queryByLabelText('Quién puede abrirla')).toBeNull()
  })

  it('switches to a grid and remembers it', async () => {
    routes([project({})], [state({})])
    mount()
    await screen.findByRole('list', { name: 'Apps' })
    await userEvent.click(screen.getByRole('radio', { name: 'Cuadrícula' }))
    expect(screen.queryByRole('list', { name: 'Apps' })).toBeNull()
    expect(screen.getByRole('article')).toBeInTheDocument()
    expect(localStorage.getItem('odyssey:inicio:view')).toBe('grid')
  })

  it('the first time asks what to create and carries the answer into the dialog', async () => {
    routes([], [])
    mount()
    expect(await screen.findByRole('heading', { name: '¿Qué quieres crear?' })).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Apps' })).toBeNull()
    await userEvent.type(screen.getByRole('textbox', { name: '¿Qué quieres crear?' }), 'Una app para registrar visitas a clientes')
    await userEvent.click(screen.getByRole('button', { name: /Continuar/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('textbox', { name: '¿Qué quieres crear?' })).toHaveValue('Una app para registrar visitas a clientes')
    // the name comes from the answer
    expect((within(dialog).getByRole('textbox', { name: 'Nombre' }) as HTMLInputElement).value).not.toBe('')
    for (const way of ['Pedir a mi asistente', 'Importar de GitHub', 'Subir una carpeta']) expect(within(dialog).getByRole('radio', { name: new RegExp(way) })).toBeInTheDocument()
  })

  it('"Subir una carpeta" creates the app and opens it on its upload dialog', async () => {
    routes([], [])
    mount()
    await userEvent.type(await screen.findByRole('textbox', { name: '¿Qué quieres crear?' }), 'Cotizador')
    await userEvent.click(screen.getByRole('button', { name: /Continuar/ }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('radio', { name: /Subir una carpeta/ }))
    await userEvent.click(within(dialog).getByRole('button', { name: 'Crear y subir' }))
    expect(await screen.findByTestId('where')).toHaveTextContent('/apps/new1?subir=1')
  })

  it('"Pedir a mi asistente" creates the app and gives the words for the assistant', async () => {
    routes([], [])
    mount()
    await userEvent.type(await screen.findByRole('textbox', { name: '¿Qué quieres crear?' }), 'Cotizador de seguros')
    await userEvent.click(screen.getByRole('button', { name: /Continuar/ }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Crear app' }))
    expect(await within(dialog).findByText(/Creamos/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Construye en esta carpeta: Cotizador de seguros\. Cuando funcione, publícala en la app nueva de Odicea\./)).toBeInTheDocument()
    // the first-time question asked for words and derived the name
    expect(api).toHaveBeenCalledWith('/projects', expect.objectContaining({ method: 'POST', body: expect.objectContaining({ name: 'Cotizador de seguros', description: 'Cotizador de seguros' }) }))
  })

  it('who cannot create sees no first-time question', async () => {
    routes([], [])
    mount([])
    expect(await screen.findByText('Todavía no hay apps')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: '¿Qué quieres crear?' })).toBeNull()
    expect(screen.queryByRole('button', { name: /Crear app/ })).toBeNull()
  })
})
