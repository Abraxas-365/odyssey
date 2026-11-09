import { useState, type FormEvent } from 'react'
import { Building2, Lock, Pencil, Plus, Users } from 'lucide-react'
import { toast } from 'sonner'
import type { UseQueryResult } from '@tanstack/react-query'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { EmptyState, ErrorNotice, ListSkeleton, OptionCards, PageHeader, Panel, PanelHeader, SwitchField } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { defaultTakenBy, maxTeams, offeringDraft, offeringForm, offeringProblem, offeringUnchanged, teamNames, unitLabel, useCatalog, useCatalogKinds, useCatalogTeams, useUpdateOffering, type OfferingForm } from '@/lib/catalog'
import { connectionLine, isOwn } from '@/lib/connection'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import type { CatalogEntry, CatalogKind, CatalogTeam, CatalogTeams, ProjectTier, ResourceKind } from '@/lib/types'
import { kindText } from '@/features/apps/resources'
import { RecipeDialog } from '@/features/catalog/recipe-dialog'
import { tierOptions } from '@/features/policy/rules'

const kinds: ResourceKind[] = ['postgres', 'redis', 'files', 'object-storage']

/**
 * Administración → Catálogo (catalog:manage, docs/catalog.md): what apps can
 * create — the recipes Odicea ships and the resources the company already
 * has, which IT registers here — and, for each, what the organization
 * offers: on or off, in which app levels, the default of its kind, the
 * limits of each parameter and the part of them created without asking IT.
 * Without catalog:manage there is nothing to do here: say who can.
 */
export function CatalogPage() {
  const { can } = useAuth()
  if (!can(permissions.catalogManage)) {
    return (
      <>
        <PageHeader breadcrumb="Administración" title="Catálogo" />
        <EmptyState icon={Lock} title="Solo TI administra el catálogo">
          TI decide qué bases de datos, cachés y archivos pueden crear las apps. Si necesitas otra opción, pídesela.
        </EmptyState>
      </>
    )
  }
  return <Catalog />
}

function Catalog() {
  const catalog = useCatalog()
  const teams = useCatalogTeams()
  const kindsQuery = useCatalogKinds()
  const { technical } = usePreferences()
  const [editing, setEditing] = useState<CatalogEntry>()
  // a resource of the company: a new one ('new') or the next version of one
  const [registering, setRegistering] = useState<'new' | CatalogEntry>()
  const items = catalog.data?.items ?? []
  const contracts = kindsQuery.data?.items ?? []
  const canRegister = contracts.some(k => k.existing)

  return (
    <>
      <PageHeader
        breadcrumb="Administración"
        title="Catálogo"
        description="Lo que las apps pueden crear o usar. Tú eliges qué se ofrece en cada nivel de app, sus límites y hasta dónde se crea al instante; más allá, se pide tu aprobación."
        actions={canRegister && <Button variant="outline" size="sm" onClick={() => setRegistering('new')}><Plus /> Registrar un recurso que ya existe</Button>}
      />
      {catalog.isPending ? (
        <Panel><ListSkeleton /></Panel>
      ) : catalog.error ? (
        <ErrorNotice error={catalog.error} onRetry={() => catalog.refetch()} />
      ) : (
        <div className="flex flex-col gap-6">
          {kindsQuery.error && <ErrorNotice error={kindsQuery.error} onRetry={() => kindsQuery.refetch()} />}
          {kinds.map(k => {
            const of = items.filter(e => e.kind === k)
            if (of.length === 0) return null
            return (
              <Panel key={k}>
                <PanelHeader icon={kindText[k].icon} title={plural[k]} meta={String(of.length)} description={kindText[k].help} />
                <ul className="flex flex-col divide-y" aria-label={plural[k]}>
                  {of.map(e => (
                    <EntryRow key={e.recipe} entry={e} contract={contracts.find(c => c.kind === e.kind)} teams={teams.data?.available ? teams.data.items : undefined}
                      onEdit={() => setEditing(e)} onNewVersion={canRegister && isOwn(e) ? () => setRegistering(e) : undefined} />
                  ))}
                </ul>
              </Panel>
            )
          })}
        </div>
      )}
      {editing && <OfferingDialog entry={editing} entries={items} teams={teams} onClose={() => setEditing(undefined)} />}
      {registering && (
        <RecipeDialog kinds={contracts} entries={items} entry={registering === 'new' ? undefined : registering} technical={technical}
          onClose={() => setRegistering(undefined)}
          onDone={published => { setRegistering(undefined); if (registering === 'new') setEditing(published) }} />
      )}
    </>
  )
}

