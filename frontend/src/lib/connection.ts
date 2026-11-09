import type { CatalogEntry, CatalogKind, CatalogKindVariable, CatalogStore, ExistingConnection } from './types'

/**
 * IT's form for the connection of a resource the company already has
 * (docs/catalog.md «The connection»): for each variable of the kind's
 * contract (GET /catalog/kinds, never a copy here), a written value, a
 * reference to a secret store, or nothing when the variable is optional. A
 * credential is only ever read from a store; a derived variable is built by
 * the cluster. The server validates again and has the last word: this only
 * says the problem in plain words before sending.
 */

/** Where a variable comes from: written here, read from a secret store, or not given (optional only). */
export type Source = 'value' | 'store' | 'none'

export interface Row {
  source: Source
  value: string
  store: string
  key: string
  property: string
}

/** The variables IT gives (a derived one is built by the cluster). */
export const givenOf = (k: Pick<CatalogKind, 'variables'>) => k.variables.filter(v => !v.derived)

/** The ways a variable can be given, in the order the form offers them. */
export function sourcesOf(v: CatalogKindVariable): Source[] {
  const out: Source[] = v.secret ? ['store'] : v.choices.length > 0 ? ['value'] : ['value', 'store']
  return v.optional ? [...out, 'none'] : out
}

export const sourceLabel: Record<Source, string> = { value: 'Escribirlo', store: 'Del almacén de secretos', none: 'No usar' }

const empty = (source: Source, store: string): Row => ({ source, value: '', store, key: '', property: '' })

/**
 * The form's rows, one per given variable: from a published connection when
 * there is one (a new version starts from the current), otherwise each
 * variable's first way (an optional one starts unused). `store` is the store
 * a new reference starts on.
 */
export function connectionRows(kind: CatalogKind, existing: ExistingConnection | undefined, store: string): Record<string, Row> {
  const rows: Record<string, Row> = {}
  for (const v of givenOf(kind)) {
    const had = existing?.values.find(x => x.variable === v.name)
    if (had?.ref) rows[v.name] = { source: 'store', value: '', store: had.ref.store, key: had.ref.key, property: had.ref.property ?? '' }
    else if (had?.value !== undefined && had.value !== '') rows[v.name] = { ...empty('value', store), value: had.value }
    else rows[v.name] = empty(v.optional ? 'none' : sourcesOf(v)[0], store)
  }
  return rows
}

/** The connection the API takes (CatalogRecipeDraft.existing), in the contract's order, trimmed. */
export function connectionOf(kind: CatalogKind, rows: Record<string, Row>): ExistingConnection {
  const values: ExistingConnection['values'] = []
  for (const v of givenOf(kind)) {
    const r = rows[v.name]
    if (!r || r.source === 'none') continue
    if (r.source === 'value') values.push({ variable: v.name, value: r.value.trim() })
    else values.push({ variable: v.name, ref: { store: r.store, key: r.key.trim(), ...(r.property.trim() ? { property: r.property.trim() } : {}) } })
  }
  return { values }
}

/** Two connections say the same, whatever the order of their variables. */
export function sameConnection(a: ExistingConnection | undefined, b: ExistingConnection | undefined): boolean {
  const key = (c: ExistingConnection | undefined) =>
    (c?.values ?? [])
      .map(v => JSON.stringify([v.variable, v.value ?? null, v.ref?.store ?? null, v.ref?.key ?? null, v.ref?.property || null]))
      .sort()
      .join('\n')
  return key(a) === key(b)
}

// The server's charsets and bounds (pkg/resourcespec); lengths there are bytes.
const MAX_VALUE = 255
const MAX_KEY = 255
const MAX_PROPERTY = 128
const KEY = /^[A-Za-z0-9][A-Za-z0-9._/:@+=-]*$/
const PROPERTY = /^[A-Za-z0-9][A-Za-z0-9._-]*$/
const LABEL = /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$/
const IPV4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/
const bytes = (s: string) => new TextEncoder().encode(s).length

/**
 * An IPv6 address as net.ParseIP reads it, in 8 groups (undefined: not one).
 * A trailing IPv4 (::ffff:a.b.c.d) counts as its two last groups.
 */
