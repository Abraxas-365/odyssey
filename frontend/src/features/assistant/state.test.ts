import { describe, expect, it } from 'vitest'
import { knownMe } from '@/lib/me'
import type { Me } from '@/lib/types'
import { assistantState, chipKind, chipLabel, offered, readOnly } from './state'

const now = Date.parse('2026-11-06T10:00:00Z')
const me = (assistant: Me['assistant'], permissions = ['odyssey:projects:read', 'odyssey:deployments:read']): Me => ({
  user_id: 'u', organization_id: 'o', role: 'Lector', permissions, assistant,
})
const claude = { id: 'c1', key: 'claude_code' as const, name: 'Claude Code', consented_at: '2026-11-04T00:00:00Z', last_used_at: '2026-11-06T09:55:00Z' }

describe('assistantState', () => {
  it('says nothing when the installation offers no assistant, unless one is still connected', () => {
    expect(assistantState(me({ connected: [], can: [], cannot: [], tools: [] }), false, now)).toEqual({ kind: 'off' })
    expect(chipLabel({ kind: 'off' })).toBeUndefined()
    // TI stopped offering clients: what is connected still is, and can be disconnected
    expect(assistantState(me({ connected: [claude], can: [], cannot: [], tools: [] }), false, now).kind).toBe('connected')
    expect(offered(undefined)).toBeUndefined()
    expect(offered({ available: true, clients: [] })).toBe(false)
    expect(offered({ available: false, clients: [] })).toBe(false)
  })

  it('never turns "could not ask" into "not connected"', () => {
    expect(assistantState(undefined, true, now).kind).toBe('unknown')
    expect(assistantState(me(null), true, now).kind).toBe('unknown')
    expect(assistantState(undefined, false, now).kind).toBe('off')
    expect(chipLabel({ kind: 'unknown' })).toBe('Tu asistente')
    // quiet, not the call to action
    expect(chipKind({ kind: 'unknown' })).toBe('unknown')
  })

  it('invites to connect when none is', () => {
    const s = assistantState(me({ connected: [], can: [], cannot: [], tools: [] }), true, now)
    expect(s.kind).toBe('none')
    expect(chipLabel(s)).toBe('Conecta tu asistente')
    expect(chipKind(s)).toBe('connect')
  })

  it('names the one used last, when, and a reader\'s "solo lectura"', () => {
    const codex = { ...claude, id: 'c2', key: 'codex' as const, name: 'Codex', last_used_at: null }
    const s = assistantState(me({ connected: [claude, codex], can: [], cannot: [], tools: [] }), true, now)
    expect(chipLabel(s)).toBe('Claude Code +1 · hace 5 minutos · solo lectura')
    const builder = assistantState(me({ connected: [{ ...claude, last_used_at: null }], can: [], cannot: [], tools: [] }, ['odyssey:projects:read', 'odyssey:projects:write']), true, now)
    expect(chipLabel(builder)).toBe('Claude Code')
  })

  it('readOnly needs only read permissions, and some', () => {
    expect(readOnly([])).toBe(false)
    expect(readOnly(['odyssey:projects:read'])).toBe(true)
    expect(readOnly(['odyssey:projects:read', 'odyssey:deployments:create'])).toBe(false)
  })

  it('a /me whose last refetch failed is no longer known', () => {
    const old = me({ connected: [claude], can: [], cannot: [], tools: [] })
    expect(knownMe({ data: old, isError: false })).toBe(old)
    expect(assistantState(knownMe({ data: old, isError: true }), true, now).kind).toBe('unknown')
  })
})
