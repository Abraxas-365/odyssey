import { describe, expect, it } from 'vitest'
import { connectBody } from '@/lib/resources'
import { asksScope, normalizeScope, sameScope, scopeField, scopeProblem, scopeText } from './scope'
import { envDiff, layerBody, layerEnv, overriddenBy } from './variables'

const two = [{ name: 'web' }, { name: 'api' }]

describe('scope (MS-6b)', () => {
  it('says who receives it in plain words', () => {
    expect(scopeText([])).toBe('Toda la app')
    expect(scopeText(undefined)).toBe('Toda la app')
    expect(scopeText(['jobs', 'api'])).toBe('Solo: api, jobs')
  })

  it('compares scopes without caring about order or repeats', () => {
    expect(sameScope(['b', 'a'], ['a', 'b'])).toBe(true)
    expect(sameScope([], undefined)).toBe(true)
    expect(sameScope(['a'], [])).toBe(false)
    expect(normalizeScope(['b', 'a', 'b'])).toEqual(['a', 'b'])
  })

  it('asks only for an app of several parts whose cluster can scope', () => {
    expect(asksScope(two, true)).toBe(true)
    expect(asksScope(two, false)).toBe(false)
    expect(asksScope([{ name: 'web' }], true)).toBe(false)
    expect(asksScope(undefined, true)).toBe(false)
  })

  it('sends services only when someone chose parts', () => {
    expect(scopeField([])).toEqual({})
    expect(scopeField(['jobs', 'api'])).toEqual({ services: ['api', 'jobs'] })
    expect(connectBody('DB', ['api'])).toEqual({ prefix: 'DB', services: ['api'] })
    expect(connectBody(undefined, [])).toEqual({})
    expect(connectBody()).toEqual({})
  })

  it('a files volume of an app of several parts needs exactly one', () => {
    const opts = { services: two, scoped: true, exactlyOne: true }
    expect(scopeProblem([], opts)).toMatch(/solo una/)
    expect(scopeProblem(['web', 'api'], opts)).toMatch(/solo una/)
    expect(scopeProblem(['api'], opts)).toBeUndefined()
    // one part, or a cluster that cannot scope: nothing to choose, the API answers
    expect(scopeProblem([], { ...opts, services: [{ name: 'web' }] })).toBeUndefined()
    expect(scopeProblem([], { ...opts, scoped: false })).toBeUndefined()
    expect(scopeProblem([], { services: two, scoped: true })).toBeUndefined()
  })
})

describe('variables by layer (MS-6b)', () => {
  const settings = { env: { TZ: 'UTC', LOG: 'info' }, service_env: { api: { LOG: 'debug', DB: 'x' }, jobs: { LOG: 'warn' } } }

  it("starts from the part's own variables, not the whole app's", () => {
    expect(layerEnv(settings, '')).toEqual({ TZ: 'UTC', LOG: 'info' })
    expect(layerEnv(settings, 'api')).toEqual({ LOG: 'debug', DB: 'x' })
    expect(layerEnv(settings, 'web')).toEqual({})
  })

  it('a part layer sends services; the whole app does not', () => {
    const diff = envDiff({ LOG: 'debug' }, [{ name: 'LOG', value: 'trace' }, { name: 'NEW', value: '1' }])
    expect(layerBody(diff, 'api')).toEqual({ set: { LOG: 'trace', NEW: '1' }, unset: [], services: ['api'] })
    expect(layerBody(diff, '')).toEqual({ set: { LOG: 'trace', NEW: '1' }, unset: [] })
    expect('services' in layerBody(diff, '')).toBe(false)
  })

  it('names the parts that override a whole-app variable', () => {
    expect(overriddenBy('LOG', settings.service_env)).toEqual(['api', 'jobs'])
    expect(overriddenBy('TZ', settings.service_env)).toEqual([])
    expect(overriddenBy('LOG', undefined)).toEqual([])
  })
})