function ipv6(s: string): number[] | undefined {
  let l = s.toLowerCase()
  const tail = /^(.*:)(\d+\.\d+\.\d+\.\d+)$/.exec(l)
  if (tail) {
    if (!IPV4.test(tail[2])) return undefined
    const [a, b, c, d] = tail[2].split('.').map(Number)
    l = `${tail[1]}${(a * 256 + b).toString(16)}:${(c * 256 + d).toString(16)}`
  }
  const halves = l.split('::')
  if (halves.length > 2) return undefined
  const groups = (h: string) => (h === '' ? [] : h.split(':'))
  const [head, rest] = [groups(halves[0]), halves.length === 2 ? groups(halves[1]) : undefined]
  const all = [...head, ...(rest ?? [])]
  if (!all.every(p => /^[0-9a-f]{1,4}$/.test(p))) return undefined
  if (rest === undefined ? all.length !== 8 : all.length > 7) return undefined
  const n = (p: string) => parseInt(p, 16)
  return rest === undefined ? all.map(n) : [...head.map(n), ...Array(8 - all.length).fill(0), ...rest.map(n)]
}

/** The IPv4 inside an IPv4-mapped IPv6 address (::ffff:a.b.c.d), as Go's To4 finds it. */
const mappedV4 = (g: number[]) =>
  g.slice(0, 5).every(x => x === 0) && g[5] === 0xffff ? [g[6] >> 8, g[6] & 255, g[7] >> 8, g[7] & 255].join('.') : undefined

/** net.IP's loopback, link-local, multicast and unspecified, like the server refuses. */
function v4Refused(s: string): boolean {
  const [a, b] = s.split('.').map(Number)
  return a === 127 || (a === 169 && b === 254) || (a >= 224 && a <= 239) || s === '0.0.0.0'
}

/**
 * A host the connection may point to (resourcespec.host): a private address
 * or an internal name is fine; never this machine, the cloud's metadata
 * service, a name of one label, a number, or a name of the cluster itself.
 */
export function hostOk(s: string): boolean {
  if (IPV4.test(s)) return !v4Refused(s)
  if (s.includes(':')) {
    const g = ipv6(s)
    if (!g) return false
    const v4 = mappedV4(g)
    if (v4) return !v4Refused(v4)
    const unspecified = g.every(x => x === 0)
    const loopback = g.slice(0, 7).every(x => x === 0) && g[7] === 1
    return !unspecified && !loopback && (g[0] & 0xffc0) !== 0xfe80 && g[0] >> 8 !== 0xff
  }
  const l = s.toLowerCase()
  const labels = l.split('.')
  if (s.length > 253 || labels.length < 2 || !labels.every(x => LABEL.test(x))) return false
  const last = labels[labels.length - 1]
  if (/^\d+$/.test(last) || /^0x[0-9a-f]+$/.test(last) || l === 'metadata.google.internal') return false
  return !['.svc', '.cluster.local', '.localhost'].some(inside => l.endsWith(inside) || l.includes(inside + '.'))
}

function urlOk(s: string): boolean {
  let u: URL
  try {
    u = new URL(s)
  } catch {
    return false
  }
  return (u.protocol === 'https:' || u.protocol === 'http:') && hostOk(u.hostname.replace(/^\[|\]$/g, '')) && !u.username && !u.password && !u.search && !u.hash && !s.includes('?') && !s.includes('#')
}

/** What is wrong with a written value of v, in plain words (undefined: fine). */
export function valueProblem(v: CatalogKindVariable, s: string): string | undefined {
  const what = meaningOf(v).what
  if (!s) return v.choices.length > 0 ? `Elige ${what}.` : v.optional ? `Escribe ${what}, o elige «No usar».` : `Escribe ${what}.`
  // the server counts bytes and refuses any character that is not printable (Go's unicode.IsPrint: only the ASCII space)
  if (bytes(s) > MAX_VALUE || s.trim() !== s || /[\p{C}\p{Zl}\p{Zp}]|(?! )\p{Zs}/u.test(s)) return `Hasta ${MAX_VALUE} caracteres, sin espacios al inicio ni al final.`
  if (v.choices.length > 0 && !v.choices.includes(s)) return `Elige uno de: ${v.choices.join(', ')}.`
  switch (v.format) {
    case 'host':
      return hostOk(s) ? undefined : 'Escribe un nombre como db.empresa.com o una dirección IP. No vale localhost ni un nombre interno del clúster.'
    case 'port':
      return /^[1-9]\d{0,4}$/.test(s) && Number(s) <= 65535 ? undefined : 'Un número del 1 al 65535.'
    case 'url':
      return urlOk(s) ? undefined : 'Una dirección como https://s3.empresa.com, sin usuario ni parámetros.'
    default:
      return undefined
  }
}