const plural: Record<ResourceKind, string> = { postgres: 'Bases de datos', redis: 'Cachés', files: 'Archivos', 'object-storage': 'Almacenamiento de objetos' }

/** "Sandbox · Interna" in factory order. */
const tierList = (ts: readonly ProjectTier[]) => tierOptions.filter(o => ts.includes(o.value)).map(o => o.title).join(' · ')

/** A number field's value: a cleared field (NaN) shows empty. */
const num = (v: number) => (Number.isNaN(v) ? '' : v)

/** A Kubernetes quantity as people read it: "2Gi" is "2 GB", "512Mi" "512 MB". */
const quantity = (q: string) => q.replace(/^(\d+(?:\.\d+)?)(Gi|Mi)$/, (_, n: string, u: string) => `${n} ${u === 'Gi' ? 'GB' : 'MB'}`)

/** The engine settings a version fixes, for IT ("2 instancias · PostgreSQL 17 · 1 CPU · 2 GB de memoria"). */
export function specLine(e: Pick<CatalogEntry, 'spec'>): string | undefined {
  const pg = e.spec.postgres
  if (!pg) return undefined
  return [
    pg.instances && (pg.instances === 1 ? '1 instancia' : `${pg.instances} instancias`),
    pg.major && `PostgreSQL ${pg.major}`,
    pg.cpu && `${pg.cpu} CPU`,
    pg.memory && `${quantity(pg.memory)} de memoria`,
  ].filter(Boolean).join(' · ')
}

/** How the offering reads in one line: where, for whom, the default, and each parameter's ranges. */
export function offeringSummary(e: CatalogEntry, teams?: readonly CatalogTeam[]): string[] {
  if (!e.offering.active) return [isOwn(e) ? 'No se ofrece: ninguna app puede usarla.' : 'No se ofrece: ninguna app puede crearla.']
  const f = offeringForm(e)
  const out = [`Se ofrece en apps ${tierList(e.offering.tiers)}.`]
  if (e.offering.teams.length > 0) {
    const n = e.offering.teams.length
    if (!teams) out.push(n === 1 ? 'Solo la crea un equipo.' : `Solo la crean ${n} equipos.`)
    else {
      const names = teamNames(e.offering.teams, teams)
      out.push(n === 1 ? `Solo la crea el equipo ${names[0]}.` : `Solo la crean los equipos ${names.join(', ')}.`)
    }
  }
  const defaults = e.offering.defaults.filter(t => e.offering.tiers.includes(t))
  if (defaults.length > 0) out.push(`Es la opción por defecto en ${tierList(defaults)}.`)
  for (const p of e.params) {
    const l = f.limits[p.name]
    const a = f.preapproved[p.name]
    const unit = p.unit ? ` ${unitLabel(p.unit)}` : ''
    out.push(
      a.min === l.min && a.max === l.max
        ? `${p.label}: de ${l.min} a ${l.max}${unit}, todo al instante.`
        : `${p.label}: de ${l.min} a ${l.max}${unit}; al instante de ${a.min} a ${a.max}${unit}, más allá con aprobación.`,
    )
  }
  return out
}

