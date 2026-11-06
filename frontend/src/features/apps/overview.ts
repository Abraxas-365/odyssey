import type { CurrentRelease, GitHubPush } from '@/lib/types'
import { pushActive } from '@/lib/github'

/**
 * App → Resumen (UX v4 §5.3): one primary action, chosen by the app's state
 * instead of a "Publicar ▾" menu.
 *
 * - `first`: nothing online yet; the empty state shows the three ways in
 *   (assistant, GitHub, folder) and the header shows no button of its own.
 * - `github`: the connected branch has a commit that is not online.
 * - `assistant`: a version is online; "Subir una carpeta" stays secondary.
 * - `none`: publishing now (the progress replaces the hero), archived, or the
 *   person cannot publish.
 */
export type PrimaryAction = 'none' | 'first' | 'github' | 'assistant'

export interface OverviewState {
  archived: boolean
  canPublish: boolean
  publishing: boolean
  live: boolean
  /** A repository is connected and its newest commit is not online. */
  unpublishedCommit: boolean
}

export function primaryAction(s: OverviewState): PrimaryAction {
  if (s.archived || !s.canPublish || s.publishing) return 'none'
  if (s.unpublishedCommit) return 'github'
  return s.live ? 'assistant' : 'first'
}

/**
 * The newest commit GitHub told about is not the one online, and nothing is
 * importing it: "Publicar ahora desde GitHub" would publish it. A push that
 * failed counts (publishing again is the way out); one still on its way does
 * not (it publishes itself).
 */
export function hasUnpublishedCommit(last: GitHubPush | undefined, current: CurrentRelease | undefined): boolean {
  if (!last || pushActive(last.status)) return false
  const online = current?.release ? current.deployment?.source?.git?.commit : undefined
  return online !== last.git.commit
}

/**
 * What to tell the assistant. With GitHub connected the code goes through the
 * repository (the app publishes itself); otherwise the assistant uploads the
 * folder with `deploy_folder`.
 */
export function assistantPrompt(slug: string, { live, github }: { live: boolean; github: boolean }): string {
  if (github) return `Sube los cambios de esta carpeta a GitHub; la app ${slug} de Odicea se publica sola.`
  return live ? `Publica los cambios de esta carpeta en la app ${slug} de Odicea.` : `Publica esta carpeta en la app ${slug} de Odicea.`
}
