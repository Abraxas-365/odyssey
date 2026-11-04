import { describe, expect, it } from 'vitest'
import type { SecretView } from '@/lib/types'
import { canLoad, canRemove, envChanged, envDiff, loadLabel, looksSecret, rowsOf, rowsValid, secretStateText, variableNameProblem } from './variables'

const secret = (state: SecretView['state'], extra: Partial<SecretView> = {}): SecretView => ({ name: 'API_KEY', source: 'managed', state, next_version: 1, ...extra })

describe('variables', () => {
  it('sends only what changed', () => {
    const before = { TZ: 'UTC', LOG: 'info', OLD: 'x' }
    const rows = [{ name: 'TZ', value: 'UTC' }, { name: 'LOG', value: 'debug' }, { name: 'NEW', value: '1' }, { name: '', value: '' }]
    expect(envDiff(before, rows)).toEqual({ set: { LOG: 'debug', NEW: '1' }, unset: ['OLD'] })
    expect(envChanged(before, rowsOf(before))).toBe(false)
    expect(envChanged(undefined, [{ name: ' ', value: '' }])).toBe(false)
    // an empty value is a value
    expect(envDiff({ A: 'x' }, [{ name: 'A', value: '' }])).toEqual({ set: { A: '' }, unset: [] })
  })

  it('lists them by name', () => {
    expect(rowsOf({ b: '2', a: '1' }).map(r => r.name)).toEqual(['a', 'b'])
    expect(rowsOf(undefined)).toEqual([])
  })

  it('explains a bad name in plain words, like the API refuses it', () => {
    const rows = [{ name: 'A', value: '1' }, { name: 'A', value: '2' }]
    expect(variableNameProblem('MY-VAR', [], [])).toMatch(/letras/)
    expect(variableNameProblem('1A', [], [])).toMatch(/número/)
    expect(variableNameProblem('odyssey_x', [], [])).toMatch(/Odicea/)
    expect(variableNameProblem('A', rows, [])).toMatch(/repetido/)
    expect(variableNameProblem('DB', [], ['DB'])).toMatch(/secreto/)
    expect(variableNameProblem('TZ', [{ name: 'TZ', value: '' }], [])).toBeUndefined()
    expect(rowsValid(rows, [])).toBe(false)
    expect(rowsValid([{ name: '', value: 'orphan' }], [])).toBe(false)
    expect(rowsValid([{ name: 'A', value: 'x'.repeat(4097) }], [])).toBe(false)
    expect(rowsValid([{ name: 'A', value: 'ok' }, { name: '', value: '' }], [])).toBe(true)
  })

  it('flags names that look like a secret', () => {
    for (const n of ['DB_PASSWORD', 'stripe_api_key', 'GITHUB_TOKEN', 'CLIENT_SECRET']) expect(looksSecret(n), n).toBe(true)
    for (const n of ['TZ', 'LOG_LEVEL', 'PUBLIC_URL']) expect(looksSecret(n), n).toBe(false)
  })
})

describe('secrets', () => {
  it('words every state and never shows its id', () => {
    for (const [state, t] of Object.entries(secretStateText)) expect(`${t.label} ${t.help}`, state).not.toMatch(/\b(missing|loaded|reload|external|unavailable|failed)\b/)
  })

  it('lets a person load only a managed value the cluster can take', () => {
    expect(canLoad(secret('missing'), true)).toBe(true)
    expect(canLoad(secret('reload'), true)).toBe(true)
    expect(canLoad(secret('failed'), true)).toBe(true)
    expect(canLoad(secret('missing'), false)).toBe(false)
    expect(canLoad(secret('unavailable'), true)).toBe(false)
    expect(canLoad(secret('external', { source: 'external' }), true)).toBe(false)
  })

  it('leaves a vault secret to whoever may change the company vault ones', () => {
    expect(canRemove(secret('loaded'), false)).toBe(true)
    expect(canRemove(secret('external', { source: 'external' }), false)).toBe(false)
    expect(canRemove(secret('external', { source: 'external' }), true)).toBe(true)
  })

  it('says what loading will do', () => {
    expect(loadLabel(secret('missing'))).toBe('Cargar valor')
    expect(loadLabel(secret('loaded'))).toBe('Cambiar valor')
    expect(loadLabel(secret('reload'))).toBe('Volver a cargar')
    expect(loadLabel(secret('failed'))).toBe('Volver a cargar')
  })
})
