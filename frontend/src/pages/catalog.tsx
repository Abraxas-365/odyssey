import { useState, type FormEvent } from 'react'
import { Lock, Pencil } from 'lucide-react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { EmptyState, ErrorNotice, ListSkeleton, PageHeader, Panel, PanelHeader, SwitchField } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { defaultTakenBy, offeringDraft, offeringForm, offeringProblem, offeringUnchanged, unitLabel, useCatalog, useUpdateOffering, type OfferingForm } from '@/lib/catalog'
import { permissions } from '@/lib/permissions'
import type { CatalogEntry, ProjectTier, ResourceKind } from '@/lib/types'
import { kindText } from '@/features/apps/resources'
import { tierOptions } from '@/features/policy/rules'

const kinds: ResourceKind[] = ['postgres', 'redis', 'files']

/**
 * Administración → Catálogo (catalog:manage, docs/catalog.md): what apps can
 * create — the recipes Odicea ships — and, for each, what the organization
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
  const [editing, setEditing] = useState<CatalogEntry>()
  const items = catalog.data?.items ?? []

  return (
    <>
      <PageHeader
        breadcrumb="Administración"
        title="Catálogo"
        description="Lo que las apps pueden crear. Tú eliges qué se ofrece en cada nivel de app, sus límites y hasta dónde se crea al instante; más allá, se pide tu aprobación."
      />
      {catalog.isPending ? (
        <Panel><ListSkeleton /></Panel>
      ) : catalog.error ? (
        <ErrorNotice error={catalog.error} onRetry={() => catalog.refetch()} />
      ) : (
        <div className="flex flex-col gap-6">
          {kinds.map(k => {
            const of = items.filter(e => e.kind === k)
            if (of.length === 0) return null
            return (
              <Panel key={k}>
                <PanelHeader icon={kindText[k].icon} title={plural[k]} meta={String(of.length)} description={kindText[k].help} />
                <ul className="flex flex-col divide-y" aria-label={plural[k]}>
                  {of.map(e => <EntryRow key={e.recipe} entry={e} onEdit={() => setEditing(e)} />)}
                </ul>
              </Panel>
            )
          })}
        </div>
      )}
      {editing && <OfferingDialog entry={editing} entries={items} onClose={() => setEditing(undefined)} />}
    </>
  )
}

const plural: Record<ResourceKind, string> = { postgres: 'Bases de datos', redis: 'Cachés', files: 'Archivos' }

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

/** How the offering reads in one line: where, the default, and each parameter's ranges. */
export function offeringSummary(e: CatalogEntry): string[] {
  if (!e.offering.active) return ['No se ofrece: ninguna app puede crearla.']
  const f = offeringForm(e)
  const out = [`Se ofrece en apps ${tierList(e.offering.tiers)}.`]
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

function EntryRow({ entry, onEdit }: { entry: CatalogEntry; onEdit: () => void }) {
  const spec = specLine(entry)
  return (
    <li className="flex flex-col gap-2 px-5 py-4 sm:flex-row sm:items-start">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[13px] font-semibold">{entry.title}</span>
          <Badge variant={entry.offering.active ? 'success' : 'secondary'}>{entry.offering.active ? 'Se ofrece' : 'Apagada'}</Badge>
          <span className="text-xs text-muted-foreground">Versión {entry.version}</span>
        </div>
        <p className="mt-0.5 text-[13px] text-muted-foreground">{entry.description}</p>
        {spec && <p className="mt-0.5 text-xs text-muted-foreground">{spec}</p>}
        <ul className="mt-1.5 flex flex-col gap-0.5 text-xs">
          {offeringSummary(entry).map(line => <li key={line}>{line}</li>)}
        </ul>
      </div>
      <Button size="sm" variant="outline" onClick={onEdit} aria-label={`Editar ${entry.title}`}>
        <Pencil /> Editar
      </Button>
    </li>
  )
}

function OfferingDialog({ entry, entries, onClose }: { entry: CatalogEntry; entries: CatalogEntry[]; onClose: () => void }) {
  const update = useUpdateOffering()
  const [form, setForm] = useState<OfferingForm>(() => offeringForm(entry))
  const problem = offeringProblem(entry.params, form)
  const unchanged = offeringUnchanged(entry, form)
  const set = (patch: Partial<OfferingForm>) => setForm(f => ({ ...f, ...patch }))
  const toggle = (list: ProjectTier[], t: ProjectTier, on: boolean) => (on ? tierOptions.map(o => o.value).filter(x => x === t || list.includes(x)) : list.filter(x => x !== t))
  const range = (which: 'limits' | 'preapproved', name: string, end: 'min' | 'max', v: number) =>
    setForm(f => ({ ...f, [which]: { ...f[which], [name]: { ...f[which][name], [end]: v } } }))
  const versions = entry.versions.map(String)

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
          <SwitchField title="Se ofrece" description="Apagada, ninguna app puede crearla (las que ya la tienen la conservan)." checked={form.active} onCheckedChange={active => set({ active })} disabled={update.isPending} />
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
              <p className="text-xs text-muted-foreground">Versión {entry.version}: es la única que trae esta instalación de Odicea.</p>
            )}
            <fieldset className="flex flex-col gap-2" disabled={!form.active || update.isPending}>
              <legend className="mb-1 text-[13px] font-medium">Niveles de app</legend>
              {tierOptions.map(o => {
                const offered = form.tiers.includes(o.value)
                const taken = defaultTakenBy(entries, entry.recipe, o.value)
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
              <FieldDescription>La opción por defecto es la que recibe quien pide «una base de datos» sin elegir. Hay una por tipo en cada nivel.</FieldDescription>
            </fieldset>
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
