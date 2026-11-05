import { useState, type FormEvent } from 'react'
import { Database, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Callout, ChipGroup, EmptyState, ErrorNotice, Panel, PanelBody, PanelFooter, PanelHeader, TechnicalDetails } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import { useConnectResource, useCreateResource, useDeleteResource, useDisconnectResource, useResizeResource, useResources } from '@/lib/resources'
import type { Resource, ResourceKind } from '@/lib/types'
import { AIRow } from './ai-panel'
import { anchors } from './uses'
import { atLimit, deleteBlock, deleteConfirmed, DEFAULT_GI, failureOf, isActive, kindText, MAX_GI, MAX_NAME, MAX_PREFIX, MIN_GI, nameProblem, prefixProblem, sizeLabel, sizeProblem, stateOf, suggestName } from './resources'

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
  const [adding, setAdding] = useState(false)
  const [sizing, setSizing] = useState<Resource>()
  const [removing, setRemoving] = useState<Resource>()
  const items = list.data?.items ?? []
  const kinds = list.data?.available_kinds ?? []
  const mayDelete = can(permissions.resourcesDelete)
  const full = atLimit(items.length)

  const change = (r: Resource, on: boolean) => {
    const done = (out: { deployment_id: string | null }) => toast.success(applied(out.deployment_id))
    if (on) connect.mutate({ name: r.name }, { onSuccess: done })
    else disconnect.mutate(r.name, { onSuccess: done })
  }

  return (
    <Panel id={anchors.data} tabIndex={-1} className="scroll-mt-20 outline-none">
      <PanelHeader icon={Database} title="Datos e IA" description="Bases de datos, cachés, archivos e IA de la app. La app los recibe como variables de entorno." />
      <PanelBody>
        <ErrorNotice error={list.error} onRetry={() => list.refetch()} />
        {list.data && items.length === 0 && (
          <EmptyState icon={Database} level={2} title="Esta app no usa nada todavía">
            {kinds.length > 0 ? 'Agrega una base de datos, una caché o un espacio de archivos.' : 'El clúster de esta app todavía no puede crear bases de datos, cachés ni archivos. Pídele a TI que lo habilite.'}
          </EmptyState>
        )}
        {items.length > 0 && (
          <ul className="flex flex-col divide-y rounded-lg border" aria-label="Recursos de la app">
            {items.map(r => {
              const text = kindText[r.kind]
              const state = stateOf(r)
              const failure = failureOf(r)
              const block = deleteBlock(r)
              return (
                <li key={r.id} className="flex flex-col gap-2 px-4 py-3">
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                    <text.icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-mono text-[13px] font-medium">{r.name}</span>
                        <span className="text-xs text-muted-foreground">{text.label} · {sizeLabel(r.storage_gi)}</span>
                        <Badge variant={state.tone}>{state.label}</Badge>
                        {r.status === 'active' && <Badge variant={r.connected ? 'success' : 'secondary'}>{r.connected ? 'Conectada a la app' : 'Sin conectar'}</Badge>}
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
                    <div className="rounded-md border">
                      <TechnicalDetails title={`Qué dijo el clúster de ${r.name}`} defaultOpen>
                        <p className="px-5 py-3 font-mono text-xs break-all text-muted-foreground">{failure.detail}</p>
                      </TechnicalDetails>
                    </div>
                  )}
                  {technical && r.variables.length > 0 && (
                    <div className="rounded-md border">
                      <TechnicalDetails title={`Variables de ${r.name}`} defaultOpen>
                        <p className="px-5 py-3 font-mono text-xs break-all text-muted-foreground">{r.variables.join('  ')}</p>
                      </TechnicalDetails>
                    </div>
                  )}
                </li>
              )
            })}
          </ul>
        )}
        <ErrorNotice error={connect.error ?? disconnect.error} />
        <AIRow project={project} env={env} canWrite={canWrite} busy={busy} technical={technical} />
      </PanelBody>
      {canWrite && (
        <PanelFooter>
          <span>{full ? 'Llegaste al máximo de recursos de esta app.' : 'Al eliminar un recurso se borran sus datos para siempre.'}</span>
          <Button size="sm" variant="outline" onClick={() => setAdding(true)} disabled={!list.data || kinds.length === 0 || full}>
            <Plus /> Agregar
          </Button>
        </PanelFooter>
      )}
      {adding && <AddResourceDialog project={project} env={env} kinds={kinds} taken={items.map(r => r.name)} onClose={() => setAdding(false)} />}
      {sizing && <ResizeDialog project={project} env={env} resource={sizing} onClose={() => setSizing(undefined)} />}
      {removing && <DeleteDialog project={project} env={env} resource={removing} onClose={() => setRemoving(undefined)} />}
    </Panel>
  )
}

