import { describe, expect, it } from 'vitest'
import { atLimit, deleteBlock, deleteConfirmed, failureOf, isActive, nameProblem, prefixProblem, sizeProblem, stateOf, suggestName } from './resources'

describe('resources', () => {
  it('says what a resource is doing in plain words', () => {
    expect(stateOf({ status: 'active', phase: 'provisioning', message: '' })).toMatchObject({ label: 'Preparando…', busy: true })
    expect(stateOf({ status: 'active', phase: 'ready', message: '' })).toMatchObject({ label: 'Lista', tone: 'success', busy: false })
    // the agent's English message never reaches the badge
    expect(stateOf({ status: 'active', phase: 'failed', message: 'storage class missing' })).toMatchObject({ label: 'Falló', tone: 'destructive' })
    expect(stateOf({ status: 'active', phase: 'failed', message: '' }).label).toBe('Falló')
    // deleting wins over whatever the cluster last said
    expect(stateOf({ status: 'deleting', phase: 'ready', message: '' })).toMatchObject({ label: 'Eliminando…', busy: true })
    expect(isActive({ status: 'deleting' })).toBe(false)
  })

  it('explains a failure in plain Spanish by kind and keeps the agent message as the technical detail', () => {
    const msg = 'waiting for the PersistentVolumeClaim: no storage class'
    expect(failureOf({ kind: 'postgres', status: 'active', phase: 'failed', message: msg })).toEqual({ text: 'No se pudo preparar la base de datos.', detail: msg })
    expect(failureOf({ kind: 'redis', status: 'active', phase: 'failed', message: '' })).toEqual({ text: 'No se pudo preparar la caché.', detail: undefined })
    expect(failureOf({ kind: 'files', status: 'active', phase: 'failed', message: msg })?.text).toBe('No se pudo preparar el espacio de archivos.')
    // no plain line carries the raw message
    for (const kind of ['postgres', 'redis', 'files'] as const) expect(failureOf({ kind, status: 'active', phase: 'failed', message: msg })?.text).not.toContain('storage')
    expect(failureOf({ kind: 'postgres', status: 'active', phase: 'ready', message: '' })).toBeUndefined()
    expect(failureOf({ kind: 'postgres', status: 'active', phase: 'provisioning', message: 'x' })).toBeUndefined()
    expect(failureOf({ kind: 'postgres', status: 'deleting', phase: 'failed', message: 'x' })).toBeUndefined()
  })

  it('checks names like the API', () => {
    expect(nameProblem('db', [])).toBeUndefined()
    expect(nameProblem('', [])).toBeUndefined()
    expect(nameProblem('Base', [])).toMatch(/minúsculas/)
    expect(nameProblem('1db', [])).toMatch(/letra/)
    expect(nameProblem('db-', [])).toMatch(/minúsculas/)
    expect(nameProblem('a'.repeat(31), [])).toMatch(/30/)
    expect(nameProblem('db', ['db'])).toMatch(/Ya hay/)
  })

  it('checks prefixes like the API', () => {
    expect(prefixProblem('')).toBeUndefined()
    expect(prefixProblem('ANALYTICS_2')).toBeUndefined()
    expect(prefixProblem('analytics')).toMatch(/MAYÚSCULAS/)
    expect(prefixProblem('A_')).toMatch(/MAYÚSCULAS/)
    expect(prefixProblem('A'.repeat(41))).toMatch(/40/)
  })

  it('sizes only grow and stay in range', () => {
    expect(sizeProblem(1)).toBeUndefined()
    expect(sizeProblem(100)).toBeUndefined()
    expect(sizeProblem(0)).toMatch(/1 a 100/)
    expect(sizeProblem(101)).toMatch(/1 a 100/)
    expect(sizeProblem(1.5)).toMatch(/1 a 100/)
    expect(sizeProblem(Number.NaN)).toMatch(/1 a 100/)
    expect(sizeProblem(5, 5)).toBeUndefined()
    expect(sizeProblem(4, 5)).toMatch(/crecer/)
  })

  it('suggests a free name per kind', () => {
    expect(suggestName('postgres', [])).toBe('db')
    expect(suggestName('postgres', ['db', 'db-2'])).toBe('db-3')
    expect(suggestName('redis', ['db'])).toBe('cache')
    expect(suggestName('files', [])).toBe('archivos')
  })

  it('confirms an erase only by the exact name and never while the app uses it', () => {
    expect(deleteConfirmed('db', 'db')).toBe(true)
    expect(deleteConfirmed(' db ', 'db')).toBe(true)
    expect(deleteConfirmed('DB', 'db')).toBe(false)
    expect(deleteConfirmed('', 'db')).toBe(false)
    expect(deleteBlock({ connected: true, status: 'active' })).toMatch(/Desconéctala/)
    expect(deleteBlock({ connected: false, status: 'deleting' })).toMatch(/eliminando/)
    expect(deleteBlock({ connected: false, status: 'active' })).toBeUndefined()
  })

  it('stops at the limit of ten', () => {
    expect(atLimit(9)).toBe(false)
    expect(atLimit(10)).toBe(true)
  })
})
