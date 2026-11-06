import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, type Auth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import { PreferencesProvider } from '@/lib/preferences'
import type { AuditEntry, AuditSummary, AuditVerification } from '@/lib/types'
import { sealOf, weekSentence, whoOf } from '@/features/audit/audit'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: (...a: unknown[]) => api(...a),
}))

import { RegistroPage } from './registro'

const entry = (p: Partial<AuditEntry>): AuditEntry => ({
  id: 'e1', organization_id: 'o1', seq: 7, type: 'deployment.started', version: 1, actor_id: 'u2', client: 'mcp:x',
  subject: 'deployment/d1', summary: 'Publicó una versión de Visitas', payload: { project_id: 'a1' } as unknown as AuditEntry['payload'], correlation_id: 'req-abc',
  causation_id: '', occurred_at: new Date().toISOString(), recorded_at: new Date().toISOString(), prev_hash: 'p', hash: 'h1234567890abcdef',
  channel: 'assistant:claude_code', actor_name: 'Ana Pérez', ...p,
})
const summary = (p: Partial<AuditSummary>): AuditSummary => ({ from: new Date().toISOString(), events: 0, people: 0, deployments: 0, by_channel: [], ...p })
const verified = (p: Partial<AuditVerification>): AuditVerification => ({ valid: true, entries: 3, broken_at: null, verified_at: new Date().toISOString(), ...p })

function respond({ entries = [] as AuditEntry[], verify = verified({}), week = summary({}) } = {}) {
  api.mockImplementation((path: string) => {
    if (path.startsWith('/audit/verify')) return Promise.resolve(verify)
    if (path.startsWith('/audit/summary')) return Promise.resolve(week)
    if (path.startsWith('/audit/people')) return Promise.resolve({ items: [{ actor_id: 'u2', name: 'Ana Pérez', entries: 3, last_at: new Date().toISOString() }] })
    if (path.startsWith('/audit')) return Promise.resolve({ items: entries, page: { total: entries.length, limit: 25, offset: 0 } })
    if (path.startsWith('/projects')) return Promise.resolve({ items: [{ id: 'a1', name: 'Visitas' }], page: { total: 1, limit: 100, offset: 0 } })
    return Promise.reject(new Error('unexpected ' + path))
  })
}

