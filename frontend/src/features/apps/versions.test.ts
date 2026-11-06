import { describe, expect, it } from 'vitest'
import type { CurrentRelease, Deployment } from '@/lib/types'
import { appStatus, configReachedApp, lastProblem, progressSteps, versionMessage, versions } from './versions'

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
    build_id: null,
    builds: [],
    source: null,
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
    settings_revision: null,
    error: null,
    run_id: 'run',
    requested_by: 'me',
    requested_by_name: '',
    requested_via: 'console',
    message: '',
    rebuild: false,
    created_at: `2026-10-27T10:${String(n).padStart(2, '0')}:00Z`,
    updated_at: '2026-10-27T10:00:00Z',
    finished_at: `2026-10-27T10:${String(n).padStart(2, '0')}:30Z`,
    ...over,
  }
}

const current = (d: Deployment, active?: Deployment): CurrentRelease => ({
  environment_id: 'e',
  url: 'https://app.example',
  release: { id: d.release_id!, organization_id: 'o', project_id: 'p', image: d.image, config: { port: 80, health_path: '/', replicas: 1, env: {} }, services: [], created_by: 'me', created_at: d.created_at },
  deployment: d,
  active: active ?? null,
  applied_settings_revision: null,
  access: null,
})

describe('versions', () => {
  // newest first, as the API returns them
  const v1 = dep({})
  const bad = dep({ status: 'failed', step: 'wait_ready', error: { code: 'HEALTH_CHECK_FAILED', message: 'No responde.', detail: 'GET /health 503' } })
  const restore = dep({ reason: 'auto_rollback', release_id: v1.release_id, requested_by: '', requested_via: 'platform' })
  const replaced = dep({ status: 'cancelled' })
  const v2 = dep({ requested_by: 'ana', requested_by_name: 'Ana Torres', requested_via: 'assistant:codex' })
  const history = [v2, replaced, restore, bad, v1]

  it('lists what went live or failed, marks the live one and who did it, through what', () => {
    const items = versions(history, current(v2), 'me')
    expect(items.map(i => [i.id, i.kind, i.by, i.channel?.label])).toEqual([
      [v2.id, 'live', 'Ana Torres', 'Codex'],
      [restore.id, 'previous', 'la plataforma', 'Automático'],
      [bad.id, 'failed', 'ti', 'Consola'],
      [v1.id, 'previous', 'ti', 'Consola'],
    ])
    expect(items[1].message).toBe('Restaurada automáticamente')
  })

  it('offers "Volver a esta" only for other releases that went live', () => {
    const items = versions([restore, bad, v1], current(restore), 'me')
    expect(items.map(i => i.restorable)).toEqual([false, false, false]) // v1 is the release online
    expect(versions(history, current(v2), 'me').filter(i => i.restorable).map(i => i.id)).toEqual([restore.id, v1.id])
  })

  it('lists a deployment waiting for IT as such, never restorable', () => {
    const waiting = dep({ status: 'waiting_approval', finished_at: null })
    const items = versions([waiting, v1], current(v1, waiting), 'me')
    expect(items.map(i => [i.id, i.kind, i.restorable])).toEqual([[waiting.id, 'waiting', false], [v1.id, 'live', false]])
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
    // waiting for IT is in progress, not a problem
    expect(lastProblem([dep({ status: 'waiting_approval' }), bad])).toBe(bad)
    expect(lastProblem([])).toBeUndefined()
  })
})

