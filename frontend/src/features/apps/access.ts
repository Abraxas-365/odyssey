import { Building2, Globe, Users } from 'lucide-react'
import type { AccessMode, CurrentRelease, EnvironmentSettings } from '@/lib/types'

/** "Quién puede abrirla" in plain words (UX v3 §1): modes are never shown by their ids. */
export const accessOptions: { value: AccessMode; title: string; description: string; icon: typeof Users }[] = [
  { value: 'team', title: 'Solo tu equipo', description: 'Quienes están en tu organización de Odicea inician sesión para abrirla.', icon: Users },
  { value: 'organization', title: 'Toda la empresa', description: 'Cualquier persona de la empresa con su inicio de sesión de Odicea.', icon: Building2 },
  { value: 'public', title: 'Cualquiera con el enlace', description: 'Sin iniciar sesión. Solo TI puede dejarla así.', icon: Globe },
]

export const accessLabel = (m: AccessMode) => accessOptions.find(o => o.value === m)?.title ?? m

/**
 * The panel's headline: who can open the app now, as the cluster enforces it
 * (`current.access`, the same answer Inicio gives), never the saved mode
 * merely because it is saved. With nothing online it says what publishing
 * will apply; when it cannot be told it says nothing.
 */
export function accessHeadline(settings: EnvironmentSettings | undefined, current: CurrentRelease | undefined): string | undefined {
  if (current?.access) return `Ahora: ${accessLabel(current.access.mode).toLowerCase()}.`
  if (settings && current && !current.release) return `Al publicarla: ${accessLabel(settings.access.mode).toLowerCase()}.`
  return undefined
}

/**
 * True while the cluster has not been told the settings in force: the latest
 * revision is newer than the one the last apply handed it
 * (`applied_settings_revision`, which a deployment that fails afterwards keeps:
 * the app is not reopened by itself). With nothing online the next
 * publication carries them.
 */
export function accessPending(settings: EnvironmentSettings | undefined, current: CurrentRelease | undefined): boolean {
  if (!settings || !current?.release) return false
  const applied = current.applied_settings_revision ?? -1
  return settings.revision > applied
}

/** The deployment that is applying a change of access right now, if any. */
export const accessApplying = (current: CurrentRelease | undefined) => current?.active?.reason === 'config'

/**
 * What the panel says besides the options, most specific first. Each one is a
 * different situation:
 *  - `applying`: the config deployment is running ("Aplicando el cambio…").
 *  - `cluster`: the cluster cannot ask for a sign-in yet (only public works).
 *  - `login`: this server has no sign-in configured: the app publishes, nobody can sign in.
 *  - `pending`: the change has not reached the app and nothing is on its way.
 */
export type AccessNotice = 'applying' | 'cluster' | 'login' | 'pending' | undefined

export function accessNotice(settings: EnvironmentSettings | undefined, current: CurrentRelease | undefined, chosen: AccessMode | undefined): AccessNotice {
  if (!settings) return undefined
  if (accessApplying(current)) return 'applying'
  if (settings.access_protection_available === false && settings.access.mode !== 'public') return 'cluster'
  const mode = chosen ?? settings.access.mode
  if (settings.access_protection_available === false) return undefined
  if (settings.access_login_available === false && mode !== 'public') return 'login'
  if (current?.active) return undefined
  return accessPending(settings, current) && settings.access.mode !== 'public' ? 'pending' : undefined
}

export const noticeText = {
  applying: { title: 'Aplicando el cambio…', body: 'Estamos avisando a la app. Puede tardar un momento; mientras tanto sigue como estaba.' },
  cluster: {
    title: 'Este clúster todavía no puede pedir inicio de sesión: TI debe habilitarlo.',
    body: 'Mientras tanto, solo puedes dejarla abierta a cualquiera con el enlace.',
  },
  login: { title: 'La app se publica, pero nadie podrá iniciar sesión hasta que TI lo configure.', body: 'Pide a TI que habilite el inicio de sesión de Odicea en este servidor.' },
  pending: { title: 'Este cambio aún no llega a la app.', body: 'Se aplica con la próxima publicación, o al volver a publicar la versión actual.' },
} as const

/**
 * Whether an option can be picked. A protected mode needs a cluster that can
 * ask for the sign-in; `public` needs its own permission (unless it already is
 * the saved mode, which is only displayed).
 */
export function optionDisabled(mode: AccessMode, o: { canWrite: boolean; canPublic: boolean; saved: AccessMode | undefined; protectionAvailable: boolean }): boolean {
  if (!o.canWrite) return true
  if (mode !== 'public' && !o.protectionAvailable) return true
  return !canChoose(mode, o.canPublic) && o.saved !== 'public'
}

/** Whether the person can choose the mode: `public` needs a permission of its own. */
export const canChoose = (mode: AccessMode, canPublic: boolean) => mode !== 'public' || canPublic
