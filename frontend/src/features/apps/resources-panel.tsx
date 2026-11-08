import { useState, type FormEvent } from 'react'
import { Database, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Callout, ChipGroup, ErrorNotice, ListSkeleton, OptionCards, Panel, PanelBody, PanelFooter, PanelHeader, TechnicalDetails, toneBadge } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { afterWrite } from '@/features/approvals/notice'
import { defaultValues, growthOf, markOf, mutabilityHint, offeredKinds, offersOf, paramProblem, startingOffer, unitLabel, useCatalog, useEnvironmentCatalog } from '@/lib/catalog'
import { permissions } from '@/lib/permissions'
import { useConnectResource, useCreateResource, useDeleteResource, useDisconnectResource, useResizeResource, useResources } from '@/lib/resources'
import { useSettings } from '@/lib/releases'
import { useServices } from '@/lib/services'
import type { CatalogOffer, EnvironmentCatalog, Resource, ResourceKind, Service } from '@/lib/types'
import { AIRow } from './ai-panel'
import { scopeField, scopeProblem } from './scope'
import { ScopeBadge, ScopePicker } from './service-scope'
import { isMulti } from './services'
import { anchors } from './uses'
import { atLimit, deleteBlock, deleteConfirmed, failureOf, isActive, kindText, MAX_GI, MAX_NAME, MAX_PREFIX, nameProblem, prefixProblem, sizeLabel, sizeProblem, stateOf, suggestName } from './resources'

const applied = (deployment: unknown) => (deployment ? 'Aplicando el cambio a la app…' : 'Guardado. La app lo recibe con la próxima publicación.')

/**
 * "Datos e IA": the databases, caches and files of the app. Creating one
 * connects it by default (the app then reads its variables); a resource the
 * app is connected to cannot be erased until it is disconnected.
 */
