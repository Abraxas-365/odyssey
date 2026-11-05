import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, type Auth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import type { Approval } from '@/lib/types'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))

import { BandejaPage } from './bandeja'

const base: Approval = {
  id: 'a1', action: 'set_access', project_id: 'p', environment_id: 'e', summary: 'Hacer pública «Inventario»', payload: {},
  requested_by: 'u2', reason: 'Hacer pública una app requiere aprobación de TI', rules: [{ id: 'r', name: 'Pública con aprobación' }],
  approvers: [], status: 'pending', decided_by: null, decided_at: null, decision_note: '', expires_at: new Date(Date.now() + 5 * 864e5).toISOString(),
  consumed_at: null, created_at: new Date(Date.now() - 36e5).toISOString(), mine: false, can_decide: true,
}

const list = (items: Approval[]) => ({ items, page: { total: items.length, limit: 50, offset: 0 } })

function mount(decider: boolean, url = '/bandeja') {
  const auth: Auth = { can: p => decider && p === permissions.approvalsDecide, user: { id: 'u1' }, logout: () => {}, switchOrganization: () => {} }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider value={auth}>
        <MemoryRouter initialEntries={[url]}>
          <BandejaPage />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

describe('BandejaPage', () => {
  beforeEach(() => api.mockReset())

  it('shows the sentence, why and the rule, and approves with a note', async () => {
    api.mockImplementation((_path: string, init?: { method?: string }) => (init?.method === 'POST' ? Promise.resolve({ ...base, status: 'approved' }) : Promise.resolve(list([base]))))
    mount(true)
    expect(await screen.findByText('Hacer pública «Inventario»')).toBeTruthy()
    expect(screen.getByText(/Hacer pública una app requiere aprobación de TI/)).toBeTruthy()
    expect(screen.getByText(/Pública con aprobación/)).toBeTruthy()
    expect(api.mock.calls[0][0]).toBe('/approvals?status=pending&decidable=true')

    await userEvent.click(screen.getByRole('button', { name: /Aprobar/ }))
    await userEvent.type(screen.getByLabelText(/Nota/), 'Revisado')
    await userEvent.click(screen.getAllByRole('button', { name: 'Aprobar' }).at(-1)!)
    await waitFor(() => expect(api.mock.calls.some(c => c[0] === '/approvals/a1/approve')).toBe(true))
    expect(api.mock.calls.find(c => c[0] === '/approvals/a1/approve')?.[1]).toMatchObject({ method: 'POST', body: { note: 'Revisado' } })
  })

  it('offers no decision on my own request, only Cancelar, and asks only for mine when I cannot decide', async () => {
    api.mockImplementation((path: string) => Promise.resolve(list(String(path).includes('mine=true') ? [{ ...base, mine: true, can_decide: false }] : [])))
    mount(false)
    expect(await screen.findByText('Hacer pública «Inventario»')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Aprobar/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Rechazar/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Cancelar' })).toBeTruthy()
    expect(screen.queryByRole('radio', { name: 'Para decidir' })).toBeNull()
    expect(api.mock.calls.every(c => String(c[0]).includes('mine=true'))).toBe(true)
  })

  it('says calmly when there is nothing to decide', async () => {
    api.mockResolvedValue(list([]))
    mount(true)
    expect(await screen.findByText('Nada esperando')).toBeTruthy()
  })

  it('opens the request of a /bandeja?id= link above the lists, even when it is not in them', async () => {
    api.mockImplementation((path: string) => Promise.resolve(path === '/approvals/a9' ? { ...base, id: 'a9', summary: 'Activar la IA de «Pagos»' } : list([])))
    mount(true, '/bandeja?id=a9')
    expect(await screen.findByText('Activar la IA de «Pagos»')).toBeTruthy()
    expect(screen.getByRole('button', { name: /Aprobar/ })).toBeTruthy()
    expect(api.mock.calls.some(c => c[0] === '/approvals/a9')).toBe(true)
  })

  it('says so when the linked request is not theirs', async () => {
    api.mockImplementation((path: string) => (path === '/approvals/nope' ? Promise.reject(new Error('404')) : Promise.resolve(list([]))))
    mount(true, '/bandeja?id=nope')
    expect(await screen.findByText('No encontramos esa solicitud')).toBeTruthy()
  })
})
