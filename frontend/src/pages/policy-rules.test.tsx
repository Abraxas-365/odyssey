import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, type Auth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import type { PolicyRule } from '@/lib/types'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))

import { PolicyRulesPage } from './policy-rules'

const mine: PolicyRule = {
  id: 'r1', name: 'Mi regla', description: '', expression: "action == 'deploy'", effect: 'deny', approvers: 'odyssey:approvals:decide',
  enabled: true, builtin: null, version: 1, created_by: '', updated_by: '', created_at: '', updated_at: '',
}

function mount(can: boolean) {
  const auth: Auth = { can: p => can && p === permissions.policyManage, user: { id: 'u1' }, logout: () => {}, switchOrganization: () => {} }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider value={auth}>
        <MemoryRouter>
          <PolicyRulesPage />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

describe('PolicyRulesPage', () => {
  beforeEach(() => api.mockReset())

  it('says only IT can see the rules, without asking the API (it would be a 403)', () => {
    mount(false)
    expect(screen.getByText('Solo TI puede ver las reglas')).toBeTruthy()
    expect(api).not.toHaveBeenCalled()
  })

  it('tries the condition being typed in the new-rule dialog, without saving it', async () => {
    api.mockImplementation((path: string) =>
      path === '/policy/evaluate'
        ? Promise.resolve({ effect: 'deny', rules: [{ id: 'x', name: 'Regla de prueba' }], approvers: [], results: [{ rule: { id: 'x', name: 'Regla de prueba' }, effect: 'deny', enabled: true, matched: true }] })
        : Promise.resolve({ items: [mine] }),
    )
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: /Nueva regla/ }))
    expect(screen.getByText('Qué puede leer la condición')).toBeTruthy()
    await userEvent.type(screen.getByLabelText('Nombre'), 'Algo')
    await userEvent.click(screen.getByRole('button', { name: 'Probar' }))
    await waitFor(() => expect(screen.getByText(/Aplica: bloquea/)).toBeTruthy())
    const call = api.mock.calls.find(c => c[0] === '/policy/evaluate')
    expect(call?.[1].body.rule).toMatchObject({ name: 'Algo', effect: 'approval', expression: "action == 'deploy' && project.tier == 'production'" })
    expect(api.mock.calls.some(c => c[1]?.method === 'POST' && c[0] === '/policy/rules')).toBe(false)
  })
})