/** What is wrong with one row, in plain words (undefined: fine). */
export function rowProblem(v: CatalogKindVariable, r: Row | undefined): string | undefined {
  if (!r || !sourcesOf(v).includes(r.source)) return v.secret ? 'Es una credencial: se lee del almacén de secretos.' : `Elige de dónde sale ${meaningOf(v).what}.`
  if (r.source === 'none') return undefined
  if (r.source === 'value') return valueProblem(v, r.value.trim())
  if (!r.store) return 'Elige el almacén de secretos.'
  const key = r.key.trim()
  if (!key) return 'Escribe la llave del secreto en el almacén.'
  if (key.length > MAX_KEY || !KEY.test(key)) return 'La llave usa letras, números y . _ / : @ + = - (sin espacios).'
  const property = r.property.trim()
  if (property && (property.length > MAX_PROPERTY || !PROPERTY.test(property))) return 'La propiedad usa letras, números y . _ - (sin espacios).'
  return undefined
}

/**
 * The first problem of the whole connection: a row's (with its variable),
 * or optional variables that go together given only in part.
 */
export function connectionProblem(kind: CatalogKind, rows: Record<string, Row>): { variable?: string; text: string } | undefined {
  for (const v of givenOf(kind)) {
    const text = rowProblem(v, rows[v.name])
    if (text) return { variable: v.name, text }
  }
  const used = (v: CatalogKindVariable) => !!rows[v.name] && rows[v.name].source !== 'none'
  for (const v of givenOf(kind)) {
    if (!v.together || used(v)) continue
    const other = givenOf(kind).find(o => o.together === v.together && used(o))
    if (other) return { variable: v.name, text: `${capital(meaningOf(other).what)} y ${meaningOf(v).what} van juntas: usa las dos o ninguna.` }
  }
  return undefined
}

/** The stores a connection reads that no cluster of the organization announces (renamed or gone). */
export function missingStores(rows: Record<string, Row>, stores: readonly Pick<CatalogStore, 'name'>[]): string[] {
  const named = new Set(Object.values(rows).filter(r => r.source === 'store' && r.store).map(r => r.store))
  return [...named].filter(s => !stores.some(x => x.name === s)).sort()
}

/**
 * The clusters whose apps can use the connection: the ones that read every
 * store it names (all of them when it names none), by name.
 */
export function readersOf(rows: Record<string, Row>, stores: readonly CatalogStore[]): { all: boolean; clusters: string[] } {
  const named = [...new Set(Object.values(rows).filter(r => r.source === 'store' && r.store).map(r => r.store))]
  if (named.length === 0) return { all: true, clusters: [] }
  const lists = named.map(n => stores.find(s => s.name === n)?.clusters ?? [])
  const clusters = lists[0].filter(c => lists.every(l => l.some(x => x.id === c.id))).map(c => c.name)
  return { all: false, clusters: [...new Set(clusters)].sort() }
}

/**
 * Where the connection points, for IT's network warning: the written host
 * (or a URL's host) and whether it is an address of a private network, which
 * no cluster's apps reach until IT adds it to the cluster's allowed egress
 * (docs/egress.md: even «internet» keeps private ranges closed). `internal`
 * is undefined for a name (its address is the company's DNS's call).
 */
export function networkTarget(kind: CatalogKind, rows: Record<string, Row>): { address: string; internal?: boolean } | undefined {
  for (const v of givenOf(kind)) {
    const r = rows[v.name]
    if (!r || r.source !== 'value' || (v.format !== 'host' && v.format !== 'url') || valueProblem(v, r.value.trim())) continue
    const address = v.format === 'url' ? new URL(r.value.trim()).hostname.replace(/^\[|\]$/g, '') : r.value.trim()
    return { address, internal: IPV4.test(address) || address.includes(':') ? privateAddress(address) : undefined }
  }
  return undefined
}

