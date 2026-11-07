import type { SecretView } from '@/lib/types'

/** One row of the variables editor (UX: name = value, nothing technical). */
export type VariableRow = { name: string; value: string }

/** pkg-level rule shared with the API (SecretName): an environment variable name. */
export const VARIABLE_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/
export const MAX_VARIABLE_VALUE = 4096

export const rowsOf = (env: Record<string, string> | undefined): VariableRow[] =>
  Object.entries(env ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, value]) => ({ name, value }))

/**
 * What PATCH /env must receive to turn `original` into the rows: only what
 * changed (an untouched variable is not resent), names removed go in unset.
 * Blank rows are ignored.
 */
export function envDiff(original: Record<string, string> | undefined, rows: VariableRow[]): { set: Record<string, string>; unset: string[] } {
  const before = original ?? {}
  const after = new Map(rows.filter(r => r.name.trim()).map(r => [r.name.trim(), r.value]))
  const set: Record<string, string> = {}
  for (const [name, value] of after) if (before[name] !== value) set[name] = value
  const unset = Object.keys(before).filter(name => !after.has(name)).sort()
  return { set, unset }
}

export const envChanged = (original: Record<string, string> | undefined, rows: VariableRow[]) => {
  const d = envDiff(original, rows)
  return Object.keys(d.set).length > 0 || d.unset.length > 0
}

/** The problem with a variable's name, in plain words (undefined: fine). The API checks the same. */
export function variableNameProblem(name: string, rows: VariableRow[], secrets: string[]): string | undefined {
  const n = name.trim()
  if (!n) return undefined
  if (!VARIABLE_NAME.test(n)) return 'Usa letras, números y _ (sin espacios ni guiones), y no empieces con un número.'
  if (n.toUpperCase().startsWith('ODYSSEY_')) return 'Los nombres que empiezan con ODYSSEY_ los usa Odicea.'
  if (rows.filter(r => r.name.trim() === n).length > 1) return 'Este nombre está repetido.'
  if (secrets.includes(n)) return 'Ya hay un secreto con este nombre.'
  return undefined
}

export const rowsValid = (rows: VariableRow[], secrets: string[]) =>
  rows.every(r => (!r.name.trim() ? !r.value : !variableNameProblem(r.name, rows, secrets) && r.value.length <= MAX_VARIABLE_VALUE))

/**
 * A guard for honest mistakes (the same words the MCP refuses): a password
 * pasted as a plain variable is visible to anyone who can read the app.
 */
export function looksSecret(name: string): boolean {
  const up = name.toUpperCase()
  return ['PASSWORD', 'PASSWD', 'SECRET', 'TOKEN', 'PRIVATE_KEY', 'API_KEY', 'APIKEY', 'CREDENTIAL'].some(w => up.includes(w))
}

/** Each secret state in plain words, with the badge tone it is shown in. */
export const secretStateText: Record<SecretView['state'], { label: string; tone: 'success' | 'warning' | 'destructive' | 'info' | 'secondary'; help: string }> = {
  missing: { label: 'Falta cargar', tone: 'warning', help: 'Nadie cargó el valor todavía. La app no lo recibe hasta entonces.' },
  loaded: { label: 'Cargado', tone: 'success', help: 'La app lo recibe como variable de entorno.' },
  reload: { label: 'Vuelve a cargarlo', tone: 'destructive', help: 'El clúster cambió su llave y ya no puede abrir el valor guardado. Cárgalo otra vez.' },
  external: { label: 'Bóveda de la empresa', tone: 'info', help: 'Lo lee el clúster del almacén de secretos de la empresa.' },
  unavailable: { label: 'No disponible', tone: 'secondary', help: 'El clúster de esta app todavía no puede guardar este tipo de secreto. Pídele a TI que lo habilite.' },
  failed: { label: 'No llegó', tone: 'destructive', help: 'El valor no llegó al clúster de la app. Cárgalo otra vez.' },
}

/**
 * Whether a person can load (or replace) the value from the console: only a
 * managed secret (one read from the company's vault is changed by TI there).
 */
export const canLoad = (s: SecretView, keyAvailable: boolean) => s.source === 'managed' && keyAvailable && s.state !== 'unavailable'

/** Whether a person can remove the secret (a vault one needs TI's permission). */
export const canRemove = (s: SecretView, canExternal: boolean) => s.source === 'managed' || canExternal

/** The button that loads a value says what it will do. */
export const loadLabel = (s: SecretView) => (s.state === 'loaded' ? 'Cambiar valor' : s.state === 'reload' || s.state === 'failed' ? 'Volver a cargar' : 'Cargar valor')

/**
 * What PATCH /env receives for a layer (MS-4): '' is the whole app's own
 * variables; a name is that part's own layer (its variables win over the
 * whole app's of the same name).
 */
export function layerBody(diff: { set: Record<string, string>; unset: string[] }, layer: string): { set: Record<string, string>; unset: string[]; services?: string[] } {
  return layer ? { ...diff, services: [layer] } : diff
}

/** The variables a layer starts from (the whole app's or one part's own). */
export const layerEnv = (settings: { env: Record<string, string>; service_env?: Record<string, Record<string, string>> } | undefined, layer: string): Record<string, string> | undefined =>
  layer ? settings?.service_env?.[layer] ?? {} : settings?.env

/** The parts whose own layer sets `name` (they win over the whole app's value). */
export const overriddenBy = (name: string, serviceEnv: Record<string, Record<string, string>> | undefined): string[] =>
  Object.entries(serviceEnv ?? {})
    .filter(([, env]) => name in env)
    .map(([part]) => part)
    .sort()
