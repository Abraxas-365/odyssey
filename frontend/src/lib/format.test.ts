import { describe, expect, it } from 'vitest'
import { formatRelative } from './format'
import { slugify } from './projects'

describe('slugify (mirrors the API Slugify)', () => {
  it.each([
    ['Visitas a clientes', 'visitas-a-clientes'],
    ['Señal de Producción', 'senal-de-produccion'],
    ['  --Hola, Mundo!!  ', 'hola-mundo'],
    ['Año 2026 · Q4', 'ano-2026-q4'],
  ])('%s → %s', (input, out) => expect(slugify(input)).toBe(out))

  it('caps at 40 chars without a trailing dash', () => {
    const s = slugify('a'.repeat(39) + ' bcd')
    expect(s.length).toBeLessThanOrEqual(40)
    expect(s.endsWith('-')).toBe(false)
  })
})

describe('formatRelative', () => {
  const now = Date.parse('2026-10-27T12:00:00Z')
  it('says ahora under a minute', () => expect(formatRelative('2026-10-27T11:59:30Z', now)).toBe('ahora'))
  it('uses Spanish relative units', () => {
    expect(formatRelative('2026-10-27T10:00:00Z', now)).toBe('hace 2 horas')
    expect(formatRelative('2026-10-26T12:00:00Z', now)).toBe('ayer')
  })
  it('is empty for invalid input', () => expect(formatRelative('nope', now)).toBe(''))
})