const PRIVATE_V4: [number, number, number][] = [[10, 0, 8], [172, 16, 12], [192, 168, 16], [100, 64, 10]]
function privateAddress(ip: string): boolean {
  if (ip.includes(':')) {
    const g = ipv6(ip)
    const v4 = g && mappedV4(g)
    return v4 ? privateAddress(v4) : !!g && g[0] >> 9 === 0x7e // fc00::/7
  }
  const n = ip.split('.').reduce((a, p) => a * 256 + Number(p), 0)
  return PRIVATE_V4.some(([a, b, bits]) => Math.floor(n / 2 ** (32 - bits)) === Math.floor((a * 2 ** 24 + b * 2 ** 16) / 2 ** (32 - bits)))
}

export const capital = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

/**
 * A contract's meaning split for the form: what the variable is («la llave de
 * acceso»), said in labels and sentences, and the hint the server adds in
 * parentheses («vacía: la identidad de la app»), shown apart.
 */
export function meaningOf(v: Pick<CatalogKindVariable, 'meaning'>): { what: string; hint?: string } {
  const m = /^(.*?)\s*\(([^()]*)\)\s*$/.exec(v.meaning)
  return m && m[1] ? { what: m[1], hint: m[2] } : { what: v.meaning }
}

// --- the recipe's id -------------------------------------------------------

/** The server's bounds of an organization's recipe (internal/catalog). */
export const MAX_RECIPE_ID = 48
export const MAX_TITLE = 80
export const MAX_DESCRIPTION = 500
const ORG_ID = /^org-[a-z0-9]+(-[a-z0-9]+)*$/

/**
 * The id of a new recipe, from its title: «org-» and its words in lowercase
 * without accents («Base de reportes» → org-base-de-reportes), cut to the
 * server's length, numbered when the catalog already has it. IT never types
 * an id; assistants see it.
 */
export function recipeIdOf(title: string, taken: readonly string[]): string {
  const slug = title.normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  const cut = (s: string, n: number) => s.slice(0, n).replace(/-+$/, '')
  const base = cut(`org-${slug || 'recurso'}`, MAX_RECIPE_ID)
  if (!taken.includes(base)) return base
  for (let i = 2; ; i++) {
    const id = `${cut(base, MAX_RECIPE_ID - String(i).length - 1)}-${i}`
    if (!taken.includes(id)) return id
  }
}

export const recipeIdOk = (id: string) => id.length <= MAX_RECIPE_ID && ORG_ID.test(id)

/** The problem with a recipe's title and description (undefined: fine). */
export function titleProblem(title: string, description: string): string | undefined {
  const t = title.trim()
  if (!t) return 'Ponle un nombre: es lo que verán quienes crean apps.'
  if (t.length > MAX_TITLE) return `El nombre tiene hasta ${MAX_TITLE} caracteres.`
  if (description.trim().length > MAX_DESCRIPTION) return `La descripción tiene hasta ${MAX_DESCRIPTION} caracteres.`
  return undefined
}

// --- reading a published one ----------------------------------------------

/** A recipe of the organization's own: a resource the company already has (IT publishes its versions). */
export const isOwn = (e: Pick<CatalogEntry, 'engine'>) => e.engine === 'existing'

/**
 * Where a published connection points, for IT, in one line: the address it
 * connects to (the first written host or URL, else the first written value)
 * and the stores its credentials come from.
 */
export function connectionLine(kind: CatalogKind | undefined, e: Pick<CatalogEntry, 'spec'>): string | undefined {
  const values = e.spec.existing?.values
  if (!values) return undefined
  const formatOf = (name: string) => kind?.variables.find(v => v.name === name)?.format
  const written = values.filter(v => v.value)
  const where = written.find(v => formatOf(v.variable) === 'host' || formatOf(v.variable) === 'url') ?? written[0]
  const stores = [...new Set(values.flatMap(v => (v.ref ? [v.ref.store] : [])))].sort()
  const out = [
    where && `Se conecta a ${where.value}`,
    stores.length > 0 && `credenciales del almacén ${stores.map(s => `«${s}»`).join(', ')}`,
  ].filter(Boolean) as string[]
  return out.length > 0 ? capital(out.join(' · ')) + '.' : undefined
}
