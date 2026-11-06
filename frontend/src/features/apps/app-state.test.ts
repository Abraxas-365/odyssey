import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { AppState } from '@/lib/types'
import { cardAudience, cardHost, cardStatus } from './app-state'

const state = (over: Partial<AppState>): AppState => ({
  app_id: 'a', status: 'online', url: '', since: null, access: null, cluster: null, unavailable: [], ...over,
})

describe('cardStatus', () => {
  it('says what the app page says', () => {
    expect(cardStatus(state({ status: 'online' }), false)).toEqual({ status: 'live' })
    expect(cardStatus(state({ status: 'deploying' }), false)).toEqual({ status: 'building' })
    expect(cardStatus(state({ status: 'waiting_approval' }), false)?.status).toBe('waiting')
    expect(cardStatus(state({ status: 'failed' }), false)).toEqual({ status: 'attention', label: 'No se publicó' })
    expect(cardStatus(state({ status: 'not_deployed' }), false)).toEqual({ status: 'paused', label: 'Sin publicar' })
    expect(cardStatus(state({ status: 'archived' }), false)).toEqual({ status: 'archived' })
  })
  it('archived wins; unknown claims nothing', () => {
    expect(cardStatus(undefined, true)).toEqual({ status: 'archived' })
    expect(cardStatus(undefined, false)).toBeUndefined()
  })
})

describe('cardAudience', () => {
  it('only from what the API read', () => {
    expect(cardAudience(undefined)).toBeUndefined()
    expect(cardAudience(state({ access: null }))).toBeUndefined()
    expect(cardAudience(state({ access: { mode: 'organization', pending: null } }))).toBe('Toda la empresa')
    expect(cardAudience(state({ access: { mode: 'team', pending: 'public' } }))).toBe('Solo tu equipo · cambio pendiente')
  })
})

describe('cardHost', () => {
  it('the host, or nothing', () => {
    expect(cardHost(state({ url: 'https://visitas.apps.acme.com/' }))).toBe('visitas.apps.acme.com')
    expect(cardHost(state({ url: '' }))).toBeUndefined()
    expect(cardHost(state({ url: 'not a url' }))).toBeUndefined()
  })
})

// UX v4 rule: no screen shows who can open an app unless the API said so. An
// audience written as a literal outside the design system's samples is a
// claim nobody read (D2: "Solo tu equipo" on every card).
describe('no invented audience', () => {
  const src = resolve(__dirname, '../..')
  const files = (dir: string): string[] =>
    readdirSync(dir).flatMap(n => {
      const p = join(dir, n)
      return statSync(p).isDirectory() ? files(p) : /\.tsx?$/.test(n) && !/\.test\.tsx?$/.test(n) ? [p] : []
    })
  it('audience comes from data, never a literal', () => {
    const offenders = files(src)
      .filter(f => !f.endsWith('design-system.tsx'))
      .filter(f => /audience:\s*['"`]/.test(readFileSync(f, 'utf8')))
    expect(offenders).toEqual([])
  })
})
