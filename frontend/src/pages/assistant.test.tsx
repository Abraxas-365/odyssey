import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '@/lib/preferences'
import type { Assistant, Me } from '@/lib/types'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))
const toastSuccess = vi.fn()
vi.mock('sonner', async orig => ({ ...(await orig<typeof import('sonner')>()), toast: { success: (...a: unknown[]) => toastSuccess(...a), error: vi.fn() } }))

import { AssistantPage } from './assistant'

const skill = { url: 'https://odicea.test/skills/odicea.tar.gz', version: 'abc123', name: 'odicea' as const }
const data: Assistant = {
  available: true,
  mcp_url: 'https://odicea.test/mcp',
  skill,
  clients: [
    { key: 'claude_code', name: 'Claude Code', install: 'claude mcp add --transport http odicea https://odicea.test/mcp', skill_install: 'mkdir -p ~/.claude/skills && curl claude', notes: 'Escribe /mcp.' },
    { key: 'codex', name: 'Codex', install: 'codex mcp add odicea --url https://odicea.test/mcp', skill_install: 'mkdir -p ~/.agents/skills && curl codex', notes: 'codex mcp login odicea.' },
    { key: 'cursor', name: 'Cursor', install: '{"mcpServers":{}}', install_link: 'cursor://anysphere.cursor-deeplink/mcp/install?name=odicea&config=e30=', skill_install: 'curl cursor', notes: 'Con el botón.' },
  ],
}
const can = 'Ver tus apps, su estado y lo que necesita atención'
const cannot = 'Publicar versiones'
const lector = (assistant: Me['assistant']): Me => ({
  user_id: 'u', organization_id: 'o', role: 'Lector', permissions: ['odyssey:deployments:read', 'odyssey:projects:read'], assistant,
})
const claude = { id: 'c1', key: 'claude_code' as const, name: 'Claude Code', consented_at: '2026-11-04T00:00:00Z', last_used_at: new Date(Date.now() - 5 * 60_000).toISOString() }

