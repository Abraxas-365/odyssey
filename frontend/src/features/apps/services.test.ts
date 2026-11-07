import { describe, expect, it } from 'vitest'
import { bodyOf, changedText, hasProblems, isMulti, isSimple, newDraft, problemsOf, sameList, stateText, whereText, type Draft } from './services'

const web: Draft = { name: 'web', kind: 'web', root: 'apps/web', route: '/', strip_prefix: false }
const api: Draft = { name: 'api', kind: 'web', root: 'apps/api', route: '/api', strip_prefix: true }
const jobs: Draft = { name: 'jobs', kind: 'worker', root: 'apps/jobs', route: '', strip_prefix: false }

describe('services (MS-6)', () => {
  it('names how each part stands', () => {
    expect(stateText({ kind: 'web', phase: 'ready', reason: '' }).label).toBe('En línea')
    expect(stateText({ kind: 'worker', phase: 'ready', reason: '' }).label).toBe('Funcionando')
    expect(stateText({ kind: 'web', phase: 'failed', reason: 'image_pull' })).toEqual({ label: 'No se pudo descargar', tone: 'destructive' })
    expect(stateText({ kind: 'web', phase: 'failed', reason: '' }).label).toBe('No responde')
    expect(stateText({ kind: 'web', phase: '', reason: '' }).tone).toBe('secondary')
  })
  it('talks about services only from two on (D3)', () => {
    expect(isMulti(undefined)).toBe(false)
    expect(isMulti([web])).toBe(false)
    expect(isMulti([web, api])).toBe(true)
  })

  it('says where each part answers in words', () => {
    expect(whereText(web)).toBe('La dirección principal de la app')
    expect(whereText(api)).toBe('En /api (recibe la ruta sin ese prefijo)')
    expect(whereText(jobs)).toBe('Sin dirección: trabaja en segundo plano')
    expect(whereText({ kind: 'web', route: '', strip_prefix: false })).toBe('Solo la ven las otras partes de la app')
  })

  it('sends a normalized body: slashes trimmed, workers without route or prefix', () => {
    expect(bodyOf([{ ...web, root: '/apps/web/' }, { ...api, route: '/api/' }, { ...jobs, route: '/x', strip_prefix: true }])).toEqual([
      { name: 'web', kind: 'web', root: 'apps/web', route: '/', strip_prefix: false },
      { name: 'api', kind: 'web', root: 'apps/api', route: '/api', strip_prefix: true },
      { name: 'jobs', kind: 'worker', root: 'apps/jobs', route: '', strip_prefix: false },
    ])
    // stripping "/" means nothing: never sent
    expect(bodyOf([{ ...web, strip_prefix: true }])[0].strip_prefix).toBe(false)
  })

  it('finds the same problems as the API', () => {
    expect(hasProblems(problemsOf([web, api, jobs]))).toBe(false)
    const ps = problemsOf([
      { ...web, name: 'Web' },
      { ...api, root: '../secret' },
      { ...api, name: 'api2', route: '/_odicea/x' },
      { ...api, name: 'api3' },
      { ...api, name: 'api3', route: '/b' },
    ])
    expect(ps[0].name).toMatch(/minúsculas/)
    expect(ps[1].root).toMatch(/carpeta/)
    expect(ps[2].route).toMatch(/_odicea/)
    expect(ps[3].route).toMatch(/ya responde/) // /api twice
    expect(ps[3].name).toMatch(/otra parte/) // api3 twice
    expect(problemsOf([{ ...api, route: '/a b' }])[0].route).toMatch(/ruta como/)
  })

  it('knows the plain one-part app and an unchanged list', () => {
    const plain: Draft = { name: 'web', kind: 'web', root: '', route: '/', strip_prefix: false }
    expect(isSimple([plain])).toBe(true)
    expect(isSimple([web])).toBe(false) // a folder is not the whole code
    expect(sameList([web, api], [web, api])).toBe(true)
    expect(sameList([api, web], [web, api])).toBe(false) // order is part of it
    expect(sameList([{ ...web, root: '/apps/web/' }], [web])).toBe(true)
  })

  it('proposes a free name and route for a new part', () => {
    expect(newDraft([])).toEqual({ name: 'web', kind: 'web', root: '', route: '/', strip_prefix: false })
    expect(newDraft([web])).toEqual({ name: 'api', kind: 'web', root: '', route: '/api', strip_prefix: true })
    expect(newDraft([web, api])).toMatchObject({ name: 'worker', route: '/worker' })
  })

  it('says what a version changed only with two or more parts', () => {
    expect(changedText(['web'], 1)).toBeUndefined()
    expect(changedText(null, 3)).toBeUndefined()
    expect(changedText([], 2)).toBe('Sin cambios en el código')
    expect(changedText(['api'], 2)).toBe('Cambió: api')
    expect(changedText(['web', 'api', 'jobs'], 3)).toBe('Cambiaron: web, api y jobs')
  })
})
