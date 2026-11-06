import { describe, expect, it } from 'vitest'
import { attribution, channelOf, requesterOf } from './attribution'

const req = (requested_by: string, requested_via: string, requested_by_name = '') => ({ requested_by, requested_via, requested_by_name })

describe('requesterOf', () => {
  it('ti, the name the directory gave, tu equipo while unknown, la plataforma', () => {
    expect(requesterOf(req('me', 'console', 'Prabir'), 'me')).toBe('ti')
    expect(requesterOf(req('ana', 'console', 'Ana Torres'), 'me')).toBe('Ana Torres')
    expect(requesterOf(req('ana', 'assistant:codex'), 'me')).toBe('tu equipo')
    expect(requesterOf(req('ana', 'console', '   '), 'me')).toBe('tu equipo')
    expect(requesterOf(req('', 'github'), 'me')).toBe('tu equipo') // a push nobody in the console asked for
    expect(requesterOf(req('', 'platform'), 'me')).toBe('la plataforma')
    expect(requesterOf(req('ana', 'platform', 'Ana'), 'me')).toBe('la plataforma')
    expect(requesterOf(req('', ''), 'me')).toBe('la plataforma') // before requested_via existed
    expect(requesterOf(req('', 'console'), '')).toBe('la plataforma') // '' never matches "me"
  })
})

describe('channelOf', () => {
  it('words requested_via; an unrecorded one says so', () => {
    expect(channelOf('console')).toEqual({ kind: 'console', label: 'Consola' })
    expect(channelOf('github')).toEqual({ kind: 'github', label: 'GitHub' })
    expect(channelOf('platform')).toEqual({ kind: 'platform', label: 'Automático' })
    expect(channelOf('assistant:claude_code')).toEqual({ kind: 'assistant', label: 'Claude Code' })
    expect(channelOf('assistant:codex').label).toBe('Codex')
    expect(channelOf('assistant:antigravity').label).toBe('Antigravity')
    expect(channelOf('assistant:vscode').label).toBe('VS Code')
    expect(channelOf('assistant:other')).toEqual({ kind: 'assistant', label: 'Asistente' })
    expect(channelOf('')).toEqual({ kind: 'unrecorded', label: 'Canal no registrado' })
    expect(channelOf('carrier-pigeon').kind).toBe('unrecorded')
  })
})

describe('attribution', () => {
  it('both, from the same deployment', () => {
    expect(attribution(req('ana', 'assistant:claude_code', 'Ana Torres'), 'me')).toEqual({ by: 'Ana Torres', channel: { kind: 'assistant', label: 'Claude Code' } })
  })
})