describe('appStatus', () => {
  const d = dep({})
  it('follows the plain-language states', () => {
    expect(appStatus(true, current(d), undefined).status).toBe('archived')
    expect(appStatus(false, current(d, dep({ status: 'applying' })), undefined).status).toBe('building')
    // IT has to approve before it starts: its own word, not "Publicando…"
    expect(appStatus(false, current(d, dep({ status: 'waiting_approval' })), undefined)).toEqual({ status: 'waiting', label: 'Esperando aprobación de TI' })
    expect(appStatus(false, undefined, undefined).status).not.toBe('waiting')
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

  it('starts a deployment from a folder by building it', () => {
    const d = dep({ status: 'building', step: 'build', release_id: null, image: '', build_id: 'b', steps: [{ name: 'build', status: 'running' }, { name: 'apply', status: 'pending' }, { name: 'wait_ready', status: 'pending' }, { name: 'promote', status: 'pending' }] })
    expect(progressSteps(d).map(s => [s.title, s.state])[0]).toEqual(['Construyendo tu app', 'running'])
  })
})

describe('folder versions', () => {
  it('are "Subida desde una carpeta"; which folder and its size are technical; building counts as in progress', () => {
    const built = dep({ build_id: 'b', source: { id: 's', name: 'visitas', size: 4300, git: null } })
    const [item] = versions([built], current(built), 'me')
    expect(item.message).toBe('Subida desde una carpeta')
    expect(item.source).toBe('carpeta visitas · 4.2 KB')
    const failed = dep({ status: 'failed', release_id: null, image: '', build_id: 'b2', source: { id: 's2', name: 'roto', size: 10, git: null } })
    expect(versions([failed], undefined, 'me')[0].sha).toBeUndefined()
    expect(lastProblem([dep({ status: 'building' }), failed])).toBe(failed)
  })

  it('are called by what whoever asked said changed', () => {
    const said = dep({ build_id: 'b', message: ' Agrega el filtro por vendedor ', source: { id: 's', name: 'visitas', size: 1, git: null } })
    expect(versionMessage(said)).toBe('Agrega el filtro por vendedor')
    expect(versionMessage(dep({ message: 'Imagen nueva' }))).toBe('Imagen nueva')
    expect(versionMessage(dep({}))).toBe('Nueva versión')
    // a restore keeps its own phrase, whatever the version it restores said
    expect(versionMessage(dep({ reason: 'rollback', message: 'x' }))).toBe('Volviste a una versión anterior')
  })
})

describe('GitHub versions', () => {
  const git = { provider: 'github' as const, repository: 'acme/visitas', branch: 'main', commit: 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678', title: 'Agrega fotos a la visita', author: 'ana-ruiz' }
  const fromGit = (over: Partial<Deployment> = {}) => dep({ build_id: 'b', source: { id: 's', name: 'acme-visitas', size: 4300, git }, ...over })

  it('are called by the commit title, with the commit in the GitLine data', () => {
    const d = fromGit({ message: 'ignored: the commit names it' })
    const [item] = versions([d], current(d), 'me')
    expect(item.message).toBe('Agrega fotos a la visita')
    expect(item.git).toEqual({ branch: 'main', sha: git.commit, author: 'ana-ruiz' })
  })

  it('fall back to a plain phrase when the commit has no title', () => {
    expect(versionMessage(dep({ reason: 'config' }))).toBe('Cambió quién puede abrirla')
    expect(versionMessage(fromGit({ source: { id: 's', name: 'x', size: 1, git: { ...git, title: '  ' } } }))).toBe('Cambios desde GitHub')
  })

  it('leave folder uploads and restores without git', () => {
    const folder = dep({ build_id: 'b', source: { id: 's', name: 'visitas', size: 10, git: null } })
    const restored = fromGit({ reason: 'rollback' })
    const items = versions([folder, restored], undefined, 'me')
    expect(items.map(i => i.git)).toEqual([undefined, undefined])
    expect(items[1].message).toBe('Volviste a una versión anterior')
  })
})

describe('failed config deployments', () => {
  const failed = (step: Deployment['step'], code: string) =>
    dep({ reason: 'config', status: 'failed', step, error: { code, message: 'No quedó lista', detail: '' } as Deployment['error'] })

  it('show in Versiones with their error and are the last problem', () => {
    const f = failed('wait_ready', 'HEALTH_CHECK_FAILED')
    const list = versions([f], current(dep({})), 'me')
    expect(list.some(v => v.id === f.id && v.kind === 'failed' && v.deployment.error?.code === 'HEALTH_CHECK_FAILED')).toBe(true)
    expect(lastProblem([f])).toBe(f)
  })

  it('tells one that reached the cluster (the app is not reopened) from one refused at apply', () => {
    expect(configReachedApp(failed('wait_ready', 'HEALTH_CHECK_FAILED'))).toBe(true)
    expect(configReachedApp(failed('promote', 'PROMOTE_FAILED'))).toBe(true)
    expect(configReachedApp(failed('apply', 'APPLY_FAILED'))).toBe(false)
    expect(configReachedApp(dep({ reason: 'deploy', status: 'failed', step: 'wait_ready' }))).toBe(false)
  })
})