function EntryRow({ entry, contract, teams, onEdit, onNewVersion }: { entry: CatalogEntry; contract?: CatalogKind; teams?: readonly CatalogTeam[]; onEdit: () => void; onNewVersion?: () => void }) {
  const spec = specLine(entry)
  const own = isOwn(entry)
  const connection = own ? connectionLine(contract, entry) : undefined
  return (
    <li className="flex flex-col gap-2 px-5 py-4 sm:flex-row sm:items-start">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[13px] font-semibold">{entry.title}</span>
          <Badge variant={entry.offering.active ? 'success' : 'secondary'}>{entry.offering.active ? 'Se ofrece' : 'Apagada'}</Badge>
          {own && <Badge variant="info" title="Odicea no la crea ni la borra: solo la conecta."><Building2 /> Ya existe en la empresa</Badge>}
          {entry.offering.active && entry.offering.teams.length > 0 && <Badge variant="secondary"><Users /> Por equipo</Badge>}
          <span className="text-xs text-muted-foreground">Versión {entry.version}{own && Math.max(...entry.versions) > entry.version ? ` (hay una más nueva sin ofrecer)` : ''}</span>
        </div>
        {entry.description && <p className="mt-0.5 text-[13px] text-muted-foreground">{entry.description}</p>}
        {spec && <p className="mt-0.5 text-xs text-muted-foreground">{spec}</p>}
        {connection && <p className="mt-0.5 text-xs text-muted-foreground">{connection}</p>}
        <ul className="mt-1.5 flex flex-col gap-0.5 text-xs">
          {offeringSummary(entry, teams).map(line => <li key={line}>{line}</li>)}
        </ul>
      </div>
      <div className="flex items-center gap-1.5">
        {onNewVersion && (
          <Button size="sm" variant="ghost" onClick={onNewVersion} aria-label={`Versión nueva de ${entry.title}`}>Versión nueva</Button>
        )}
        <Button size="sm" variant="outline" onClick={onEdit} aria-label={`Editar ${entry.title}`}>
          <Pencil /> Editar
        </Button>
      </div>
    </li>
  )
}

