import { describe, expect, it } from 'vitest'
import type { CurrentRelease, Deployment } from '@/lib/types'
import { appStatus, lastProblem, progressSteps, versions } from './versions'

let n = 0
function dep(over: Partial<Deployment>): Deployment {
  n++
  return {
    id: `d${n}`,
    organization_id: 'o',
    project_id: 'p',
    environment_id: 'e',
    release_id: `r${n}`,
    image: `r.local/app@sha256:${String(n).padStart(64, '0')}`,
    status: 'ready',
    reason: 'deploy',
    step: 'promote',
    steps: [
      { name: 'apply', status: 'done' },
      { name: 'wait_ready', status: 'done' },
      { name: 'promote', status: 'done' },
    ],
    revision: n,
    previous_release_id: null,
    rollback_of: null,
    error: null,
    run_id: 'run',
    requested_by: 'me',
    created_at: `2026-10-27T10:${String(n).padStart(2, '0')}:00Z`,
    updated_at: '2026-10-27T10:00:00Z',
    finished_at: `2026-10-27T10:${String(n).padStart(2, '0')}:30Z`,
    ...over,
  }
}

const current = (d: Deployment, active?: Deployment): CurrentRelease => ({
  environment_id: 'e',
  url: 'https://app.example',
  release: { id: d.release_id, organization_id: 'o', project_id: 'p', image: d.image, config: { port: 80, health_path: '/', replicas: 1, env: {} }, created_by: 'me', created_at: d.created_at },
  deployment: d,
  active: active ?? null,
})

describe('versions', () => {
  // newest first, as the API returns them
  const v1 = dep({})
  const bad = dep({ status: 'failed', step: 'wait_ready', error: { code: 'HEALTH_CHECK_FAILED', message: 'No responde.', detail: 'GET /health 503' } })
  const restore = dep({ reason: 'auto_rollback', release_id: v1.release_id, requested_by: '' })
  const replaced = dep({ status: 'cancelled' })
  const v2 = dep({ requested_by: 'ana' })
  const history = [v2, replaced, restore, bad, v1]

  it('lists what went live or failed, marks the live one and who did it', () => {
    const items = versions(history, current(v2), 'me')
    expect(items.map(i => [i.id, i.kind, i.by])).toEqual([
      [v2.id, 'live', 'tu equipo'],
      [restore.id, 'previous', 'la plataforma'],
      [bad.id, 'failed', 'ti'],
      [v1.id, 'previous', 'ti'],
    ])
    expect(items[1].message).toBe('Restaurada automáticamente')
  })

  it('offers "Volver a esta" only for other releases that went live', () => {
    const items = versions([restore, bad, v1], current(restore), 'me')
    expect(items.map(i => i.restorable)).toEqual([false, false, false]) // v1 is the release online
    expect(versions(history, current(v2), 'me').filter(i => i.restorable).map(i => i.id)).toEqual([restore.id, v1.id])
  })
})

describe('lastProblem', () => {
  it('is the last attempt someone made, when it failed, even after an automatic restore', () => {
    const v1 = dep({})
    const bad = dep({ status: 'failed' })
    const restore = dep({ reason: 'auto_rollback' })
    expect(lastProblem([restore, bad, v1])).toBe(bad)
    expect(lastProblem([dep({}), restore, bad, v1])).toBeUndefined()
    expect(lastProblem([dep({ status: 'verifying' }), bad])).toBe(bad)
    expect(lastProblem([])).toBeUndefined()
  })
})

describe('appStatus', () => {
  const d = dep({})
  it('follows the plain-language states', () => {
    expect(appStatus(true, current(d), undefined).status).toBe('archived')
    expect(appStatus(false, current(d, dep({ status: 'applying' })), undefined).status).toBe('building')
    expect(appStatus(false, current(d), undefined).status).toBe('live')
    expect(appStatus(false, undefined, dep({ status: 'failed' }))).toEqual({ status: 'attention', label: 'No se publicó' })
    expect(appStatus(false, undefined, undefined)).toEqual({ status: 'paused', label: 'Sin publicar' })
  })
})

describe('progressSteps', () => {
  it('names the steps for people and keeps the internal name for technical mode', () => {
    const steps = progressSteps(dep({ status: 'verifying', steps: [{ name: 'apply', status: 'done' }, { name: 'wait_ready', status: 'running' }, { name: 'promote', status: 'pending' }] }))
    expect(steps.map(s => [s.title, s.state, s.technical])).toEqual([
      ['Preparando tu app', 'done', 'apply'],
      ['Revisando que funcione', 'running', 'wait_ready'],
      ['Publicando', 'pending', 'promote'],
    ])
  })
})