function AddResourceDialog({ project, env, kinds, taken, onClose }: { project: string; env: string; kinds: ResourceKind[]; taken: string[]; onClose: () => void }) {
  const create = useCreateResource(project, env)
  const [kind, setKind] = useState<ResourceKind>(kinds[0] ?? 'postgres')
  const [name, setName] = useState(() => suggestName(kinds[0] ?? 'postgres', taken))
  const [edited, setEdited] = useState(false)
  const [prefix, setPrefix] = useState('')
  const [size, setSize] = useState(DEFAULT_GI)
  const [connectIt, setConnectIt] = useState(true)
  const n = name.trim()
  const problem = nameProblem(n, taken) ?? (n ? undefined : 'Ponle un nombre.')
  const pProblem = connectIt ? prefixProblem(prefix) : undefined
  const sProblem = sizeProblem(size)
  const ready = !problem && !pProblem && !sProblem
  const pick = (k: ResourceKind) => {
    setKind(k)
    if (!edited) setName(suggestName(k, taken))
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!ready) return
    create.mutate(
      { name: n, kind, storage_gi: size, ...(connectIt ? { connect: prefix.trim() ? { prefix: prefix.trim() } : {} } : {}) },
      {
        onSuccess: out => {
          toast.success(out.connected ? applied(out.deployment_id) : 'Listo. Falta conectarla a la app.')
          onClose()
        },
      },
    )
  }
  return (
    <Dialog open onOpenChange={v => !v && !create.isPending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Agregar a la app</DialogTitle>
            <DialogDescription>{kindText[kind].help}</DialogDescription>
          </DialogHeader>
          <ChipGroup label="Tipo" value={kind} onChange={pick} options={kinds.map(k => ({ value: k, label: kindText[k].label }))} />
          <FieldGroup>
            <Field data-invalid={!!problem || undefined}>
              <FieldLabel htmlFor="resource-name">Nombre</FieldLabel>
              <Input id="resource-name" className="font-mono" value={name} maxLength={MAX_NAME} autoComplete="off" spellCheck={false} onChange={e => { setName(e.target.value); setEdited(true) }} />
              {problem ? <FieldError>{problem}</FieldError> : <FieldDescription>No se puede cambiar después.</FieldDescription>}
            </Field>
            <Field data-invalid={!!sProblem || undefined}>
              <FieldLabel htmlFor="resource-size">Tamaño (GB)</FieldLabel>
              <Input id="resource-size" type="number" inputMode="numeric" min={MIN_GI} max={MAX_GI} value={size} onChange={e => setSize(e.target.valueAsNumber)} />
              {sProblem ? <FieldError>{sProblem}</FieldError> : <FieldDescription>Después solo puede crecer.</FieldDescription>}
            </Field>
            <ChipGroup label="Conexión" value={connectIt ? 'yes' : 'no'} onChange={v => setConnectIt(v === 'yes')} options={[{ value: 'yes', label: 'Conectarla a la app ahora' }, { value: 'no', label: 'Solo crearla' }]} />
            {connectIt && (
              <Field data-invalid={!!pProblem || undefined}>
                <FieldLabel htmlFor="resource-prefix">Prefijo <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
                <Input id="resource-prefix" className="font-mono" value={prefix} placeholder="ANALYTICS" maxLength={MAX_PREFIX} autoComplete="off" spellCheck={false} onChange={e => setPrefix(e.target.value)} />
                {pProblem ? <FieldError>{pProblem}</FieldError> : <FieldDescription>Úsalo si la app tiene dos del mismo tipo: así no se pisan las variables.</FieldDescription>}
              </Field>
            )}
          </FieldGroup>
          <ErrorNotice error={create.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={create.isPending}>Cancelar</Button>
            <Button type="submit" variant="primary" disabled={!ready || create.isPending}>{create.isPending ? 'Creando…' : 'Agregar'}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ResizeDialog({ project, env, resource, onClose }: { project: string; env: string; resource: Resource; onClose: () => void }) {
  const resize = useResizeResource(project, env)
  const [size, setSize] = useState(resource.storage_gi)
  const problem = sizeProblem(size, resource.storage_gi)
  const same = size === resource.storage_gi
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (problem || same) return
    resize.mutate({ name: resource.name, storage_gi: size }, { onSuccess: () => { toast.success('Ampliando el espacio…'); onClose() } })
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
            <Input id="resize-size" type="number" inputMode="numeric" min={resource.storage_gi} max={MAX_GI} value={size} autoFocus onChange={e => setSize(e.target.valueAsNumber)} />
            {problem ? <FieldError>{problem}</FieldError> : <FieldDescription>Ahora tiene {sizeLabel(resource.storage_gi)}.</FieldDescription>}
          </Field>
          <ErrorNotice error={resize.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={resize.isPending}>Cancelar</Button>
            <Button type="submit" variant="primary" disabled={!!problem || same || resize.isPending}>{resize.isPending ? 'Guardando…' : 'Ampliar'}</Button>
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
    remove.mutate(resource.name, { onSuccess: () => { toast.success('Eliminando…'); onClose() } })
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
