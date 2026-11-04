import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Assistant } from '@/lib/types'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))

import { AssistantPage } from './assistant'

const skill = { url: 'https://odicea.test/skills/odicea.tar.gz', version: 'abc123', name: 'odicea' as const }
const data: Assistant = {
  available: true,
  mcp_url: 'https://odicea.test/mcp',
  skill,
  clients: [
    { key: 'claude_code', name: 'Claude Code', install: 'claude mcp add --transport http odicea https://odicea.test/mcp', skill_install: 'mkdir -p ~/.claude/skills && curl claude', notes: 'Escribe /mcp.' },
    { key: 'codex', name: 'Codex', install: 'codex mcp add odicea --url https://odicea.test/mcp', skill_install: 'mkdir -p ~/.agents/skills && curl codex', notes: 'codex mcp login odicea.' },
  ],
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <AssistantPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('AssistantPage', () => {
  beforeEach(() => api.mockReset())

  it('says IT has not enabled the assistant, and shows no commands', async () => {
    api.mockResolvedValue({ ...data, available: false, clients: [] })
    mount()
    expect(await screen.findByText(/Tu equipo de TI todavía no habilitó el asistente/)).toBeTruthy()
    expect(screen.queryByText(/mcp add/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Copiar' })).toBeNull()
  })

  it('shows the commands of the chosen assistant and changes them when switching', async () => {
    api.mockResolvedValue(data)
    mount()
    expect(await screen.findByText(data.clients[0].install)).toBeTruthy()
    expect(screen.getByText(data.clients[0].skill_install)).toBeTruthy()
    expect(screen.queryByText(data.clients[1].install)).toBeNull()

    await userEvent.click(screen.getByRole('radio', { name: /Codex/ }))
    expect(await screen.findByText(data.clients[1].install)).toBeTruthy()
    expect(screen.getByText(data.clients[1].skill_install)).toBeTruthy()
    expect(screen.queryByText(data.clients[0].install)).toBeNull()
    expect(api).toHaveBeenCalledWith('/assistant', expect.anything())
  })
})
