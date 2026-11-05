import { describe, expect, it } from 'vitest'
import type { EnvironmentUses } from '@/lib/types'
import { USES_POLL_MAX_MS, USES_POLL_MIN_MS, USES_POLL_WINDOW_MS, usesChanging, usesPollDelay } from '@/lib/uses'
import { alertsOf, attentionAnchor, attentionTone, byWeight, egressText, secretsText, unavailableText, usesRows } from './uses'

const base = (patch: Partial<EnvironmentUses> = {}): EnvironmentUses => ({
  access: { mode: 'team', pending: false, failed: false },
  identity: { verifiable: true },
  resources: [],
  secrets: { total: 0, missing: [], external: 0, unavailable: 0 },
  variables: 0,
  ai: { available: true, enabled: false, status: 'off' },
  egress: { mode: 'internet', state: 'applied', enforced: true },
  unavailable: [],
  attention: [],
  ...patch,
})

const row = (u: EnvironmentUses, key: string) => usesRows(u).find(r => r.key === key)!

describe('uses rows', () => {
  it('lists access, identity, data, secrets, AI and internet in that order', () => {
    expect(usesRows(base()).map(r => r.area)).toEqual(['access', 'identity', 'data', 'secrets', 'ai', 'egress'])
  })

  it('says who can open it in plain words, and that a change is on its way or did not apply', () => {
    expect(row(base(), 'access')).toMatchObject({ text: 'Solo tu equipo', tone: 'ok', anchor: 'acceso' })
    expect(row(base({ access: { mode: 'public', pending: false, failed: false }, identity: { verifiable: false } }), 'identity')).toMatchObject({ text: 'La app no sabe quién la abre', tone: 'neutral' })
    expect(row(base({ access: { mode: 'organization', pending: true, failed: false } }), 'access')).toMatchObject({ text: 'Toda la empresa · aplicándose', tone: 'info' })
    expect(row(base({ access: { mode: 'organization', pending: true, failed: true } }), 'access')).toMatchObject({ text: 'Toda la empresa · sin aplicar', tone: 'warn' })
  })

  it('has one row per resource with its own state, and the variable names only as technical detail', () => {
    const u = base({
      resources: [
        { name: 'db', kind: 'postgres', prefix: '', status: 'ready', variables: ['DATABASE_URL', 'PGHOST'] },
        { name: 'cache', kind: 'redis', prefix: '', status: 'failed', detail: 'sin memoria', variables: ['REDIS_URL'] },
        { name: 'files', kind: 'files', prefix: '', status: 'preparing', variables: ['FILES_DIR'] },
        { name: 'old', kind: 'redis', prefix: '', status: 'unknown', variables: [] },
      ],
    })
    expect(row(u, 'data:db')).toMatchObject({ label: 'Base de datos · db', text: 'Lista', tone: 'ok', anchor: 'datos' })
    expect(row(u, 'data:db').technical).toContain('DATABASE_URL')
    expect(row(u, 'data:cache')).toMatchObject({ text: 'Falló', tone: 'bad' })
    expect(row(u, 'data:cache').technical).toContain('sin memoria')
    expect(row(u, 'data:files')).toMatchObject({ text: 'Preparando…', tone: 'info' })
    expect(row(u, 'data:old')).toMatchObject({ text: 'No se pudo leer ahora', tone: 'neutral' })
    // the plain label never carries the engine or the variables
    expect(row(u, 'data:db').label).not.toMatch(/PostgreSQL|DATABASE_URL/)
  })

  it('says there is no data when the app uses none', () => {
    expect(row(base(), 'data')).toMatchObject({ text: 'Sin base de datos ni archivos', tone: 'neutral' })
  })

  it('counts the secrets still to load, and those that cannot reach the app', () => {
    const s = (p: Partial<NonNullable<EnvironmentUses['secrets']>>) => ({ total: 0, missing: [], external: 0, unavailable: 0, ...p })
    expect(secretsText(s({ total: 3, missing: ['A', 'B'] }))).toEqual({ text: '2 por cargar', tone: 'warn' })
    expect(secretsText(s({ total: 1, missing: ['A'] })).text).toBe('1 por cargar')
    expect(secretsText(s({}))).toEqual({ text: 'Sin secretos', tone: 'neutral' })
    expect(secretsText(s({ total: 2 }))).toEqual({ text: '2 cargados', tone: 'ok' })
    expect(secretsText(s({ total: 2, external: 2 })).text).toBe('2 en la bóveda de la empresa')
    expect(secretsText(s({ total: 2, unavailable: 2 }))).toEqual({ text: '2 sin llegar a la app', tone: 'warn' })
    expect(row(base({ secrets: s({ total: 2, missing: ['STRIPE_API_KEY'] }) }), 'secrets')).toMatchObject({ technical: 'Faltan: STRIPE_API_KEY', anchor: 'secretos' })
  })

  it('shows the AI state with the same words as its panel', () => {
    expect(row(base(), 'ai')).toMatchObject({ text: 'Sin activar', tone: 'neutral', anchor: 'ia' })
    expect(row(base({ ai: { available: true, enabled: true, status: 'enabled' } }), 'ai')).toMatchObject({ text: 'Activa', tone: 'ok' })
    expect(row(base({ ai: { available: true, enabled: false, status: 'revoke_failed' } }), 'ai')).toMatchObject({ text: 'Llave por revocar', tone: 'warn' })
    expect(row(base({ ai: { available: false, enabled: false, status: 'off' } }), 'ai').tone).toBe('neutral')
  })

  it('says whether the internet is open, limited and confirmed, or limited and not confirmed', () => {
    expect(egressText({ mode: 'internet', state: 'unsupported', enforced: false })).toEqual({ text: 'Sin límites', tone: 'ok' })
    expect(egressText({ mode: 'restricted', state: 'applied', enforced: true }).tone).toBe('ok')
    expect(egressText({ mode: 'restricted', state: 'pending', enforced: false })).toEqual({ text: 'Limitada, sin confirmar todavía', tone: 'warn' })
    expect(egressText({ mode: '', state: '', enforced: false }).text).toBe('Aún sin clúster')
    // TI's: nothing to change from the app
    expect(row(base(), 'egress').anchor).toBeUndefined()
  })

  it('shows a section the API could not read as such, never as fine', () => {
    const u = base({ secrets: null, ai: null, egress: null, unavailable: ['secrets', 'ai', 'egress'] })
    for (const k of ['secrets', 'ai', 'egress']) expect(row(u, k)).toMatchObject({ text: 'No se pudo leer ahora', tone: 'neutral' })
    expect(unavailableText(u)).toBe('No pudimos leer ahora: secretos, IA, salida a internet. Vuelve a cargar en un momento.')
    expect(unavailableText(base())).toBeUndefined()
  })
})

