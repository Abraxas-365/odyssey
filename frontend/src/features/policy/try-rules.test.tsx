import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { catalogEntry } from '@/lib/catalog.fixture'
import { defaultScenario } from './rules'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))

import { TryRules } from './try-rules'

// IT withdrew postgres-produccion; it pre-approves up to 10 GB of the basic one
const basic = catalogEntry({}, { preapproved: { storage_gi: { max: 10 } } })
const withdrawn = catalogEntry({ recipe: 'postgres-produccion', title: 'PostgreSQL de producción' }, { active: false, tiers: [], defaults: [] })

function mount(initial = defaultScenario) {
  api.mockImplementation((path: string) =>
    path === '/catalog/recipes'
      ? Promise.resolve({ items: [basic, withdrawn] })
      : Promise.resolve({ effect: 'allow', rules: [], approvers: [], results: [] }),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><TryRules initial={initial} /></QueryClientProvider>)
}
const evaluated = () => api.mock.calls.filter(c => c[0] === '/policy/evaluate').map(c => c[1].body)

describe('TryRules with the catalog', () => {
  beforeEach(() => { api.mockReset() })

  it('tries a create with the recipe the tier gets and says the catalog asks beyond its range', async () => {
    mount({ ...defaultScenario, action: 'create_resource' })
    const size = await screen.findByLabelText('Tamaño (GB)')
    fireEvent.change(size, { target: { value: '20' } })
    await userEvent.click(screen.getByRole('button', { name: /Probar/ }))
    await waitFor(() => expect(evaluated()).toHaveLength(1))
    expect(evaluated()[0].resource).toEqual({ kind: 'postgres', recipe: 'postgres-basico', recipe_version: 1, engine: 'builtin', params: { storage_gi: 20 }, out_of_range: ['storage_gi'] })
    expect(await screen.findByText(/pide aprobación aunque ninguna regla lo diga/)).toBeTruthy()
  })

  it('tries a change on a recipe IT no longer offers (what exists keeps changing)', async () => {
    mount({ ...defaultScenario, action: 'change_resource', recipe: 'postgres-produccion', size: 5 })
    expect(await screen.findByLabelText('Tamaño nuevo (GB)')).toBeTruthy()
    expect(screen.getByText(/solo puede crecer/)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: /Probar/ }))
    await waitFor(() => expect(evaluated()).toHaveLength(1))
    expect(evaluated()[0].resource).toMatchObject({ recipe: 'postgres-produccion', params: { storage_gi: 5 }, out_of_range: [] })
    expect(screen.queryByText(/pide aprobación aunque/)).toBeNull()
  })

  it('shows an empty size field instead of NaN when it is cleared', async () => {
    mount({ ...defaultScenario, action: 'create_resource' })
    const size = (await screen.findByLabelText('Tamaño (GB)')) as HTMLInputElement
    fireEvent.change(size, { target: { value: '' } })
    expect(size.value).toBe('')
  })
})
