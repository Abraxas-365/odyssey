import { Archive, Database, FolderOpen, Zap, type LucideIcon } from 'lucide-react'
import type { Resource, ResourceKind } from '@/lib/types'

/** Same limits as the API (pkg/resourcekind, internal/resource). */
export const MAX_NAME = 30
export const MAX_PREFIX = 40
export const MIN_GI = 1
export const MAX_GI = 100
export const MAX_RESOURCES = 10
export const DEFAULT_GI = 1

const NAME = /^[a-z]([a-z0-9-]*[a-z0-9])?$/
const PREFIX = /^[A-Z]([A-Z0-9_]*[A-Z0-9])?$/

/** The kinds in plain words; `technical` is for the details toggle. */
export const kindText: Record<ResourceKind, { label: string; noun: string; the: string; defaultName: string; help: string; technical: string; sized: boolean; icon: LucideIcon }> = {
  postgres: { label: 'Base de datos', noun: 'La base de datos', the: 'la base de datos', defaultName: 'db', help: 'Para guardar los datos de la app: clientes, pedidos, usuarios.', technical: 'PostgreSQL', sized: true, icon: Database },
  redis: { label: 'Caché', noun: 'La caché', the: 'la caché', defaultName: 'cache', help: 'Guarda datos temporales para que la app responda más rápido.', technical: 'Redis', sized: true, icon: Zap },
  files: { label: 'Archivos', noun: 'El espacio de archivos', the: 'el espacio de archivos', defaultName: 'archivos', help: 'Una carpeta donde la app guarda archivos que deben conservarse.', technical: 'Volumen persistente', sized: true, icon: FolderOpen },
  'object-storage': { label: 'Almacenamiento de objetos', noun: 'El almacenamiento de objetos', the: 'el almacenamiento de objetos', defaultName: 'objetos', help: 'Un bucket de la empresa donde la app guarda y lee archivos.', technical: 'S3 (bucket existente)', sized: false, icon: Archive },
}

export type StateTone = 'success' | 'warning' | 'destructive' | 'info' | 'secondary'

/** What a resource is doing, in one badge ("Preparando…", "Lista", "Falló", "Eliminando…"); one the company already has is connected, not prepared or erased. */
export function stateOf(r: Pick<Resource, 'status' | 'phase' | 'message'> & { existing?: boolean }): { label: string; tone: StateTone; busy: boolean } {
  const going = r.existing ? 'Quitando…' : 'Eliminando…'
  if (r.status === 'deleting') return { label: going, tone: 'secondary', busy: true }
  switch (r.phase) {
    case 'ready':
      return { label: 'Lista', tone: 'success', busy: false }
    case 'failed':
      return { label: 'Falló', tone: 'destructive', busy: false }
    case 'deleted':
      return { label: going, tone: 'secondary', busy: true }
    default:
      return { label: r.existing ? 'Conectando…' : 'Preparando…', tone: 'info', busy: true }
  }
}

/**
 * Why a resource failed, for the person using the app: plain Spanish by kind,
 * never the agent's own (English) message. That one is `detail`, shown only
 * with the technical-details toggle. Undefined when the resource did not fail
 * (or is being erased). One the company already has failed to connect (a
 * store or a key IT set up), so it is IT's to fix.
 */
export function failureOf(r: Pick<Resource, 'kind' | 'status' | 'phase' | 'message'> & { existing?: boolean }): { text: string; detail?: string } | undefined {
  if (r.status !== 'active' || r.phase !== 'failed') return undefined
  const text = r.existing
    ? `No se pudo conectar ${kindText[r.kind].the} de la empresa. Avísale a TI: la conexión la configura TI.`
    : `No se pudo preparar ${kindText[r.kind].the}.`
  return { text, detail: r.message || undefined }
}

/**
 * The resource's kind and size in one line ("Base de datos · 5 GB"); one the
 * company already has has no size: it says Odicea only connects it.
 */
