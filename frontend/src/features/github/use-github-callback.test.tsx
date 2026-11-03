import { StrictMode, type ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('sonner', () => ({ toast }))

import { useGitHubCallback } from './use-github-callback'

function Probe() {
  const cb = useGitHubCallback()
  const loc = useLocation()
  return <p data-testid="out">{`${loc.pathname}${loc.search}|${cb.claiming}|${cb.errorCode ?? ''}|${cb.requested}`}</p>
}

function mount(url: string, children: ReactNode = <Probe />) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(
    <StrictMode>
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={[url]}>
          <Routes>
            <Route path="/configuracion/github" element={children} />
            <Route path="/apps/:id" element={children} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </StrictMode>,
  )
}

describe('useGitHubCallback', () => {
  beforeEach(() => { api.mockReset(); toast.success.mockReset() })

  it('posts the claim exactly once (even under StrictMode), toasts and goes back', async () => {
    api.mockResolvedValue({ installation: { account: 'acme' }, return_to: '/apps/7' })
    mount('/configuracion/github?github_claim=tok')
    await waitFor(() => expect(screen.getByTestId('out').textContent).toMatch(/^\/apps\/7/))
    expect(api).toHaveBeenCalledTimes(1)
    expect(api).toHaveBeenCalledWith('/github/installations', { method: 'POST', body: { claim: 'tok' } })
    expect(toast.success).toHaveBeenCalledTimes(1)
  })

  it('removes the claim from the URL and keeps the error to show', async () => {
    api.mockRejectedValue(new Error('x'))
    mount('/configuracion/github?github_claim=tok')
    await waitFor(() => expect(api).toHaveBeenCalled())
    await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('/configuracion/github|false||false'))
    expect(api).toHaveBeenCalledTimes(1)
  })

  it('reads the error and the requested approval without posting', () => {
    api.mockReset()
    mount('/configuracion/github?github_error=GITHUB_NOT_YOURS&github=requested')
    expect(screen.getByTestId('out').textContent).toBe('/configuracion/github?github_error=GITHUB_NOT_YOURS&github=requested|false|GITHUB_NOT_YOURS|true')
    expect(api).not.toHaveBeenCalled()
  })
})