const kinds = [
  'secret_missing',
  'secrets_unavailable',
  'resource_not_ready',
  'resource_failed',
  'ai_error',
  'ai_pending',
  'config_pending',
  'config_failed',
  'egress_not_applied',
] as const

describe('attention', () => {
  it('has a weight and a place to solve it for every kind of the closed list', () => {
    for (const k of kinds) expect(attentionTone[k]).toBeDefined()
    expect(Object.keys(attentionTone).sort()).toEqual([...kinds].sort())
    expect(Object.keys(attentionAnchor).sort()).toEqual([...kinds].sort())
    expect(attentionAnchor.secret_missing).toBe('secretos')
    expect(attentionAnchor.config_failed).toBe('acceso')
    // IT's: nowhere in the console to solve it
    expect(attentionAnchor.egress_not_applied).toBeUndefined()
    expect(attentionAnchor.secrets_unavailable).toBeUndefined()
  })

  it('puts the worst first', () => {
    const items = [{ tone: 'info' as const, n: 1 }, { tone: 'bad' as const, n: 2 }, { tone: 'warn' as const, n: 3 }]
    expect(byWeight(items).map(i => i.n)).toEqual([2, 3, 1])
  })

  it('alerts only on what needs a person: what is in progress is a row state, not an alert', () => {
    const u = base({
      attention: [
        { kind: 'resource_not_ready', who: 'wait', message: 'Se está preparando.' },
        { kind: 'egress_not_applied', who: 'it', message: 'El clúster no puede aplicar la política de salida.' },
        { kind: 'secret_missing', who: 'owner', message: 'Falta cargar 1 secreto.' },
        { kind: 'config_pending', who: 'wait', message: 'Aplicando los últimos cambios de configuración.' },
      ],
    })
    expect(alertsOf(u).map(a => a.kind)).toEqual(['egress_not_applied', 'secret_missing']) // same weight: the API's order stays
    expect(alertsOf(base({ attention: [{ kind: 'ai_pending', who: 'wait', message: 'Activando.' }] }))).toEqual([])
  })
})

describe('polling', () => {
  it('asks again only while the app works on something', () => {
    expect(usesChanging(undefined)).toBe(false)
    expect(usesChanging(base())).toBe(false)
    expect(usesChanging(base({ access: { mode: 'team', pending: true, failed: false } }))).toBe(true)
    expect(usesChanging(base({ ai: { available: true, enabled: false, status: 'enabling' } }))).toBe(true)
    expect(usesChanging(base({ resources: [{ name: 'db', kind: 'postgres', prefix: '', status: 'preparing', variables: [] }] }))).toBe(true)
    expect(usesChanging(base({ resources: [{ name: 'db', kind: 'postgres', prefix: '', status: 'failed', variables: [] }] }))).toBe(false)
  })

  it('never polls for what waits for a person or IT', () => {
    expect(usesChanging(base({ egress: { mode: 'restricted', state: 'pending', enforced: false } }))).toBe(false)
    // a change that failed to apply stays failed until it is saved again
    expect(usesChanging(base({ access: { mode: 'team', pending: true, failed: true } }))).toBe(false)
    expect(usesChanging(base({ ai: null, secrets: null, egress: null, unavailable: ['ai', 'secrets', 'egress'] }))).toBe(false)
  })

  it('backs off from 3 s to 30 s and stops after 5 minutes', () => {
    expect(usesPollDelay(0)).toBe(USES_POLL_MIN_MS)
    expect(usesPollDelay(29_000)).toBe(3000)
    expect(usesPollDelay(30_000)).toBe(6000)
    expect(usesPollDelay(60_000)).toBe(12_000)
    expect(usesPollDelay(90_000)).toBe(24_000)
    expect(usesPollDelay(120_000)).toBe(USES_POLL_MAX_MS)
    expect(usesPollDelay(USES_POLL_WINDOW_MS - 1)).toBe(USES_POLL_MAX_MS)
    expect(usesPollDelay(USES_POLL_WINDOW_MS)).toBe(false)
    expect(usesPollDelay(-1)).toBe(false)
  })
})
