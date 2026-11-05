import { describe, expect, it } from 'vitest'
import type { AttentionItem, InventoryScan } from '@/lib/types'
import { buildScanText, rowUnavailable, scanText, splitScopes, unavailableText } from './inventory'

const scan = (p: Partial<InventoryScan>): InventoryScan => ({ state: 'none', critical: 0, high: 0, scanned_at: null, ...p })

describe('scanText', () => {
  it('says each state in words, with the tone that carries it', () => {
    expect(scanText(scan({ state: 'none' }))).toMatchObject({ label: 'Sin escaneo', tone: 'neutral' })
    expect(scanText(scan({ state: 'pending' }))).toMatchObject({ label: 'Escaneando…', tone: 'info' })
    expect(scanText(scan({ state: 'clean' })).tone).toBe('ok')
    expect(scanText(scan({ state: 'unknown', error: 'El clúster no pudo escanear' }))).toMatchObject({ tone: 'warn', detail: 'El clúster no pudo escanear' })
  })

  it('counts criticals and highs, bad only with a critical', () => {
    expect(scanText(scan({ state: 'vulnerable', critical: 1, high: 3 }))).toMatchObject({ label: '1 crítica, 3 altas', tone: 'bad' })
    expect(scanText(scan({ state: 'vulnerable', critical: 2 }))).toMatchObject({ label: '2 críticas', tone: 'bad' })
    expect(scanText(scan({ state: 'vulnerable', high: 1 }))).toMatchObject({ label: '1 alta', tone: 'warn' })
  })
})

describe('buildScanText', () => {
  it('has nothing to say while the build loads', () => expect(buildScanText(undefined, true)).toBeUndefined())
  it('an image published as it was is never scanned', () => expect(buildScanText(null)).toMatchObject({ label: 'Sin escaneo' }))
  it('a build without a scan yet is on its way', () => expect(buildScanText({ status: 'succeeded', scan: null })).toMatchObject({ label: 'Escaneando…' }))
  it('a scan that could not be done says why, not "clean"', () => {
    const s = { digest: 'sha256:x', critical: 0, high: 0, medium: 0, low: 0, tool: '', db_updated_at: '', error: 'No hay espacio', scanned_at: '2026-05-01T00:00:00Z' }
    expect(buildScanText({ status: 'succeeded', scan: s })).toMatchObject({ tone: 'warn', detail: 'No hay espacio' })
    expect(buildScanText({ status: 'succeeded', scan: { ...s, error: '', critical: 1 } })).toMatchObject({ tone: 'bad' })
    expect(buildScanText({ status: 'succeeded', scan: { ...s, error: '' } })).toMatchObject({ tone: 'ok' })
  })
})

describe('attention copy', () => {
  it('names the sources that failed, never "todo en orden"', () => {
    expect(unavailableText([])).toBe('')
    expect(unavailableText(['clusters'])).toBe('No pudimos revisar los clústeres ahora, así que esta lista puede estar incompleta.')
    expect(unavailableText(['approvals', 'clusters', 'requests'])).toContain('las aprobaciones pendientes, los clústeres y tus solicitudes')
  })
  it('splits TI from the person’s own', () => {
    const item = (scope: AttentionItem['scope']) => ({ scope }) as AttentionItem
    const { it: ti, mine } = splitScopes([item('mine'), item('it'), item('it')])
    expect([ti.length, mine.length]).toEqual([2, 1])
  })
  it('says what a row could not read', () => expect(rowUnavailable({ unavailable: ['scan', 'cluster'] })).toBe('el escaneo, el clúster'))
})
