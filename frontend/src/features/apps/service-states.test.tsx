import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Release, ServiceState } from '@/lib/types'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: (...a: unknown[]) => api(...a),
}))

import { ServiceNames, ServiceStatesPanel } from './service-states'

type RS = Release['services'][number]
const svc = (name: string, over: Partial<RS> = {}): RS => ({ name, kind: 'web', root: `apps/${name}`, route: '/', strip_prefix: false, image: 'r/x@sha256:1', port: 8080, health_path: '/', replicas: 1, ...over })
const state = (name: string, over: Partial<ServiceState> = {}): ServiceState => ({ name, kind: 'web', route: '/', phase: 'ready', reason: '', message: '', ready_replicas: 1, replicas: 1, ...over })

function mount(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

describe('ServiceStatesPanel (MS-6)', () => {
  beforeEach(() => api.mockReset())

  it('draws nothing and asks nothing for an app of one part (D3)', () => {
    const { container } = mount(<ServiceStatesPanel project="p" env="e" release={{ services: [svc('web')] }} technical={false} />)
    expect(container.innerHTML).toBe('')
    expect(api).not.toHaveBeenCalled()
  })

  it('shows how each part of the version online stands', async () => {
    api.mockResolvedValue({ items: [state('web'), state('api', { route: '/api', phase: 'failed', reason: 'crash' }), state('jobs', { kind: 'worker', route: '' })] })
    mount(<ServiceStatesPanel project="p" env="e" release={{ services: [svc('web'), svc('api', { route: '/api' }), svc('jobs', { kind: 'worker', route: '' })] }} technical={false} />)
    expect(await screen.findByText('Se detiene al arrancar')).toBeTruthy()
    expect(screen.getByText('En línea')).toBeTruthy()
    expect(screen.getByText('Funcionando')).toBeTruthy()
    expect(api).toHaveBeenCalledWith('/projects/p/environments/e/services', expect.anything())
  })
})

describe('ServiceNames', () => {
  it('lists the parts of an app of several, nothing for one', () => {
    const { container, rerender } = render(<ServiceNames services={[{ name: 'web', kind: 'web', route: '/' }]} />)
    expect(container.innerHTML).toBe('')
    rerender(<ServiceNames services={[{ name: 'web', kind: 'web', route: '/' }, { name: 'api', kind: 'web', route: '/api' }]} />)
    expect(screen.getByText('web · api')).toBeTruthy()
  })
})
