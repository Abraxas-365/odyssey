import type { DetectedService, Service, ServiceKind, ServiceSpec, ServiceState } from '@/lib/types'

/**
 * An app's services in plain words (MS-6). D3: an app of one service reads
 * exactly as before; the console says "servicios" only once there are two.
 * Same limits as the API (internal/project/service.go).
 */
export const MAX_SERVICES = 8
export const MAX_ROOT = 200
export const MAX_ROUTE = 100
const NAME = /^[a-z][a-z0-9]{0,19}$/
const ROUTE = /^(\/[A-Za-z0-9._~-]+)+$/
const RESERVED = '/_odicea'

/** Two or more services: only then does the console talk about them. */
export const isMulti = (services: readonly unknown[] | undefined | null): boolean => (services?.length ?? 0) >= 2

export const kindText: Record<ServiceKind, { label: string; help: string }> = {
  web: { label: 'Web', help: 'Responde páginas o una API.' },
  worker: { label: 'En segundo plano', help: 'Trabaja sin dirección: procesa una cola, tareas programadas.' },
}

/** Where a service answers, in words: "/api" on the app's address, internal, or none (a worker). */
export function whereText(s: Pick<Service, 'kind' | 'route' | 'strip_prefix'>): string {
  if (s.kind === 'worker') return 'Sin dirección: trabaja en segundo plano'
  if (!s.route) return 'Solo la ven las otras partes de la app'
  if (s.route === '/') return 'La dirección principal de la app'
  return `En ${s.route}${s.strip_prefix ? ' (recibe la ruta sin ese prefijo)' : ''}`
}

/** A row of the editor: what PUT /services takes, kept as typed. */
export interface Draft {
  name: string
  kind: ServiceKind
  root: string
  route: string
  strip_prefix: boolean
}

export const draftOf = (s: Pick<Service, 'name' | 'kind' | 'root' | 'route' | 'strip_prefix'>): Draft => ({ name: s.name, kind: s.kind, root: s.root, route: s.route, strip_prefix: s.strip_prefix })

export const fromDetected = (d: DetectedService): Draft => draftOf(d)

/** A new row: the first free name, a web at /<name> (or at / when no one has it). */
export function newDraft(rows: Draft[]): Draft {
  const taken = new Set(rows.map(r => r.name))
  const name = ['web', 'api', 'worker', 'jobs'].find(n => !taken.has(n)) ?? Array.from({ length: MAX_SERVICES + 1 }, (_, i) => `svc${i + 1}`).find(n => !taken.has(n))!
  const rootTaken = rows.some(r => normalizeRoute(r.route) === '/')
  return { name, kind: 'web', root: '', route: rootTaken ? `/${name}` : '/', strip_prefix: rootTaken }
}

export const normalizeRoot = (root: string) => root.trim().replace(/^\/+|\/+$/g, '')
export function normalizeRoute(route: string): string {
  const r = route.trim()
  if (r.length <= 1) return r
  return r.replace(/\/+$/, '') || '/'
}

/** What PUT /services gets: normalized, a worker without route or prefix. */
export function bodyOf(rows: Draft[]): ServiceSpec[] {
  return rows.map(r => {
    const worker = r.kind === 'worker'
    const route = worker ? '' : normalizeRoute(r.route)
    return { name: r.name.trim(), kind: r.kind, root: normalizeRoot(r.root), route, strip_prefix: !worker && route !== '' && route !== '/' && r.strip_prefix }
  })
}

export type Problems = Partial<Record<'name' | 'root' | 'route', string>>

/** The problems of each row, in plain words (empty objects: fine). The API decides the same. */
export function problemsOf(rows: Draft[]): Problems[] {
  const body = bodyOf(rows)
  const names = new Map<string, number>()
  const routes = new Map<string, number>()
  for (const s of body) {
    names.set(s.name, (names.get(s.name) ?? 0) + 1)
    if (s.route) routes.set(s.route, (routes.get(s.route) ?? 0) + 1)
  }
  return body.map(s => {
    const p: Problems = {}
    if (!NAME.test(s.name ?? '')) p.name = 'Usa minúsculas y números (hasta 20), y empieza con una letra.'
    else if ((names.get(s.name) ?? 0) > 1) p.name = 'Ya hay otra parte con este nombre.'
    const root = s.root ?? ''
    if (root.length > MAX_ROOT || root.split('/').some(part => root !== '' && (part === '' || part === '.' || part === '..')) || /[\\\0]/.test(root)) p.root = 'Escribe una carpeta del código, como apps/web.'
    const route = s.route ?? ''
    if (route && route !== '/') {
      if (route.length > MAX_ROUTE || !ROUTE.test(route) || route.split('/').some(seg => seg === '.' || seg === '..')) p.route = 'Usa / o una ruta como /api (letras, números, . _ ~ -).'
      else if (route === RESERVED || route.startsWith(RESERVED + '/')) p.route = 'Las rutas bajo /_odicea son de la plataforma.'
    }
    if (!p.route && route && (routes.get(route) ?? 0) > 1) p.route = 'Otra parte ya responde en esta ruta.'
    return p
  })
}

export const hasProblems = (ps: Problems[]) => ps.some(p => Object.keys(p).length > 0)

/** The list is just what any app has (one web at / with all of the code): nothing to talk about. */
export const isSimple = (rows: Pick<Draft, 'kind' | 'root' | 'route' | 'strip_prefix'>[]) =>
  rows.length === 1 && rows[0].kind === 'web' && normalizeRoot(rows[0].root) === '' && normalizeRoute(rows[0].route) === '/' && !rows[0].strip_prefix

/** Same list as saved (order included): saving it would write nothing. */
export function sameList(rows: Draft[], saved: Pick<Service, 'name' | 'kind' | 'root' | 'route' | 'strip_prefix'>[]): boolean {
  const a = bodyOf(rows)
  return a.length === saved.length && a.every((s, i) => s.name === saved[i].name && s.kind === saved[i].kind && s.root === saved[i].root && s.route === saved[i].route && s.strip_prefix === saved[i].strip_prefix)
}

/**
 * How one part stands, in one badge. A worker is "Funcionando" (it has no
 * address to answer on); "" is the cluster not having said yet.
 */
export function stateText(s: Pick<ServiceState, 'kind' | 'phase' | 'reason'>): { label: string; tone: 'success' | 'info' | 'destructive' | 'secondary' } {
  switch (s.phase) {
    case 'ready': return { label: s.kind === 'worker' ? 'Funcionando' : 'En línea', tone: 'success' }
    case 'progressing': return { label: 'Arrancando…', tone: 'info' }
    case 'failed': return { label: failureWords[s.reason || 'unhealthy'], tone: 'destructive' }
    default: return { label: 'Sin noticias todavía', tone: 'secondary' }
  }
}

const failureWords: Record<Exclude<ServiceState['reason'], ''>, string> = {
  unhealthy: 'No responde',
  image_pull: 'No se pudo descargar',
  crash: 'Se detiene al arrancar',
  config: 'Falta configuración',
  resource: 'Espera un recurso',
}

/**
 * What a version changed, for its line in Historial: undefined when there is
 * nothing to say (an app of one service, or a version from before services
 * were tracked); else the services that appear, go away or run new code.
 */
export function changedText(changed: string[] | null | undefined, services: number): string | undefined {
  if (!changed || services < 2) return undefined
  if (changed.length === 0) return 'Sin cambios en el código'
  if (changed.length === 1) return `Cambió: ${changed[0]}`
  return `Cambiaron: ${changed.slice(0, -1).join(', ')} y ${changed[changed.length - 1]}`
}