function OfferingDialog({ entry, entries, teams, onClose }: { entry: CatalogEntry; entries: CatalogEntry[]; teams: UseQueryResult<CatalogTeams>; onClose: () => void }) {
  const update = useUpdateOffering()
  const [form, setForm] = useState<OfferingForm>(() => offeringForm(entry))
  const problem = offeringProblem(entry.params, form)
  const unchanged = offeringUnchanged(entry, form)
  const set = (patch: Partial<OfferingForm>) => setForm(f => ({ ...f, ...patch }))
  const toggle = (list: ProjectTier[], t: ProjectTier, on: boolean) => (on ? tierOptions.map(o => o.value).filter(x => x === t || list.includes(x)) : list.filter(x => x !== t))
  const range = (which: 'limits' | 'preapproved', name: string, end: 'min' | 'max', v: number) =>
    setForm(f => ({ ...f, [which]: { ...f[which], [name]: { ...f[which][name], [end]: v } } }))
  const versions = entry.versions.map(String)
  const creators = form.creators === 'teams' ? form.teams : []

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (problem || unchanged) return
    update.mutate({ recipe: entry.recipe, body: offeringDraft(entry.params, form) }, { onSuccess: () => { toast.success('Catálogo actualizado'); onClose() } })
  }

  return (
    <Dialog open onOpenChange={v => !v && !update.isPending && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>{entry.title}</DialogTitle>
            <DialogDescription>Lo que cambies vale para lo que se cree desde ahora. Lo que ya existe se queda como está.</DialogDescription>
          </DialogHeader>
          <SwitchField title="Se ofrece" description={isOwn(entry) ? 'Apagada, ninguna app puede agregarla (las que ya la usan la conservan).' : 'Apagada, ninguna app puede crearla (las que ya la tienen la conservan).'} checked={form.active} onCheckedChange={active => set({ active })} disabled={update.isPending} />
          <FieldGroup>
            {versions.length > 1 ? (
              <Field>
                <FieldLabel>Versión que se ofrece</FieldLabel>
                <Select value={String(form.version)} items={Object.fromEntries(versions.map(v => [v, `Versión ${v}`]))} onValueChange={v => set({ version: Number(v ?? form.version) })}>
                  <SelectTrigger className="w-full" aria-label="Versión que se ofrece"><SelectValue /></SelectTrigger>
                  <SelectContent>{versions.map(v => <SelectItem key={v} value={v}>Versión {v}</SelectItem>)}</SelectContent>
                </Select>
                <FieldDescription>Lo ya creado sigue en la versión con que se creó. Los límites de abajo son los de la versión {entry.version}: guarda la versión primero y luego ajústalos.</FieldDescription>
              </Field>
            ) : (
              <p className="text-xs text-muted-foreground">Versión {entry.version}: {isOwn(entry) ? 'la única publicada. «Versión nueva» publica otra si cambia la conexión.' : 'es la única que trae esta instalación de Odicea.'}</p>
            )}
            <fieldset className="flex flex-col gap-2" disabled={!form.active || update.isPending}>
              <legend className="mb-1 text-[13px] font-medium">Niveles de app</legend>
              {tierOptions.map(o => {
                const offered = form.tiers.includes(o.value)
                const taken = defaultTakenBy(entries, entry.recipe, o.value, creators)
                return (
                  <div key={o.value} className="flex flex-wrap items-center gap-x-5 gap-y-1 text-[13px]">
                    <label className="flex w-32 items-center gap-2">
                      <Checkbox checked={offered} onCheckedChange={v => set({ tiers: toggle(form.tiers, o.value, !!v), defaults: v ? form.defaults : form.defaults.filter(t => t !== o.value) })} />
                      {o.title}
                    </label>
                    <label className="flex items-center gap-2 text-muted-foreground" title={taken ? `«${taken.title}» ya es la opción por defecto en ${o.title}.` : undefined}>
                      <Checkbox checked={form.defaults.includes(o.value)} disabled={!offered || (!!taken && !form.defaults.includes(o.value))} onCheckedChange={v => set({ defaults: toggle(form.defaults, o.value, !!v) })} />
                      Opción por defecto{taken && !form.defaults.includes(o.value) ? ` (ya lo es «${taken.title}»)` : ''}
                    </label>
                  </div>
                )
              })}
              <FieldDescription>La opción por defecto es la que recibe quien pide «una base de datos» sin elegir. Hay una por tipo en cada nivel; la de un equipo gana a la de todos para sus miembros.</FieldDescription>
            </fieldset>
            <TeamsField form={form} saved={entry.offering.teams} teams={teams} disabled={!form.active || update.isPending} onChange={set} />
            {entry.params.map(p => {
              const unit = p.unit ? ` (${unitLabel(p.unit)})` : ''
              const l = form.limits[p.name]
              const a = form.preapproved[p.name]
              return (
                <fieldset key={p.name} className="flex flex-col gap-3" disabled={!form.active || update.isPending || form.version !== entry.version}>
                  <legend className="mb-1 text-[13px] font-medium">{p.label}{unit}</legend>
                  <div className="grid grid-cols-2 gap-3">
                    <Field>
                      <FieldLabel htmlFor={`${p.name}-min`}>Mínimo permitido</FieldLabel>
                      <Input id={`${p.name}-min`} type="number" inputMode="numeric" min={p.min} max={p.max} value={num(l.min)} onChange={e => range('limits', p.name, 'min', e.target.valueAsNumber)} />
                    </Field>
                    <Field>
                      <FieldLabel htmlFor={`${p.name}-max`}>Máximo permitido</FieldLabel>
                      <Input id={`${p.name}-max`} type="number" inputMode="numeric" min={p.min} max={p.max} value={num(l.max)} onChange={e => range('limits', p.name, 'max', e.target.valueAsNumber)} />
                    </Field>
                    <Field>
                      <FieldLabel htmlFor={`${p.name}-pre-min`}>Al instante desde</FieldLabel>
                      <Input id={`${p.name}-pre-min`} type="number" inputMode="numeric" min={l.min} max={l.max} value={num(a.min)} onChange={e => range('preapproved', p.name, 'min', e.target.valueAsNumber)} />
                    </Field>
                    <Field>
                      <FieldLabel htmlFor={`${p.name}-pre-max`}>Al instante hasta</FieldLabel>
                      <Input id={`${p.name}-pre-max`} type="number" inputMode="numeric" min={l.min} max={l.max} value={num(a.max)} onChange={e => range('preapproved', p.name, 'max', e.target.valueAsNumber)} />
                    </Field>
                  </div>
                  <FieldDescription>Fuera del límite no se puede pedir. Dentro del límite pero fuera de «al instante», se crea cuando TI lo apruebe. Esta versión admite de {p.min} a {p.max}.</FieldDescription>
                </fieldset>
              )
            })}
          </FieldGroup>
          {problem && <FieldError>{problem}</FieldError>}
          <ErrorNotice error={update.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={update.isPending}>Cancelar</Button>
            <Button type="submit" variant="primary" disabled={!!problem || unchanged || update.isPending}>{update.isPending ? 'Guardando…' : 'Guardar'}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * "Quién puede crearla": everyone, or only the people in some IAMKit groups
 * (its teams). Without the directory a new team cannot be named (the server
 * refuses it) but the ones already saved are kept; a team since deleted in
 * IAMKit is shown as such and can be taken out.
 */
function TeamsField({ form, saved, teams, disabled, onChange }: {
  form: OfferingForm
  saved: readonly string[]
  teams: UseQueryResult<CatalogTeams>
  disabled: boolean
  onChange: (patch: Partial<OfferingForm>) => void
}) {
  const known = teams.data?.items ?? []
  const available = teams.data?.available ?? false
  const gone = form.teams.filter(id => !known.some(t => t.id === id))
  const canChoose = available || saved.length > 0
  const pick = (id: string, on: boolean) => onChange({ teams: on ? [...form.teams, id] : form.teams.filter(x => x !== id) })

  return (
    <fieldset className="flex flex-col gap-2" disabled={disabled}>
      <legend className="mb-1 text-[13px] font-medium">Quién puede crearla</legend>
      <OptionCards
        label="Quién puede crearla"
        value={form.creators}
        onChange={creators => onChange({ creators })}
        options={[
          { value: 'all', title: 'Todos', description: 'Cualquier persona de la organización que pueda crear en una app.' },
          {
            value: 'teams', title: 'Solo estos equipos', disabled: disabled || !canChoose,
            description: canChoose ? 'Solo quienes estén en alguno de los equipos elegidos. Lo ya creado sigue funcionando para todos.'
              : 'No disponible: Odicea no puede leer los equipos de tu organización.',
          },
        ]}
      />
      {form.creators === 'teams' && (
        teams.isPending ? (
          <ListSkeleton />
        ) : teams.error ? (
          <ErrorNotice error={teams.error} onRetry={() => teams.refetch()} />
        ) : (
          <div className="flex flex-col gap-1.5 rounded-lg border px-4 py-3" role="group" aria-label="Equipos">
            {!available && <p className="text-xs text-muted-foreground">Odicea no puede leer los equipos ahora: se conservan los elegidos, pero no puedes agregar otros.</p>}
            {available && known.length === 0 && <p className="text-xs text-muted-foreground">Tu organización aún no tiene equipos. Créalos en la administración de usuarios.</p>}
            {available && known.map(t => (
              <label key={t.id} className="flex items-center gap-2 text-[13px]">
                <Checkbox checked={form.teams.includes(t.id)} disabled={!form.teams.includes(t.id) && form.teams.length >= maxTeams}
                  onCheckedChange={v => pick(t.id, !!v)} />
                <span className="min-w-0 flex-1 truncate">{t.name}</span>
                <span className="text-xs text-muted-foreground">{t.members === 1 ? '1 persona' : `${t.members} personas`}{t.directory ? ' · del directorio de la empresa' : ''}</span>
              </label>
            ))}
            {(available ? gone : form.teams).map(id => (
              <label key={id} className="flex items-center gap-2 text-[13px] text-muted-foreground">
                <Checkbox checked onCheckedChange={v => !v && pick(id, false)} />
                <span className="min-w-0 flex-1 truncate">{teamNames([id], available ? known : undefined)[0]}</span>
              </label>
            ))}
          </div>
        )
      )}
    </fieldset>
  )
}
