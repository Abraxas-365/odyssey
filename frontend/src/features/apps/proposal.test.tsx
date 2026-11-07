import { QueryClient } from '@tanstack/react-query'
import { fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DetectedServices, Service } from '@/lib/types'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: (...a: unknown[]) => api(...a),
}))
const toast = vi.hoisted(() => ({ message: vi.fn(), error: vi.fn(), success: vi.fn() }))
vi.mock('sonner', () => ({ toast }))

import { offerOf, ProposedParts, saveProposal, type Proposal } from './services-panel'
import type { Draft } from './services'

const web: Draft = { name: 'web', kind: 'web', root: 'apps/web', route: '/', strip_prefix: false }
const apiPart: Draft = { name: 'api', kind: 'web', root: 'apps/api', route: '/api', strip_prefix: true }
const detected: DetectedServices = { items: [web, apiPart].map(d => ({ ...d, evidence: `${d.root}/package.json` })), notes: ['packages/ui es una librería: no se publica.'], truncated: false }
const plain: Service = { name: 'web', kind: 'web', root: '', route: '/', strip_prefix: false, updated_at: '2026-11-06T00:00:00Z' }

describe('detection before the first publication (MS-6b)', () => {
  beforeEach(() => { api.mockReset(); Object.values(toast).forEach(f => f.mockReset()) })

  it('offers parts only to an app that is still one piece', () => {
    expect(offerOf(detected, [])).toEqual([web, apiPart]) // a new app
    expect(offerOf(detected, [plain])).toEqual([web, apiPart])
    expect(offerOf(detected, undefined)).toBeUndefined() // not known yet: no guess
    expect(offerOf(detected, [{ ...plain, root: 'apps/web' }, { ...plain, name: 'api', route: '/api' }])).toBeUndefined() // already split: the platform's list wins
  })

  it('draws nothing for a proposal of one part (D3)', () => {
    const { container } = render(<ProposedParts proposal={{ notes: [], loading: false, error: null }} use onUseChange={() => {}} technical={false} />)
    expect(container.innerHTML).toBe('')
  })

  it('shows the parts found and lets the person publish it as one piece', () => {
    const onUse = vi.fn()
    const proposal: Proposal = { rows: [web, apiPart], notes: detected.notes, loading: false, error: null }
    render(<ProposedParts proposal={proposal} use onUseChange={onUse} technical={false} />)
    expect(screen.getByText('Encontramos 2 partes en tu código')).toBeTruthy()
    expect(screen.getByText('En /api (recibe la ruta sin ese prefijo)')).toBeTruthy()
    expect(screen.getByText(detected.notes[0])).toBeTruthy()
    fireEvent.click(screen.getByRole('checkbox'))
    expect(onUse).toHaveBeenCalledWith(false)
  })

  it('saves the parts, and stops the publication when IT holds them or it fails', async () => {
    const qc = new QueryClient()
    api.mockResolvedValueOnce({ items: [] })
    expect(await saveProposal(qc, 'p', [web, apiPart])).toBe(true)
    expect(api).toHaveBeenCalledWith('/projects/p/services', { method: 'PUT', body: { items: [web, apiPart] } })

    api.mockResolvedValueOnce({ status: 'pending_approval', approval_id: 'a' })
    expect(await saveProposal(qc, 'p', [web, apiPart])).toBe(false)
    expect(toast.message).toHaveBeenCalledWith(expect.stringMatching(/Publica cuando lo aprueben/))

    api.mockRejectedValueOnce(new Error('boom'))
    expect(await saveProposal(qc, 'p', [web, apiPart], 'La app se creó. ')).toBe(false)
    expect(toast.error).toHaveBeenCalledWith(expect.stringMatching(/^La app se creó\. No pudimos guardar las partes/))
  })
})
