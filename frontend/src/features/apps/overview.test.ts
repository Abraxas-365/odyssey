import { describe, expect, it } from 'vitest'
import type { CurrentRelease, Deployment, GitHubPush } from '@/lib/types'
import { assistantPrompt, buildPrompt, deriveName, fixPrompt, hasUnpublishedCommit, primaryAction, type OverviewState } from './overview'

const base: OverviewState = { archived: false, canPublish: true, publishing: false, live: false, unpublishedCommit: false }

describe('primaryAction', () => {
  it('follows the app state (UX v4 §5.3)', () => {
    expect(primaryAction(base)).toBe('first')
    expect(primaryAction({ ...base, live: true })).toBe('assistant')
    expect(primaryAction({ ...base, live: true, unpublishedCommit: true })).toBe('github')
    expect(primaryAction({ ...base, unpublishedCommit: true })).toBe('github')
  })
  it('offers nothing while publishing, archived or without permission', () => {
    expect(primaryAction({ ...base, live: true, publishing: true })).toBe('none')
    expect(primaryAction({ ...base, archived: true })).toBe('none')
    expect(primaryAction({ ...base, canPublish: false, unpublishedCommit: true })).toBe('none')
  })
})

describe('hasUnpublishedCommit', () => {
  const git = (commit: string) => ({ provider: 'github' as const, repository: 'acme/visitas', branch: 'main', commit, title: 't', author: 'a' })
  const push = (commit: string, status: GitHubPush['status'] = 'deployed') => ({ git: git(commit), status }) as GitHubPush
  const online = (commit: string | null): CurrentRelease => ({
    environment_id: 'e', url: '', access: null, applied_settings_revision: null, active: null,
    release: { id: 'r' } as CurrentRelease['release'],
    deployment: { source: commit ? { id: 's', name: 'n', size: 1, git: git(commit) } : null } as Deployment,
  })
  it('is true when the newest commit is not the one online', () => {
    expect(hasUnpublishedCommit(push('b'), online('a'))).toBe(true)
    expect(hasUnpublishedCommit(push('b', 'failed'), online('a'))).toBe(true)
    expect(hasUnpublishedCommit(push('a'), undefined)).toBe(true) // nothing online
    expect(hasUnpublishedCommit(push('a'), online(null))).toBe(true) // a folder is online
  })
  it('is false when it is online, on its way, or there is no push', () => {
    expect(hasUnpublishedCommit(push('a'), online('a'))).toBe(false)
    expect(hasUnpublishedCommit(push('b', 'importing'), online('a'))).toBe(false)
    expect(hasUnpublishedCommit(push('b', 'queued'), online('a'))).toBe(false)
    expect(hasUnpublishedCommit(undefined, online('a'))).toBe(false)
  })
})

describe('assistantPrompt', () => {
  it('names the app and the way its code goes', () => {
    expect(assistantPrompt('visitas', { live: false, github: false })).toBe('Publica esta carpeta en la app visitas de Odicea.')
    expect(assistantPrompt('visitas', { live: true, github: false })).toMatch(/^Publica los cambios/)
    expect(assistantPrompt('visitas', { live: true, github: true })).toMatch(/GitHub/)
  })
})

describe('deriveName (UX v4 §5.6)', () => {
  it('keeps what the idea is, without the words that open a sentence', () => {
    expect(deriveName('Quiero una app para registrar las visitas a clientes')).toBe('Registrar las visitas')
    expect(deriveName('Tablero de ventas por región, con filtros')).toBe('Tablero de ventas')
    expect(deriveName('Reporte de cobros, semanal')).toBe('Reporte de cobros')
    expect(deriveName('necesito un formulario de vacaciones')).toBe('Formulario de vacaciones')
    expect(deriveName('Visitas a clientes')).toBe('Visitas a clientes')
  })
  it('never ends on a connector and says nothing for nothing', () => {
    expect(deriveName('Control de stock de la bodega central')).toBe('Control de stock')
    expect(deriveName('   ')).toBe('')
    expect(deriveName('quiero una app')).toBe('')
  })
})

describe('buildPrompt and fixPrompt', () => {
  it('ask the assistant to build the idea and publish it in the app', () => {
    expect(buildPrompt('visitas', 'Registrar visitas con fotos.')).toBe('Construye en esta carpeta: Registrar visitas con fotos. Cuando funcione, publícala en la app visitas de Odicea.')
    expect(buildPrompt('visitas', '')).toMatch(/la app visitas\. Cuando funcione/)
    expect(fixPrompt('visitas')).toMatch(/app visitas de Odicea no se publicó/)
  })
})
