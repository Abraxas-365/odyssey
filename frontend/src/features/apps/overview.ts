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

/** A new app from an idea (UX v4 §5.6): build it here, then publish it to the app just created. */
export function buildPrompt(slug: string, idea: string): string {
  const what = idea.trim().replace(/[.\s]+$/, '')
  return what
    ? `Construye en esta carpeta: ${what}. Cuando funcione, publícala en la app ${slug} de Odicea.`
    : `Construye en esta carpeta la app ${slug}. Cuando funcione, publícala en la app ${slug} de Odicea.`
}

/** "Pedir a mi asistente que lo arregle" for a version that did not go online. */
export function fixPrompt(slug: string): string {
  return `La última versión de la app ${slug} de Odicea no se publicó. Revisa qué pasó, corrígelo en esta carpeta y vuelve a publicarla.`
}

// Words that open an idea without naming it ("Quiero una app para que los…").
const leading = new Set(['quiero', 'queremos', 'necesito', 'necesitamos', 'me', 'gustaría', 'gustaria', 'crear', 'hacer', 'construir', 'armar', 'tener',
  'una', 'un', 'app', 'aplicación', 'aplicacion', 'sistema', 'herramienta', 'web', 'página', 'pagina', 'portal', 'para', 'que', 'donde', 'en',
  'la', 'el', 'los', 'las', 'de', 'del', 'mi', 'mis', 'nuestro', 'nuestra', 'nuestros', 'nuestras', 'poder', 'se', 'pueda', 'puedan'])
// Words a name never ends on ("Tablero de ventas por" → "Tablero de ventas").
const trailing = new Set(['a', 'al', 'de', 'del', 'la', 'el', 'los', 'las', 'y', 'o', 'con', 'para', 'por', 'en', 'que', 'sus', 'su', 'mis', 'mi', 'un', 'una'])

/**
 * A name from "¿Qué quieres crear?" (UX v4 §5.6): the first words that say
 * what it is, without the filler that opens a sentence, up to four words or
 * the first punctuation. The person can always change it.
 */
export function deriveName(idea: string, maxWords = 4): string {
  const words = idea.trim().split(/\s+/).filter(Boolean)
  let i = 0
  while (i < words.length && leading.has(words[i].toLowerCase().replace(/[^\p{L}]/gu, ''))) i++
  const out: string[] = []
  for (; i < words.length && out.length < maxWords; i++) {
    const w = words[i]
    const cut = w.search(/[.,;:!?()]/)
    if (cut === 0) break
    out.push(cut > 0 ? w.slice(0, cut) : w)
    if (cut > 0) break
  }
  while (out.length > 0 && trailing.has(out[out.length - 1].toLowerCase())) out.pop()
  const name = out.join(' ').slice(0, 60)
  return name ? name[0].toLocaleUpperCase('es') + name.slice(1) : ''
}