function serve(assistant: Assistant, me: Me | Error) {
  api.mockImplementation((path: string, opts?: { method?: string }) => {
    if (path === '/assistant') return Promise.resolve(assistant)
    if (path === '/me') return me instanceof Error ? Promise.reject(me) : Promise.resolve(me)
    if (opts?.method === 'DELETE') return Promise.resolve(undefined)
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <PreferencesProvider user="u">
        <MemoryRouter>
          <AssistantPage />
        </MemoryRouter>
      </PreferencesProvider>
    </QueryClientProvider>,
  )
}

describe('AssistantPage', () => {
  beforeEach(() => { api.mockReset(); toastSuccess.mockReset(); localStorage.clear() })

  it('says IT has not enabled the assistant, and shows no commands', async () => {
    serve({ ...data, available: false, clients: [] }, lector(null))
    mount()
    expect(await screen.findByText(/Tu equipo de TI todavía no habilitó el asistente/)).toBeTruthy()
    expect(screen.queryByText(/mcp add/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Copiar' })).toBeNull()
  })

  it('shows the commands of the chosen assistant and changes them when switching', async () => {
    serve(data, lector({ connected: [], can: [can], cannot: [cannot], tools: ['list_apps'] }))
    mount()
    expect(await screen.findByText(data.clients[0].install)).toBeTruthy()
    expect(screen.getByText(data.clients[0].skill_install)).toBeTruthy()
    expect(screen.queryByText(data.clients[1].install)).toBeNull()
    // no one-click install for a client without a link
    expect(screen.queryByRole('link', { name: /Agregar a/ })).toBeNull()

    await userEvent.click(screen.getByRole('radio', { name: /Codex/ }))
    expect(await screen.findByText(data.clients[1].install)).toBeTruthy()
    expect(screen.getByText(data.clients[1].skill_install)).toBeTruthy()
    expect(screen.queryByText(data.clients[0].install)).toBeNull()
  })

  it('offers the one-click link where the client has one, with the command still visible', async () => {
    serve(data, lector({ connected: [], can: [can], cannot: [], tools: [] }))
    mount()
    await userEvent.click(await screen.findByRole('radio', { name: /Cursor/ }))
    expect(screen.getByRole('link', { name: 'Agregar a Cursor' })).toHaveAttribute('href', data.clients[2].install_link)
    expect(screen.getByText(data.clients[2].install)).toBeTruthy()
  })

  it('"Otro" gives the server address and the skill download', async () => {
    serve(data, lector({ connected: [], can: [], cannot: [], tools: [] }))
    mount()
    await userEvent.click(await screen.findByRole('radio', { name: /Otro/ }))
    expect(screen.getAllByText(data.mcp_url).length).toBeGreaterThan(0)
    expect(screen.getByRole('link', { name: 'Descargar la skill' })).toHaveAttribute('href', skill.url)
  })

  it('says what is connected and what the assistant can and cannot do with the role', async () => {
    serve(data, lector({ connected: [claude], can: [can], cannot: [cannot], tools: ['get_app', 'list_apps'] }))
    mount()
    expect(await screen.findByText('Conectado')).toBeTruthy()
    expect(screen.getByText(/Lo usaste hace 5 minutos/)).toBeTruthy()
    expect(screen.getByText('Con tu rol (Lector), tu asistente…')).toBeTruthy()
    expect(screen.getByText(can)).toBeTruthy()
    expect(screen.getByText(cannot)).toBeTruthy()
    // the MCP tool names are for technical mode only
    expect(screen.queryByRole('list', { name: 'Herramientas MCP' })).toBeNull()
  })

  it('lists the MCP tools in technical mode', async () => {
    localStorage.setItem('odyssey:prefs:u:technical', '1')
    serve(data, lector({ connected: [], can: [can], cannot: [], tools: ['get_app', 'list_apps'] }))
    mount()
    const tools = await screen.findByRole('list', { name: 'Herramientas MCP' })
    expect(within(tools).getAllByRole('listitem').map(li => li.textContent)).toEqual(['get_app', 'list_apps'])
  })

  it('disconnects after confirming, and says so even when it was the last one', async () => {
    let connected = [claude]
    api.mockImplementation((path: string, opts?: { method?: string }) => {
      if (path === '/assistant') return Promise.resolve(data)
      if (path === '/me') return Promise.resolve(lector({ connected, can: [can], cannot: [], tools: [] }))
      if (opts?.method === 'DELETE') {
        connected = []
        return Promise.resolve(undefined)
      }
      return Promise.reject(new Error(`unexpected ${path}`))
    })
    mount()
    await userEvent.click(await screen.findByRole('button', { name: 'Desconectar' }))
    const dialog = await screen.findByRole('alertdialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Desconectar' }))
    await waitFor(() => expect(api).toHaveBeenCalledWith('/me/assistants/c1', { method: 'DELETE' }))
    // the panel it was opened from is gone, the dialog closes and the toast is said
    expect(await screen.findByText('Todavía no conectaste tu asistente.')).toBeTruthy()
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(toastSuccess).toHaveBeenCalledWith('Claude Code desconectado')
  })

  it('still shows (and lets disconnect) what is connected when IT stopped offering assistants', async () => {
    serve({ ...data, clients: [] }, lector({ connected: [claude], can: [can], cannot: [], tools: [] }))
    mount()
    expect(await screen.findByText(/Tu equipo de TI todavía no habilitó el asistente/)).toBeTruthy()
    expect(await screen.findByRole('button', { name: 'Desconectar' })).toBeTruthy()
    expect(screen.queryByText(/mcp add/)).toBeNull()
  })

  it('never says "not connected" when it could not ask', async () => {
    serve(data, lector(null))
    mount()
    expect(await screen.findByText('No pudimos ver si tu asistente está conectado.')).toBeTruthy()
    expect(screen.queryByText(/Todavía no conectaste/)).toBeNull()
    expect(screen.queryByText(/tu asistente podrá|tu asistente…/)).toBeNull()
  })
})