export function ResourcesPanel({ project, env, canWrite, busy, technical }: { project: string; env: string; canWrite: boolean; busy: boolean; technical: boolean }) {
  const { can } = useAuth()
  const list = useResources(project, env)
  const connect = useConnectResource(project, env)
  const disconnect = useDisconnectResource(project, env)
  const services = useServices(project).data?.items
  const scoped = useSettings(project, env).data?.scoped_settings
  const [adding, setAdding] = useState(false)
  const [connecting, setConnecting] = useState<Resource>()
  const [sizing, setSizing] = useState<Resource>()
  const [removing, setRemoving] = useState<Resource>()
  const items = list.data?.items ?? []
  const kinds = list.data?.available_kinds ?? []
  const mayDelete = can(permissions.resourcesDelete)
  const full = atLimit(items.length)
  const multi = isMulti(services)

  const change = (r: Resource, on: boolean) => {
    const done = (out: { deployment_id: string | null }) => toast.success(applied(out.deployment_id))
    // an app of several parts asks who gets it first; one part: one click, as always
    if (on && multi) return setConnecting(r)
    if (on) connect.mutate({ name: r.name }, { onSuccess: out => afterWrite(out, done) })
    else disconnect.mutate(r.name, { onSuccess: done })
  }

  return (
    <Panel id={anchors.data} tabIndex={-1} className="scroll-mt-20 outline-none">
      <PanelHeader icon={Database} title="Datos e IA" description="Bases de datos, cachés, archivos e IA de la app. La app los recibe como variables de entorno." />
      {(list.error || connect.error || disconnect.error) && (
        <PanelBody>
          <ErrorNotice error={list.error} onRetry={() => list.refetch()} />
          <ErrorNotice error={connect.error ?? disconnect.error} />
        </PanelBody>
      )}
      {/* UX v4 §5.9: one list with separators, never boxes inside the panel. */}
      <ul className="flex flex-col divide-y" aria-label="Recursos de la app">
        {list.data && items.length === 0 && (
          <li className="flex items-start gap-3 px-5 py-3.5">
            <Database className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
            <div className="text-[13px]">
              <div className="font-medium">Esta app no usa nada todavía</div>
              <div className="text-muted-foreground">{kinds.length > 0 ? 'Agrega una base de datos, una caché o un espacio de archivos.' : 'El clúster de esta app todavía no puede crear bases de datos, cachés ni archivos. Pídele a TI que lo habilite.'}</div>
            </div>
          </li>
        )}
        {items.map(r => {
          const text = kindText[r.kind]
          const state = stateOf(r)
          const failure = failureOf(r)
          const block = deleteBlock(r)
          return (
            <li key={r.id} className="flex flex-col gap-2 px-5 py-3.5">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <text.icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-[13px] font-medium">{r.name}</span>
                    <span className="text-xs text-muted-foreground">{text.label} · {sizeLabel(r.storage_gi)}</span>
                    <Badge variant={state.tone}>{state.label}</Badge>
                    {r.status === 'active' && <Badge variant={r.connected ? 'success' : 'secondary'}>{r.connected ? 'Conectada a la app' : 'Sin conectar'}</Badge>}
                    {r.connected && <ScopeBadge scope={r.services} services={services} />}
                  </div>
                </div>
                {canWrite && isActive(r) && (
                  <div className="flex items-center gap-1.5">
                    <Button size="sm" variant="outline" disabled={busy} onClick={() => setSizing(r)}>Cambiar tamaño</Button>
                    <Button size="sm" variant="outline" disabled={busy || connect.isPending || disconnect.isPending} onClick={() => change(r, !r.connected)}>
                      {r.connected ? 'Desconectar' : 'Conectar'}
                    </Button>
                    {mayDelete && (
                      <Button size="icon-sm" variant="ghost" aria-label={`Eliminar ${r.name}`} title={block} disabled={busy} onClick={() => setRemoving(r)}>
                        <Trash2 />
                      </Button>
                    )}
                  </div>
                )}
              </div>
              {failure && <p className="text-xs text-destructive" role="alert">{failure.text}</p>}
              {technical && failure?.detail && (
                <TechnicalDetails title={`Qué dijo el clúster de ${r.name}`} defaultOpen>
                  <p className="px-5 py-3 font-mono text-xs break-all text-muted-foreground">{failure.detail}</p>
                </TechnicalDetails>
              )}
              {technical && r.variables.length > 0 && (
                <TechnicalDetails title={`Variables de ${r.name}`} defaultOpen>
                  <p className="px-5 py-3 font-mono text-xs break-all text-muted-foreground">{r.variables.join('  ')}</p>
                </TechnicalDetails>
              )}
            </li>
          )
        })}
        <AIRow project={project} env={env} canWrite={canWrite} busy={busy} technical={technical} />
      </ul>
      {canWrite && (
        <PanelFooter>
          <span>{full ? 'Llegaste al máximo de recursos de esta app.' : 'Al eliminar un recurso se borran sus datos para siempre.'}</span>
          <Button size="sm" variant="outline" onClick={() => setAdding(true)} disabled={!list.data || kinds.length === 0 || full}>
            <Plus /> Agregar
          </Button>
        </PanelFooter>
      )}
      {adding && <AddResourceDialog project={project} env={env} taken={items.map(r => r.name)} services={services} scoped={scoped} technical={technical} onClose={() => setAdding(false)} />}
      {connecting && <ConnectDialog project={project} env={env} resource={connecting} services={services} scoped={scoped} onClose={() => setConnecting(undefined)} />}
      {sizing && <ResizeDialog project={project} env={env} resource={sizing} onClose={() => setSizing(undefined)} />}
      {removing && <DeleteDialog project={project} env={env} resource={removing} onClose={() => setRemoving(undefined)} />}
    </Panel>
  )
}

/**
 * "Conectar" in an app of several parts: who gets the variables. A files
 * volume goes to exactly one part. When the app's cluster cannot give it to
 * some parts only, it is connected to the whole app as before.
 */