export const sizeLine = (r: Pick<Resource, 'kind' | 'storage_gi' | 'existing'>) =>
  r.existing ? `${kindText[r.kind].label} · Ya existe en la empresa` : kindText[r.kind].sized ? `${kindText[r.kind].label} · ${sizeLabel(r.storage_gi)}` : kindText[r.kind].label

/** Only what Odicea created has a size to change. */
export const resizable = (r: Pick<Resource, 'kind' | 'existing'>) => !r.existing && kindText[r.kind].sized

/**
 * What deleting says: a resource the company already has is only
 * disconnected and dropped from the app (its data stays where it is, so no
 * typed confirmation); any other is erased for good.
 */
export function deleteText(r: Pick<Resource, 'kind' | 'existing'>): { verb: string; body: string; confirm: string; busy: string; typed: boolean } {
  const t = kindText[r.kind]
  return r.existing
    ? { verb: 'Quitar', body: `Se desconecta y sale de esta app. ${t.noun} de la empresa no se toca: sus datos siguen donde están.`, confirm: 'Quitar de la app', busy: 'Quitando…', typed: false }
    : { verb: 'Eliminar', body: `${t.noun} y todo lo que guarda se borran para siempre. No hay forma de recuperarlos.`, confirm: 'Eliminar para siempre', busy: 'Eliminando…', typed: true }
}

/** Only an active resource takes changes; one being erased takes none. */
export const isActive = (r: Pick<Resource, 'status'>) => r.status === 'active'

/** The problem with a new resource's name, in plain words (undefined: fine). */
export function nameProblem(name: string, taken: string[]): string | undefined {
  const n = name.trim()
  if (!n) return undefined
  if (n.length > MAX_NAME) return `Usa ${MAX_NAME} caracteres como máximo.`
  if (!NAME.test(n)) return 'Usa minúsculas, números y guiones, y empieza con una letra.'
  if (taken.includes(n)) return 'Ya hay un recurso con este nombre.'
  return undefined
}

/** The prefix is optional; empty is fine. */
export function prefixProblem(prefix: string): string | undefined {
  const p = prefix.trim()
  if (!p) return undefined
  if (p.length > MAX_PREFIX) return `Usa ${MAX_PREFIX} caracteres como máximo.`
  if (!PREFIX.test(p)) return 'Usa MAYÚSCULAS, números y _, y empieza con una letra (por ejemplo ANALYTICS).'
  return undefined
}

/** Size in GiB: a whole number in range; when resizing it cannot go below the current size. */
export function sizeProblem(gi: number, current?: number): string | undefined {
  if (!Number.isInteger(gi) || gi < MIN_GI || gi > MAX_GI) return `El tamaño va de ${MIN_GI} a ${MAX_GI} GB.`
  if (current !== undefined && gi < current) return `Solo puede crecer: ahora tiene ${current} GB.`
  return undefined
}

export const sizeLabel = (gi: number) => `${gi} GB`

/** A name for a new resource of this kind that is not taken (db, db-2, db-3…). */
export function suggestName(kind: ResourceKind, taken: string[]): string {
  const base = kindText[kind].defaultName
  if (!taken.includes(base)) return base
  for (let i = 2; ; i++) if (!taken.includes(`${base}-${i}`)) return `${base}-${i}`
}

/** Deleting is confirmed by typing the resource's name exactly. */
export const deleteConfirmed = (typed: string, name: string) => typed.trim() === name

/** What the delete button may do: nothing if the app still uses it (the API refuses it too). */
export function deleteBlock(r: Pick<Resource, 'connected' | 'status'> & { existing?: boolean }): string | undefined {
  if (r.status === 'deleting') return r.existing ? 'Ya se está quitando.' : 'Ya se está eliminando.'
  if (r.connected) return 'La app la está usando. Desconéctala primero.'
  return undefined
}

export const atLimit = (count: number) => count >= MAX_RESOURCES
