import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { ChipGroup } from '@/components/odyssey'
import type { Service } from '@/lib/types'
import { normalizeScope, scopeProblem, scopeText } from './scope'
import { isMulti, kindText } from './services'

/**
 * "Para quién": which parts of the app receive a variable, a secret, a
 * database or the AI (MS-6b). Draws nothing for an app of one part (D3).
 * When the app's cluster cannot give a setting to some parts only, it says
 * so in one line instead of offering the choice. `exactlyOne` is for a files
 * volume: exactly one part uses it, so there is no "whole app".
 */
export function ScopePicker({ value, onChange, services, scoped, exactlyOne, disabled, label = 'Quién lo recibe' }: {
  /** The names chosen; empty = the whole app. */
  value: string[]
  onChange: (next: string[]) => void
  services: readonly Pick<Service, 'name' | 'kind'>[] | undefined
  /** `scoped_settings` of the environment's settings. */
  scoped: boolean | undefined
  exactlyOne?: boolean
  disabled?: boolean
  label?: string
}) {
  if (!isMulti(services)) return null
  if (!scoped) {
    return <p className="text-xs text-muted-foreground">El servidor de esta app solo puede dárselo a toda la app. Si lo necesitas solo para algunas partes, pídeselo a TI.</p>
  }
  const parts = services ?? []
  const some = exactlyOne || value.length > 0
  const toggle = (name: string, on: boolean) => {
    if (exactlyOne) return onChange(on ? [name] : [])
    onChange(normalizeScope(on ? [...value, name] : value.filter(n => n !== name)))
  }
  const problem = scopeProblem(value, { services, scoped, exactlyOne })
  return (
    <div className="flex flex-col gap-2">
      {!exactlyOne && (
        <ChipGroup
          label={label}
          value={some ? 'some' : 'all'}
          // choosing parts starts with the first one ticked; at least one stays ticked ("Toda la app" clears)
          onChange={v => !disabled && onChange(v === 'all' ? [] : value.length > 0 ? value : [parts[0].name])}
          options={[{ value: 'all', label: 'Toda la app' }, { value: 'some', label: 'Solo algunas partes' }]}
        />
      )}
      {exactlyOne && <span className="text-[13px] font-medium">Qué parte usa los archivos</span>}
      {some && (
        <ul className="flex flex-col gap-1.5" aria-label={exactlyOne ? 'Parte que usa los archivos' : 'Partes que lo reciben'}>
          {parts.map(s => (
            <li key={s.name}>
              <label className="flex items-center gap-2.5 text-sm">
                <Checkbox checked={value.includes(s.name)} disabled={disabled || (!exactlyOne && value.length === 1 && value[0] === s.name)} onCheckedChange={v => toggle(s.name, !!v)} aria-label={s.name} />
                <span className="font-mono text-[13px]">{s.name}</span>
                <span className="text-xs text-muted-foreground">{kindText[s.kind].label}</span>
              </label>
            </li>
          ))}
        </ul>
      )}
      {problem && <p className="text-xs text-destructive">{problem}</p>}
    </div>
  )
}

/** "Solo: api, jobs" next to a row; nothing for the whole app or an app of one part. */
export function ScopeBadge({ scope, services }: { scope: readonly string[] | undefined; services: readonly unknown[] | undefined }) {
  if (!isMulti(services) || !scope || scope.length === 0) return null
  return <Badge variant="secondary" title="Solo estas partes de la app lo reciben.">{scopeText(scope)}</Badge>
}
