import { describe, expect, it } from 'vitest'
import { aiActions, aiState, aiUnavailable, aiVariablesText } from './ai'

describe('ai', () => {
  it('says the state in plain words', () => {
    expect(aiState({ status: 'off' })).toMatchObject({ label: 'Sin activar', busy: false })
    expect(aiState({ status: 'enabling' })).toMatchObject({ label: 'Activando…', busy: true })
    expect(aiState({ status: 'enabled' })).toMatchObject({ label: 'Activa', tone: 'success', busy: false })
    expect(aiState({ status: 'rotating' }).busy).toBe(true)
    expect(aiState({ status: 'disabling' }).busy).toBe(true)
    // a revocation that gave up is visible and not busy: it can be retried
    expect(aiState({ status: 'revoke_failed' })).toMatchObject({ label: 'Llave por revocar', tone: 'warning', busy: false })
  })

  it('offers one change at a time, only to people who can write', () => {
    expect(aiActions({ status: 'off', available: true }, true, false)).toEqual({ enable: true, retry: false, rotate: false, disable: false })
    expect(aiActions({ status: 'enabled', available: true }, true, false)).toEqual({ enable: false, retry: false, rotate: true, disable: true })
    expect(aiActions({ status: 'enabled', available: true }, false, false)).toEqual({ enable: false, retry: false, rotate: false, disable: false })
    // a change in progress, or a publication in progress, blocks them all
    expect(aiActions({ status: 'rotating', available: true }, true, false)).toEqual({ enable: false, retry: false, rotate: false, disable: false })
    expect(aiActions({ status: 'enabled', available: true }, true, true)).toEqual({ enable: false, retry: false, rotate: false, disable: false })
    // a failed revocation: retry it, or turn the AI on again with a new key; nothing else
    expect(aiActions({ status: 'revoke_failed', available: true }, true, false)).toEqual({ enable: true, retry: true, rotate: false, disable: false })
    expect(aiActions({ status: 'revoke_failed', available: true }, false, false).retry).toBe(false)
    // unavailable: it cannot be turned on, but an app that has it can still turn it off
    expect(aiActions({ status: 'off', available: false }, true, false).enable).toBe(false)
    expect(aiActions({ status: 'enabled', available: false }, true, false).disable).toBe(true)
  })

  it('explains why it is unavailable, only when it is off', () => {
    expect(aiUnavailable({ status: 'off', available: false, message: 'Pide a TI que la conecte.' })).toBe('Pide a TI que la conecte.')
    expect(aiUnavailable({ status: 'off', available: false })).toContain('TI')
    expect(aiUnavailable({ status: 'off', available: true })).toBeUndefined()
    expect(aiUnavailable({ status: 'enabled', available: false, message: 'x' })).toBeUndefined()
  })

  it('lists variable names only', () => {
    expect(aiVariablesText({ variables: ['OPENAI_API_KEY', 'OPENAI_BASE_URL'] })).toBe('OPENAI_API_KEY  OPENAI_BASE_URL')
  })
})
