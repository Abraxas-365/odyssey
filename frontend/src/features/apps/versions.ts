import { CloudUpload, Hammer, HeartPulse, Rocket } from 'lucide-react'
import type { AppStatusKind, ProgressStepItem, VersionData } from '@/components/odyssey'
import { formatBytes, formatRelative } from '@/lib/format'
import type { CurrentRelease, Deployment, DeploymentStep } from '@/lib/types'
import { attribution } from './attribution'
import { changedText } from './services'

/**
 * Plain-language view of releases (UX v3 §1, §7): builders see "versiones"
 * and three steps, never deployments, revisions or digests (those appear
 * only in technical mode).
 */

const stepCopy: Record<DeploymentStep['name'], Omit<ProgressStepItem, 'state'>> = {
  build: { icon: Hammer, title: 'Construyendo tu app', description: 'Convertimos tu carpeta en una app lista para funcionar.', technical: 'build' },
  apply: { icon: CloudUpload, title: 'Preparando tu app', description: 'Enviamos la nueva versión a su servidor.', technical: 'apply' },
  wait_ready: { icon: HeartPulse, title: 'Revisando que funcione', description: 'Esperamos a que responda antes de mostrarla a nadie.', technical: 'wait_ready' },
  promote: { icon: Rocket, title: 'Publicando', description: 'La dejamos en línea en lugar de la anterior.', technical: 'promote' },
}

export function progressSteps(d: Deployment): ProgressStepItem[] {
  return d.steps.map(s => ({ ...stepCopy[s.name], state: s.status === 'skipped' ? 'pending' : s.status }))
}

export const isPublishing = (c: CurrentRelease | undefined) => !!c?.active

/** IT's rules asked for approval before this deployment builds (docs/approvals.md): it has not started. */
export const waitingForIT = (d: Pick<Deployment, 'status'> | null | undefined) => d?.status === 'waiting_approval'

/** The word for a deployment's state where it is listed or opened: "Esperando aprobación de TI". */
export const waitingLabel = 'Esperando aprobación de TI'

export function appStatus(archived: boolean, current: CurrentRelease | undefined, latest: Deployment | undefined): { status: AppStatusKind; label?: string } {
  if (archived) return { status: 'archived' }
  if (waitingForIT(current?.active)) return { status: 'waiting', label: waitingLabel }
  if (current?.active) return { status: 'building' }
  if (current?.release) return { status: 'live' }
  if (latest?.status === 'failed') return { status: 'attention', label: 'No se publicó' }
  return { status: 'paused', label: 'Sin publicar' }
}

/**
 * The last thing someone asked to publish, if it failed. Automatic restores
 * and replaced attempts don't count: after a failure the platform's restore
 * is newer, but the person's last attempt is still the failed one.
 */
const inProgress = new Set<Deployment['status']>(['building', 'waiting_approval', 'pending', 'applying', 'verifying'])

export function lastProblem(deployments: Deployment[]): Deployment | undefined {
  const last = deployments.find(d => d.reason !== 'auto_rollback' && d.status !== 'cancelled' && !inProgress.has(d.status))
  return last?.status === 'failed' ? last : undefined
}

/**
 * A failed `config` deployment that got past `apply` already handed the new
 * access to the cluster: the app is NOT reopened by itself (fail-closed). One
 * refused at `apply` never reached it and the app stays as it was.
 */
export const configReachedApp = (d: Deployment) => d.reason === 'config' && (d.step === 'wait_ready' || d.step === 'promote')

const reasonText: Record<Deployment['reason'], string> = {
  deploy: 'Nueva versión',
  rollback: 'Volviste a una versión anterior',
  auto_rollback: 'Restaurada automáticamente',
  config: 'Cambió quién puede abrirla',
}

/**
 * What a version is called (UX v4 §5.3): one from GitHub, its commit title;
 * else what whoever asked said changed (`message`: the assistant says what it
 * did); else a folder upload is "Subida desde una carpeta". Restores and
 * access changes keep their own phrase. Which folder and its size are
 * technical (`sourceDetail`).
 */
export function versionMessage(d: Deployment): string {
  if (d.reason !== 'deploy') return reasonText[d.reason]
  if (d.source?.git) return d.source.git.title.trim() || 'Cambios desde GitHub'
  if (d.message.trim()) return d.message.trim()
  if (d.source) return 'Subida desde una carpeta'
  return reasonText.deploy
}

/** Technical mode: the uploaded folder and its size ("carpeta visitas · 4.2 KB"). */
export function sourceDetail(d: Deployment): string | undefined {
  if (d.reason !== 'deploy' || !d.source || d.source.git) return undefined
  return `carpeta ${d.source.name} · ${formatBytes(d.source.size)}`
}

/** The commit a version came from (GitHub), for the row's `GitLine`. */
function gitOf(d: Deployment): VersionData['git'] {
  const g = d.source?.git
  return g && d.reason === 'deploy' ? { branch: g.branch, sha: g.commit, author: g.author } : undefined
}

export interface VersionItem extends VersionData {
  /** Offer "Volver a esta" (a previous version that went live, not the one online now). */
  restorable: boolean
  deployment: Deployment
}

/**
 * Versiones, newest first. In-progress and replaced (cancelled) attempts are
 * left out: the first shows as progress, the second never mattered to anyone.
 * One waiting for IT is listed ("Esperando aprobación de TI"): it is the only
 * in-progress one that has not started anything yet.
 */
export function versions(deployments: Deployment[], current: CurrentRelease | undefined, me: string, now = Date.now()): VersionItem[] {
  return deployments
    .filter(d => d.status === 'ready' || d.status === 'failed' || d.status === 'waiting_approval')
    .map(d => versionItem(d, current, me, now))
}

/** The version online now, as the hero shows it (from the current release, not the history page). */
export function liveVersion(current: CurrentRelease | undefined, me: string, now = Date.now()): VersionItem | undefined {
  return current?.release && current.deployment ? versionItem(current.deployment, current, me, now) : undefined
}

function versionItem(d: Deployment, current: CurrentRelease | undefined, me: string, now: number): VersionItem {
  const live = current?.deployment?.id
  const kind = d.status === 'waiting_approval' ? 'waiting' : d.status === 'failed' ? 'failed' : d.id === live ? 'live' : 'previous'
  const { by, channel } = attribution(d, me)
  return {
    id: d.id,
    deployment: d,
    kind,
    message: versionMessage(d),
    when: formatRelative(d.finished_at ?? d.created_at, now),
    by,
    channel,
    sha: d.image.split('@sha256:')[1] || undefined,
    source: sourceDetail(d),
    git: gitOf(d),
    changed: changedText(d.changed, current?.release?.services.length ?? 0),
    restorable: kind === 'previous' && d.release_id !== current?.release?.id,
  }
}

/** "Volver a la anterior": the newest other release that went live, if any. */
export const previousVersion = (items: VersionItem[]) => items.find(v => v.restorable)

/** One-line explanation of a failed deployment, for its callout. */
export function failureText(d: Deployment): string {
  return d.error?.message || 'La nueva versión no se pudo publicar.'
}
