import { isMulti } from './services'

/**
 * Who receives a setting (MS-6b): empty = the whole app (also the parts added
 * later), else only the parts named. The words say "partes"; the picker and
 * the badge stay silent for an app of one part (D3).
 */
export type Scope = string[]

/** "Solo: api, jobs" (empty: "Toda la app"). */
export const scopeText = (scope: readonly string[] | undefined | null): string =>
  scope && scope.length > 0 ? `Solo: ${[...scope].sort().join(', ')}` : 'Toda la app'

/** The scope in the order the API keeps it (sorted, no repeats). */
export const normalizeScope = (scope: readonly string[]): Scope => [...new Set(scope)].sort()

export const sameScope = (a: readonly string[] | undefined, b: readonly string[] | undefined): boolean => {
  const x = normalizeScope(a ?? [])
  const y = normalizeScope(b ?? [])
  return x.length === y.length && x.every((n, i) => n === y[i])
}

/**
 * Whether the screen offers to choose: the app has several parts and its
 * cluster can give a setting to some of them only. Else everything goes to
 * the whole app, as before.
 */
export const asksScope = (services: readonly unknown[] | undefined | null, scoped: boolean | undefined): boolean => isMulti(services) && scoped === true

/**
 * What is wrong with the choice, in plain words (undefined: fine). A files
 * volume of an app of several parts is read by exactly one part. Nothing to
 * check when the screen does not ask (the API answers for the rest).
 */
export function scopeProblem(scope: readonly string[], opts: { services: readonly unknown[] | undefined | null; scoped: boolean | undefined; exactlyOne?: boolean }): string | undefined {
  if (!asksScope(opts.services, opts.scoped)) return undefined
  if (opts.exactlyOne && scope.length !== 1) return 'Elige la parte que usará los archivos: solo una puede.'
  return undefined
}

/** The body field: sent only when someone chose parts (absent = the whole app or as it was). */
export const scopeField = (scope: readonly string[]): { services?: Scope } => (scope.length > 0 ? { services: normalizeScope(scope) } : {})