function ConnectDialog({ project, env, resource, services, scoped, onClose }: {
  project: string
  env: string
  resource: Resource
  services: readonly Pick<Service, 'name' | 'kind'>[] | undefined
  scoped: boolean | undefined
  onClose: () => void
}) {
  const connect = useConnectResource(project, env)
  const exactlyOne = resource.kind === 'files'
  const [scope, setScope] = useState<string[]>(resource.services)
  const problem = scopeProblem(scope, { services, scoped, exactlyOne })
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    connect.mutate(
      { name: resource.name, services: scoped ? scope : [] },
      { onSuccess: out => { afterWrite(out, o => toast.success(applied(o.deployment_id))); onClose() } },
    )
  }
  return (
    <Dialog open onOpenChange={v => !v && !connect.isPending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Conectar <span className="font-mono">{resource.name}</span></DialogTitle>
            <DialogDescription>Elige qué partes de la app reciben sus variables.</DialogDescription>
          </DialogHeader>
          <ScopePicker value={scope} onChange={setScope} services={services} scoped={scoped} exactlyOne={exactlyOne} disabled={connect.isPending} />
          <ErrorNotice error={connect.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={connect.isPending}>Cancelar</Button>
            <Button type="submit" variant="primary" disabled={!!problem || (exactlyOne && !scoped) || connect.isPending}>{connect.isPending ? 'Conectando…' : 'Conectar'}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function AddResourceDialog({ project, env, taken, services, scoped, technical, onClose }: { project: string; env: string; taken: string[]; services: readonly Pick<Service, 'name' | 'kind'>[] | undefined; scoped: boolean | undefined; technical: boolean; onClose: () => void }) {
  const catalog = useEnvironmentCatalog(project, env)
  return (
    <Dialog open onOpenChange={v => !v && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        {catalog.data ? (
          <AddResourceForm project={project} env={env} catalog={catalog.data} taken={taken} services={services} scoped={scoped} technical={technical} onClose={onClose} />
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Agregar a la app</DialogTitle>
              <DialogDescription>Buscando lo que puedes agregar…</DialogDescription>
            </DialogHeader>
            {catalog.error ? <ErrorNotice error={catalog.error} onRetry={() => catalog.refetch()} /> : <ListSkeleton rows={2} />}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

/**
 * What can be added, from the environment's catalog: the kinds offered here,
 * the option when IT offers more than one, its parameters within IT's limits
 * and, next to the button, whether it is created at once or waits for IT.
 */
function AddResourceForm({ project, env, catalog, taken, services, scoped, technical, onClose }: { project: string; env: string; catalog: EnvironmentCatalog; taken: string[]; services: readonly Pick<Service, 'name' | 'kind'>[] | undefined; scoped: boolean | undefined; technical: boolean; onClose: () => void }) {
  const create = useCreateResource(project, env)
  const kinds = offeredKinds(catalog)
  const first = kinds[0] ?? 'postgres'
  const [kind, setKind] = useState<ResourceKind>(first)
  const [recipe, setRecipe] = useState(() => startingOffer(catalog, first)?.recipe ?? '')
  const offer = offersOf(catalog, kind).find(r => r.recipe === recipe)
  const [values, setValues] = useState<Record<string, number>>(() => defaultValues(startingOffer(catalog, first)?.params ?? []))
  const [name, setName] = useState(() => suggestName(first, taken))
  const [edited, setEdited] = useState(false)
  const [prefix, setPrefix] = useState('')
  const [connectIt, setConnectIt] = useState(true)
  const [scope, setScope] = useState<string[]>([])
  const n = name.trim()
  const problem = nameProblem(n, taken) ?? (n ? undefined : 'Ponle un nombre.')
  const pProblem = connectIt ? prefixProblem(prefix) : undefined
  const vProblem = offer?.params.some(p => paramProblem(p, values[p.name] ?? p.default))
  const scopeIssue = connectIt ? scopeProblem(scope, { services, scoped, exactlyOne: kind === 'files' }) : undefined
  const mark = offer && markOf(offer.rules_verdict, offer.params, values)
  const ready = !!offer && !problem && !pProblem && !vProblem && !scopeIssue && mark?.effect !== 'deny'
  const choose = (r: CatalogOffer | undefined) => {
    setRecipe(r?.recipe ?? '')
    setValues(defaultValues(r?.params ?? []))
  }
  const pick = (k: ResourceKind) => {
    setKind(k)
    choose(startingOffer(catalog, k))
    if (!edited) setName(suggestName(k, taken))
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!ready || !offer) return
    create.mutate(
      { name: n, kind, recipe: offer.recipe, params: { ...defaultValues(offer.params), ...values }, ...(connectIt ? { connect: { ...(prefix.trim() ? { prefix: prefix.trim() } : {}), ...scopeField(scoped ? scope : []) } } : {}) },
      {
        onSuccess: out => {
          afterWrite(out, o => toast.success(o.connected ? applied(o.deployment_id) : 'Listo. Falta conectarla a la app.'))
          onClose()
        },
      },
    )
  }
  const choices = offersOf(catalog, kind)

  if (kinds.length === 0) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>Agregar a la app</DialogTitle>
          <DialogDescription>Por ahora esta app no puede agregar bases de datos, cachés ni archivos.</DialogDescription>
        </DialogHeader>
        <ul className="flex flex-col gap-1 text-[13px] text-muted-foreground">{catalog.unavailable.map(u => <li key={u.kind}>{u.reason}</li>)}</ul>
        <DialogFooter><Button type="button" variant="outline" onClick={onClose}>Cerrar</Button></DialogFooter>
      </>
    )
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-5">
      <DialogHeader>
        <DialogTitle>Agregar a la app</DialogTitle>
        <DialogDescription>{kindText[kind].help}</DialogDescription>
      </DialogHeader>
      <ChipGroup label="Tipo" value={kind} onChange={pick} options={kinds.map(k => ({ value: k, label: kindText[k].label }))} />
      <FieldGroup>
        {choices.length > 1 || (!offer && choices.length > 0) ? (
          <>
            <OptionCards label="Opción" value={recipe} onChange={r => choose(choices.find(c => c.recipe === r))} options={choices.map(c => ({ value: c.recipe, title: c.title, description: c.description }))} />
            {!offer && <FieldError>TI cambió las opciones mientras elegías: elige una de nuevo.</FieldError>}
          </>
        ) : (
          offer && <p className="text-[13px] text-muted-foreground"><span className="font-medium text-foreground">{offer.title}.</span> {offer.description}</p>
        )}
        <Field data-invalid={!!problem || undefined}>
          <FieldLabel htmlFor="resource-name">Nombre</FieldLabel>
          <Input id="resource-name" className="font-mono" value={name} maxLength={MAX_NAME} autoComplete="off" spellCheck={false} onChange={e => { setName(e.target.value); setEdited(true) }} />
          {problem ? <FieldError>{problem}</FieldError> : <FieldDescription>No se puede cambiar después.</FieldDescription>}
        </Field>
        {offer?.params.map(p => {
          const v = values[p.name] ?? p.default
          const issue = paramProblem(p, v)
          const unit = p.unit ? ` (${unitLabel(p.unit)})` : ''
          const u = p.unit ? ` ${unitLabel(p.unit)}` : ''
          const instant = p.preapproved_min > p.min
            ? ` Al instante de ${p.preapproved_min} a ${p.preapproved_max}${u}; fuera de eso, con aprobación de TI.`
            : p.preapproved_max < p.max ? ` Al instante hasta ${p.preapproved_max}${u}; más, con aprobación de TI.` : ''
          return (
            <Field key={p.name} data-invalid={!!issue || undefined}>
              <FieldLabel htmlFor={`resource-${p.name}`}>{p.label}{unit}</FieldLabel>
              <Input id={`resource-${p.name}`} type="number" inputMode="numeric" min={p.min} max={p.max} value={Number.isNaN(v) ? '' : v} onChange={e => setValues(s => ({ ...s, [p.name]: e.target.valueAsNumber }))} />
              {issue ? <FieldError>{issue}</FieldError> : <FieldDescription>{mutabilityHint[p.mutability]}{instant}</FieldDescription>}
            </Field>
          )
        })}
        <ChipGroup label="Conexión" value={connectIt ? 'yes' : 'no'} onChange={v => setConnectIt(v === 'yes')} options={[{ value: 'yes', label: 'Conectarla a la app ahora' }, { value: 'no', label: 'Solo crearla' }]} />
        {connectIt && (
          <Field data-invalid={!!pProblem || undefined}>
            <FieldLabel htmlFor="resource-prefix">Prefijo <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
            <Input id="resource-prefix" className="font-mono" value={prefix} maxLength={MAX_PREFIX} autoComplete="off" spellCheck={false} placeholder="ANALYTICS" onChange={e => setPrefix(e.target.value)} />
            {pProblem ? <FieldError>{pProblem}</FieldError> : <FieldDescription>Úsalo si la app tiene dos del mismo tipo: así no se pisan las variables.</FieldDescription>}
          </Field>
        )}
        {connectIt && <ScopePicker value={scope} onChange={setScope} services={services} scoped={scoped} exactlyOne={kind === 'files'} disabled={create.isPending} />}
      </FieldGroup>
      {technical && offer && (
        <TechnicalDetails title="Qué recibe la app">
          <div className="flex flex-col gap-1 px-5 py-3 text-xs text-muted-foreground">
            <span className="font-mono">{offer.recipe}@{offer.recipe_version}</span>
            <span className="font-mono break-all">{offer.variables.map(x => (prefix.trim() ? `${prefix.trim()}_${x.name}` : x.name)).join('  ')}</span>
          </div>
        </TechnicalDetails>
      )}
      {mark && (
        <div className="flex items-start gap-2 text-[13px]" aria-live="polite">
          <Badge variant={toneBadge[effectTone[mark.effect]]}>{mark.label}</Badge>
          <span className="text-muted-foreground">{mark.text}</span>
        </div>
      )}
      <ErrorNotice error={create.error} />
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose} disabled={create.isPending}>Cancelar</Button>
        <Button type="submit" variant="primary" disabled={!ready || create.isPending}>{create.isPending ? 'Enviando…' : mark?.effect === 'approval' ? 'Pedir aprobación' : 'Agregar'}</Button>
      </DialogFooter>
    </form>
  )
}

const effectTone = { allow: 'ok', approval: 'warn', deny: 'bad' } as const

function ResizeDialog({ project, env, resource, onClose }: { project: string; env: string; resource: Resource; onClose: () => void }) {
  const resize = useResizeResource(project, env)
  const catalog = useCatalog()
  const growth = growthOf(catalog.data?.items.find(e => e.recipe === resource.recipe), resource.recipe_version)
  const [size, setSize] = useState(resource.storage_gi)
  const problem = growth ? growth.problem(size, resource.storage_gi) : sizeProblem(size, resource.storage_gi)
  const same = size === resource.storage_gi
  const asks = !problem && !same && !!growth?.asks(size)
  const loading = catalog.isPending
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (problem || same || loading) return
    resize.mutate({ name: resource.name, storage_gi: size }, { onSuccess: out => { afterWrite(out, () => toast.success('Ampliando el espacio…')); onClose() } })
  }
  return (
    <Dialog open onOpenChange={v => !v && !resize.isPending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Cambiar el tamaño de <span className="font-mono">{resource.name}</span></DialogTitle>
            <DialogDescription>Solo se puede ampliar. Reducirlo no es posible.</DialogDescription>
          </DialogHeader>
          <Field data-invalid={!!problem || undefined}>
            <FieldLabel htmlFor="resize-size">Tamaño (GB)</FieldLabel>
            <Input id="resize-size" type="number" inputMode="numeric" min={resource.storage_gi} max={growth?.max ?? MAX_GI} value={Number.isNaN(size) ? '' : size} autoFocus onChange={e => setSize(e.target.valueAsNumber)} />
            {problem ? <FieldError>{problem}</FieldError> : <FieldDescription>Ahora tiene {sizeLabel(resource.storage_gi)}.{growth?.hint ? ` ${growth.hint}` : ''}</FieldDescription>}
          </Field>
          {asks && <Callout tone="warn" title="Pide aprobación">Es más de lo que TI dejó ampliar al instante. Se amplía si TI lo aprueba.</Callout>}
          {!loading && !growth && !problem && !same && <FieldDescription>Si TI pide aprobarlo, te lo diremos al guardar.</FieldDescription>}
          <ErrorNotice error={resize.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={resize.isPending}>Cancelar</Button>
            <Button type="submit" variant="primary" disabled={!!problem || same || loading || resize.isPending}>{resize.isPending ? 'Guardando…' : asks ? 'Pedir aprobación' : 'Ampliar'}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Erasing is confirmed by typing the name; a resource the app still uses is not offered. */
function DeleteDialog({ project, env, resource, onClose }: { project: string; env: string; resource: Resource; onClose: () => void }) {
  const remove = useDeleteResource(project, env)
  const [typed, setTyped] = useState('')
  const block = deleteBlock(resource)
  const ok = !block && deleteConfirmed(typed, resource.name)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!ok) return
    remove.mutate(resource.name, { onSuccess: out => { afterWrite(out, () => toast.success('Eliminando…')); onClose() } })
  }
  return (
    <AlertDialog open onOpenChange={v => !v && !remove.isPending && onClose()}>
      <AlertDialogContent>
        <form onSubmit={submit} className="flex flex-col gap-5">
          <AlertDialogHeader>
            <AlertDialogTitle>¿Eliminar <span className="font-mono">{resource.name}</span>?</AlertDialogTitle>
            <AlertDialogDescription>
              {kindText[resource.kind].noun} y todo lo que guarda se borran para siempre. No hay forma de recuperarlos.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {block ? (
            <Callout tone="warn" title={block}>Desconéctala de la app y vuelve a intentarlo.</Callout>
          ) : (
            <Field>
              <FieldLabel htmlFor="delete-confirm">Escribe <span className="font-mono">{resource.name}</span> para confirmar</FieldLabel>
              <Input id="delete-confirm" className="font-mono" value={typed} autoComplete="off" spellCheck={false} autoFocus onChange={e => setTyped(e.target.value)} />
            </Field>
          )}
          <ErrorNotice error={remove.error} />
          <AlertDialogFooter>
            <AlertDialogCancel type="button">Cancelar</AlertDialogCancel>
            <Button type="submit" variant="destructive-solid" disabled={!ok || remove.isPending}>{remove.isPending ? 'Eliminando…' : 'Eliminar para siempre'}</Button>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  )
}