function mount(can: string[] = [permissions.auditRead], technical = false, at = '/registro') {
  const auth: Auth = { can: p => can.includes(p), user: { id: 'u1' }, logout: () => {}, switchOrganization: () => {} }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider value={auth}>
        <PreferencesProvider user="u1" defaultTechnical={technical}>
          <MemoryRouter initialEntries={[at]}><RegistroPage /></MemoryRouter>
        </PreferencesProvider>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

describe('the company log words (UX v4 §5.5)', () => {
  it('sums the week by channel, naming only what happened', () => {
    expect(weekSentence(summary({}))).toBe('Esta semana no hubo actividad.')
    expect(weekSentence(summary({
      events: 40, people: 4, deployments: 12,
      by_channel: [{ channel: 'assistant:claude_code', count: 6 }, { channel: 'assistant:codex', count: 3 }, { channel: 'github', count: 3 }],
    }))).toBe('Esta semana: 12 publicaciones, 9 con un asistente, 3 desde GitHub · 4 personas.')
    expect(weekSentence(summary({ events: 2, people: 1, deployments: 1, by_channel: [{ channel: 'platform', count: 1 }] })))
      .toBe('Esta semana: 1 publicación, 1 automática · 1 persona.')
  })

  it('says who without ids, and GitHub or the platform when nobody pressed', () => {
    expect(whoOf({ actor_id: 'u1', actor_name: 'Yo', channel: 'console' }, 'u1')).toBe('Tú')
    expect(whoOf({ actor_id: 'u2', actor_name: 'Ana', channel: 'console' }, 'u1')).toBe('Ana')
    expect(whoOf({ actor_id: 'u2', actor_name: '', channel: 'console' }, 'u1')).toBe('Alguien de tu equipo')
    expect(whoOf({ actor_id: '', actor_name: '', channel: 'github' }, 'u1')).toBe('GitHub')
    expect(whoOf({ actor_id: '', actor_name: '', channel: 'platform' }, 'u1')).toBe('La plataforma')
  })

  it('seals only what /audit/verify said', () => {
    expect(sealOf(undefined, false).kind).toBe('checking')
    expect(sealOf(undefined, true).kind).toBe('unknown')
    expect(sealOf(verified({ entries: 0 }), false).kind).toBe('empty')
    expect(sealOf(verified({ valid: false, broken_at: 2 }), false)).toEqual({ kind: 'broken', at: 2, entries: 3 })
    const now = Date.parse('2026-11-06T10:00:00Z')
    expect(sealOf(verified({ verified_at: '2026-11-06T09:57:00Z' }), false, now)).toEqual({ kind: 'ok', text: 'Registro íntegro · verificado hace 3 minutos' })
  })
})

describe('RegistroPage', () => {
  beforeEach(() => {
    api.mockReset()
  })

  it('is TI only, and does not ask without the permission', async () => {
    respond()
    mount([])
    expect(await screen.findByText('Solo TI puede ver el registro de la empresa')).toBeTruthy()
    expect(api.mock.calls.filter(([p]) => String(p).startsWith('/audit'))).toEqual([])
  })

  it('shows the feed with who, the channel and the app, the week and the seal', async () => {
    respond({ entries: [entry({})], week: summary({ events: 1, people: 1, deployments: 1, by_channel: [{ channel: 'assistant:claude_code', count: 1 }] }) })
    mount()
    expect(await screen.findByText('Publicó una versión de Visitas')).toBeTruthy()
    expect(screen.getByText('Ana Pérez')).toBeTruthy()
    expect(screen.getByRole('img', { name: 'Canal: Claude Code' })).toBeTruthy()
    expect(await screen.findByText(/Registro íntegro · verificado/)).toBeTruthy()
    expect(screen.getByText('Esta semana: 1 publicación, 1 con un asistente · 1 persona.')).toBeTruthy()
    expect(await screen.findByRole('link', { name: 'Visitas' })).toBeTruthy()
    expect(screen.queryByText('#7')).toBeNull() // technical evidence only in technical mode
  })

  it('a broken chain is a red alert that says where and whom to tell', async () => {
    respond({ entries: [entry({})], verify: verified({ valid: false, broken_at: 42 }) })
    mount()
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('a partir de la entrada #42')
    expect(alert.textContent).toContain('seguridad de la información')
    expect(screen.queryByText(/Registro íntegro/)).toBeNull()
  })

  it('an empty log explains what will be recorded', async () => {
    respond({ verify: verified({ entries: 0 }) })
    mount()
    expect(await screen.findByText('Aún no hay nada registrado')).toBeTruthy()
    expect(screen.getByText(/versiones publicadas \(desde la consola, un asistente o GitHub\)/)).toBeTruthy()
  })

  it('reads the filters from the address and sends them to the API', async () => {
    respond({ entries: [entry({})] })
    mount(undefined, false, '/registro?canal=github&app=a1&persona=u2')
    await screen.findByText('Publicó una versión de Visitas')
    const list = api.mock.calls.map(([p]) => String(p)).find(p => p.startsWith('/audit?'))!
    expect(list).toContain('channel=github')
    expect(list).toContain('project_id=a1')
    expect(list).toContain('actor_id=u2')
    expect(screen.getByRole('button', { name: /Quitar filtros/ })).toBeTruthy()
  })

  it('technical mode shows the evidence and follows the whole chain', async () => {
    respond({ entries: [entry({})] })
    mount(undefined, true)
    expect(await screen.findByText('#7')).toBeTruthy()
    expect(screen.getByText('deployment.started')).toBeTruthy()
    expect(screen.getByText('req-abc')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: /Ver toda la cadena/ }))
    await waitFor(() => expect(api.mock.calls.map(([p]) => String(p)).some(p => p.includes('correlation_id=req-abc'))).toBe(true))
  })
})
