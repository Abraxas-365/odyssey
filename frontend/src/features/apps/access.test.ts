import { describe, expect, it } from 'vitest'
import type { CurrentRelease, EnvironmentSettings } from '@/lib/types'
import { accessApplying, accessHeadline, accessLabel, accessNotice, accessOptions, accessPending, canChoose, noticeText, optionDisabled } from './access'

const settings = (revision: number, mode: EnvironmentSettings['access']['mode'] = 'team', extra: Partial<EnvironmentSettings> = {}): EnvironmentSettings => ({ revision, access: { mode }, env: {}, resources: [], ai: { enabled: false, key_version: 0 }, secrets: [], secret_key: null, external_secrets: false, secret_stores: [], secrets_cluster_revoked: false, access_login_available: true, access_protection_available: true, ...extra })
const online = (applied: number | null, active: unknown = null): CurrentRelease =>
  ({ environment_id: 'e', url: 'https://a.test', release: { id: 'r' }, deployment: { id: 'd' }, active, applied_settings_revision: applied }) as unknown as CurrentRelease

describe('access', () => {
  it('words every mode and never shows its id', () => {
    expect(accessOptions.map(o => o.value)).toEqual(['team', 'organization', 'public'])
    expect(accessLabel('organization')).toBe('Toda la empresa')
    for (const o of accessOptions) expect(`${o.title} ${o.description}`).not.toMatch(/\b(team|organization|public)\b/)
  })

  it('is pending only while the app online has not been given the settings in force', () => {
    expect(accessPending(settings(2), online(2))).toBe(false)
    expect(accessPending(settings(3), online(2))).toBe(true)
    // the cluster was told nothing yet
    expect(accessPending(settings(0), online(null))).toBe(true)
    // a config deployment that failed after apply still counts as told: the app is not reopened
    expect(accessPending(settings(2), online(2))).toBe(false)
    // nothing online: the next publication carries them
    expect(accessPending(settings(3), { ...online(null), release: null })).toBe(false)
    expect(accessPending(undefined, online(1))).toBe(false)
    expect(accessPending(settings(1), undefined)).toBe(false)
  })

  it('lets only who holds the permission choose public', () => {
    expect(canChoose('team', false)).toBe(true)
    expect(canChoose('organization', false)).toBe(true)
    expect(canChoose('public', false)).toBe(false)
    expect(canChoose('public', true)).toBe(true)
  })

  it('shows a pre-1e app as open to anyone with the link, applied and not pending', () => {
    const s = settings(1, 'public')
    expect(accessLabel(s.access.mode)).toBe('Cualquiera con el enlace')
    expect(accessPending(s, online(1))).toBe(false)
    expect(accessNotice(s, online(1), undefined)).toBeUndefined()
  })

  it('heads the panel with who can open it now, never the saved mode merely because it is saved', () => {
    const withAccess = (access: CurrentRelease['access']) => ({ ...online(1), access })
    // public is saved; the cluster still enforces team: "now" is team (the same as Inicio)
    expect(accessHeadline(settings(2, 'public'), withAccess({ mode: 'team', pending: 'public' }))).toBe('Ahora: solo tu equipo.')
    expect(accessHeadline(settings(1, 'organization'), withAccess({ mode: 'organization', pending: null }))).toBe('Ahora: toda la empresa.')
    // nothing online: what publishing will apply, worded as such
    expect(accessHeadline(settings(0), { ...online(null), release: null, access: null })).toBe('Al publicarla: solo tu equipo.')
    // online but it cannot be told: nothing rather than a guess
    expect(accessHeadline(settings(1, 'public'), withAccess(null))).toBeUndefined()
    expect(accessHeadline(settings(1), undefined)).toBeUndefined()
  })

  describe('notices', () => {
    const config = { id: 'c', reason: 'config', status: 'applying' }

    it('says "Aplicando el cambio…" while the config deployment runs, before anything else', () => {
      expect(accessApplying(online(1, config))).toBe(true)
      expect(accessApplying(online(1, { id: 'd2', reason: 'deploy' }))).toBe(false)
      expect(accessNotice(settings(2), online(1, config), undefined)).toBe('applying')
      expect(noticeText.applying.title).toBe('Aplicando el cambio…')
      // ...even with the cluster or the login unavailable
      expect(accessNotice(settings(2, 'team', { access_protection_available: false }), online(1, config), undefined)).toBe('applying')
    })

    it('tells the cluster apart from the login: two different situations, two different words', () => {
      const cluster = settings(0, 'team', { access_protection_available: false })
      expect(accessNotice(cluster, online(0), undefined)).toBe('cluster')
      expect(noticeText.cluster.title).toBe('Este clúster todavía no puede pedir inicio de sesión: TI debe habilitarlo.')
      expect(noticeText.cluster.body).toContain('solo puedes dejarla abierta a cualquiera con el enlace')

      const login = settings(1, 'team', { access_login_available: false })
      expect(accessNotice(login, online(1), undefined)).toBe('login')
      expect(noticeText.login.title).toBe('La app se publica, pero nadie podrá iniciar sesión hasta que TI lo configure.')
      expect(noticeText.login.title).not.toBe(noticeText.cluster.title)
      // choosing public needs no login
      expect(accessNotice(login, online(1), 'public')).toBeUndefined()
      // an app that is already public is not warned about either
      expect(accessNotice(settings(1, 'public', { access_protection_available: false }), online(1), undefined)).toBeUndefined()
      // a cluster that cannot protect reports the cluster, not the login, even if both are missing
      expect(accessNotice(settings(0, 'team', { access_protection_available: false, access_login_available: false }), online(0), undefined)).toBe('cluster')
    })

    it('says the change has not arrived only when nothing is on its way', () => {
      expect(accessNotice(settings(3), online(2), undefined)).toBe('pending')
      expect(accessNotice(settings(3), online(2, { id: 'd2', reason: 'deploy' }), undefined)).toBeUndefined()
      expect(accessNotice(settings(2), online(2), undefined)).toBeUndefined()
      expect(accessNotice(undefined, online(2), undefined)).toBeUndefined()
    })
  })

  it('disables protected options on a cluster that cannot protect, but keeps displaying the saved value', () => {
    const base = { canWrite: true, canPublic: true, saved: 'team' as const, protectionAvailable: false }
    expect(optionDisabled('team', base)).toBe(true)
    expect(optionDisabled('organization', base)).toBe(true)
    expect(optionDisabled('public', base)).toBe(false)
    expect(optionDisabled('team', { ...base, protectionAvailable: true })).toBe(false)
    expect(optionDisabled('public', { ...base, protectionAvailable: true, canPublic: false })).toBe(true)
    // a public app stays displayable for someone who cannot choose it
    expect(optionDisabled('public', { ...base, protectionAvailable: true, canPublic: false, saved: 'public' })).toBe(false)
    expect(optionDisabled('public', { ...base, canWrite: false })).toBe(true)
  })
})
