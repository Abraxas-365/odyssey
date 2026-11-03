import { CloudUpload, Hammer, HeartPulse, Rocket } from 'lucide-react'
import type { AppStatusKind, ProgressStepItem, VersionData } from '@/components/odyssey'
import { formatBytes, formatRelative } from '@/lib/format'
import type { CurrentRelease, Deployment, DeploymentStep } from '@/lib/types'

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

export function appStatus(archived: boolean, current: CurrentRelease | undefined, latest: Deployment | undefined): { status: AppStatusKind; label?: string } {
  if (archived) return { status: 'archived' }
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
const inProgress = new Set<Deployment['status']>(['building', 'pending', 'applying', 'verifying'])

export function lastProblem(deployments: Deployment[]): Deployment | undefined {
  const last = deployments.find(d => d.reason !== 'auto_rollback' && d.status !== 'cancelled' && !inProgress.has(d.status))
  return last?.status === 'failed' ? last : undefined
}

const reasonText: Record<Deployment['reason'], string> = {
  deploy: 'Nueva versión',
  rollback: 'Volviste a una versión anterior',
  auto_rollback: 'Restaurada automáticamente',
}

/** What a version is called: one from GitHub, its commit title; one from a folder, which folder. */
export function versionMessage(d: Deployment): string {
  if (d.reason === 'deploy' && d.source?.git) return d.source.git.title.trim() || 'Cambios desde GitHub'
  if (d.reason === 'deploy' && d.source) return `Desde la carpeta ${d.source.name} · ${formatBytes(d.source.size)}`
  return reasonText[d.reason]
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
 */
export function versions(deployments: Deployment[], current: CurrentRelease | undefined, me: string, now = Date.now()): VersionItem[] {
  const live = current?.deployment?.id
  return deployments
    .filter(d => d.status === 'ready' || d.status === 'failed')
    .map(d => {
      const kind = d.status === 'failed' ? 'failed' : d.id === live ? 'live' : 'previous'
      return {
        id: d.id,
        deployment: d,
        kind,
        message: versionMessage(d),
        when: formatRelative(d.finished_at ?? d.created_at, now),
        by: d.requested_by === '' ? 'la plataforma' : d.requested_by === me ? 'ti' : 'tu equipo',
        sha: d.image.split('@sha256:')[1] || undefined,
        git: gitOf(d),
        restorable: kind === 'previous' && d.release_id !== current?.release?.id,
      }
    })
}

/** One-line explanation of a failed deployment, for its callout. */
export function failureText(d: Deployment): string {
  return d.error?.message || 'La nueva versión no se pudo publicar.'
}
