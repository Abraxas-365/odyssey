import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { KeyRound, Plus, SlidersHorizontal, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Callout, ChipGroup, ErrorNotice, Panel, PanelBody, PanelFooter, PanelHeader } from '@/components/odyssey'
import { anchors } from './uses'
import { useAuth } from '@/lib/auth'
import { formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { useLoadSecret, usePutSecret, useRemoveSecret, useScopeSecret, useSetEnv, useSettings } from '@/lib/releases'
import { MAX_SECRET_VALUE } from '@/lib/secretbox'
import { useServices } from '@/lib/services'
import type { SecretView, Service } from '@/lib/types'
import { afterWrite } from '@/features/approvals/notice'
import { scopeField, sameScope } from './scope'
import { ScopeBadge, ScopePicker } from './service-scope'
import { isMulti } from './services'
import { canLoad, canRemove, envChanged, envDiff, layerBody, layerEnv, loadLabel, looksSecret, overriddenBy, rowsOf, rowsValid, secretStateText, variableNameProblem, type VariableRow } from './variables'

const applied = (deployment: unknown) => (deployment ? 'Aplicando el cambio a la app…' : 'Guardado. La app lo recibe con la próxima publicación.')

/**
 * "Variables": plain settings of the app (never secrets). Saving sends only
 * what changed; while the app is online the same version is published again
 * with them (the page's polling shows it like any publication).
 */
export function VariablesPanel({ project, env, canWrite, busy }: { project: string; env: string; canWrite: boolean; busy: boolean }) {
  const settings = useSettings(project, env)
  const services = useServices(project).data?.items
  const save = useSetEnv(project, env)
  // '' is the whole app's own variables; a name is that part's own layer (MS-4). One part: always ''.
  const [pickedLayer, setLayer] = useState('')
  const multi = isMulti(services)
  const scoped = settings.data?.scoped_settings === true
  const layer = multi && scoped && services?.some(s => s.name === pickedLayer) ? pickedLayer : ''
  const original = layerEnv(settings.data, layer)
  const [rows, setRows] = useState<VariableRow[] | null>(null)
  const shown = rows ?? rowsOf(original)
  const secretNames = settings.data?.secrets.map(s => s.name) ?? []
  const dirty = rows !== null && envChanged(original, rows)
  const valid = rowsValid(shown, secretNames)
  const suspicious = shown.filter(r => r.name.trim() && looksSecret(r.name) && original?.[r.name.trim()] !== r.value).map(r => r.name.trim())
  const [notes, setNotes] = useState<string[]>([])

  const edit = (i: number, patch: Partial<VariableRow>) => setRows(shown.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const changeLayer = (next: string) => { setLayer(next); setRows(null); setNotes([]); save.reset() }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!dirty || !valid || !rows) return
    save.mutate(layerBody(envDiff(original, rows), layer), {
      onSuccess: out => {
        setRows(null)
        setNotes(out.warnings ?? [])
        toast.success(applied(out.deployment))
      },
    })
  }

  return (
    <Panel>
      <form onSubmit={submit}>
        <PanelHeader icon={SlidersHorizontal} title="Variables" description="Ajustes de la app que no son secretos: zona horaria, nivel de log, direcciones públicas." />
        <PanelBody>
          <ErrorNotice error={settings.error} onRetry={() => settings.refetch()} />
          {multi && scoped && (
            <ChipGroup
              label="Para"
              value={layer}
              onChange={changeLayer}
              options={[{ value: '', label: 'Toda la app' }, ...(services ?? []).map(s => ({ value: s.name, label: <span className="font-mono">{s.name}</span> }))]}
            />
          )}
          {multi && layer && <p className="text-xs text-muted-foreground">Estas variables las recibe solo <span className="font-mono">{layer}</span>. Si una se llama igual que una de toda la app, gana la de esta parte.</p>}
          {multi && !scoped && <ScopePicker value={[]} onChange={() => {}} services={services} scoped={false} />}
          {settings.data && shown.length === 0 && <p className="text-[13px] text-muted-foreground">{layer ? 'Esta parte no tiene variables propias.' : 'Esta app no tiene variables.'}</p>}
          {shown.length > 0 && (
            <div className="flex flex-col gap-2" role="list" aria-label={layer ? `Variables de ${layer}` : 'Variables de la app'}>
              {shown.map((r, i) => {
                const problem = variableNameProblem(r.name, shown, secretNames)
                const over = layer ? [] : overriddenBy(r.name.trim(), settings.data?.service_env)
                return (
                  <div key={i} role="listitem" className="flex flex-col gap-1">
                    <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] items-center gap-2">
                      <Input
                        aria-label="Nombre"
                        aria-invalid={!!problem || undefined}
                        className="font-mono"
                        value={r.name}
                        placeholder="NOMBRE"
                        maxLength={128}
                        autoComplete="off"
                        spellCheck={false}
                        disabled={!canWrite}
                        onChange={e => edit(i, { name: e.target.value })}
                      />
                      <Input
                        aria-label={`Valor de ${r.name || 'la variable'}`}
                        className="font-mono"
                        value={r.value}
                        placeholder="valor"
                        maxLength={4096}
                        autoComplete="off"
                        spellCheck={false}
                        disabled={!canWrite}
                        onChange={e => edit(i, { value: e.target.value })}
                      />
                      {canWrite && (
                        <Button type="button" variant="ghost" size="icon-sm" aria-label={`Quitar ${r.name || 'esta variable'}`} onClick={() => setRows(shown.filter((_, j) => j !== i))}>
                          <Trash2 />
                        </Button>
                      )}
                    </div>
                    {problem && <p className="text-xs text-destructive">{problem}</p>}
                    {over.length > 0 && <p className="text-xs text-muted-foreground">{over.join(', ')} {over.length > 1 ? 'tienen' : 'tiene'} su propio valor para esta variable.</p>}
                  </div>
                )
              })}
            </div>
          )}
          {suspicious.length > 0 && (
            <Callout tone="warn" title={`${suspicious.join(', ')} parece un secreto.`}>
              Cualquiera que vea la app puede leer una variable. Si es una contraseña, llave o token, guárdalo en Secretos.
            </Callout>
          )}
          {notes.length > 0 && <Callout tone="info" title="Ten en cuenta">{notes.join(' ')}</Callout>}
          <ErrorNotice error={save.error} />
        </PanelBody>
        {canWrite && (
          <PanelFooter>
            <Button type="button" variant="outline" size="sm" onClick={() => setRows([...shown, { name: '', value: '' }])}>
              <Plus /> Agregar variable
            </Button>
            <div className="flex items-center gap-2">
              {dirty && (
                <Button type="button" variant="ghost" size="sm" onClick={() => { setRows(null); save.reset() }}>
                  Descartar
                </Button>
              )}
              <Button type="submit" disabled={!dirty || !valid || save.isPending || busy}>
                {save.isPending ? 'Guardando…' : 'Guardar'}
              </Button>
            </div>
          </PanelFooter>
        )}
      </form>
    </Panel>
  )
}

/**
 * "Secretos": passwords, keys and tokens the app needs. Odicea never sees a
 * value: "Cargar valor" seals it in this browser to the cluster's key, and
 * only the cluster can open it. A link from an assistant (`?secret=NAME`,
 * request_secret) opens the load dialog for that secret.
 */
export function SecretsPanel({ project, env, canWrite, busy }: { project: string; env: string; canWrite: boolean; busy: boolean }) {
  const { can } = useAuth()
  const settings = useSettings(project, env)
  const [params, setParams] = useSearchParams()
  const [loading, setLoading] = useState<SecretView>()
  const [removing, setRemoving] = useState<SecretView>()
  const [adding, setAdding] = useState(false)
  const [scoping, setScoping] = useState<SecretView>()
  const services = useServices(project).data?.items
  const multi = isMulti(services)
  const remove = useRemoveSecret(project, env)
  const panel = useRef<HTMLElement>(null)
  const data = settings.data
  const keyAvailable = !!data?.secret_key
  const mayExternal = can(permissions.secretsExternal)
  const canExternal = mayExternal && !!data?.external_secrets && (data?.secret_stores.length ?? 0) > 0

  // The assistant's link: open that secret's dialog once the settings are here.
  const linked = params.get('secret')
  useEffect(() => {
    if (!linked || !data) return
    const s = data.secrets.find(x => x.name === linked)
    panel.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    if (s && canWrite && canLoad(s, keyAvailable)) setLoading(s)
    const next = new URLSearchParams(params)
    next.delete('secret')
    setParams(next, { replace: true })
  }, [linked, data, canWrite, keyAvailable, params, setParams])

  return (
    <Panel ref={panel} id={anchors.secrets} tabIndex={-1} className="scroll-mt-20 outline-none">
      <PanelHeader icon={KeyRound} title="Secretos" description="Contraseñas, llaves y tokens. Se cifran en tu navegador: ni Odicea puede leerlos." />
      {(settings.error || remove.error || data?.secrets_cluster_revoked || (data && !keyAvailable) || data?.secrets.length === 0) && (
      <PanelBody>
        <ErrorNotice error={settings.error} onRetry={() => settings.refetch()} />
        <ErrorNotice error={remove.error} />
        {data?.secrets_cluster_revoked ? (
          <Callout tone="warn" title="El clúster de esta app fue desconectado.">
            No puede recibir secretos. Pídele a TI que mueva la app a otro clúster; luego vuelve a cargar los valores.
          </Callout>
        ) : (
          data &&
          !keyAvailable && (
            <Callout tone="warn" title="El clúster de esta app todavía no puede guardar secretos.">
              Pídele a TI que lo habilite. Mientras tanto no pongas el valor como variable.
            </Callout>
          )
        )}
        {data && data.secrets.length === 0 && <p className="text-[13px] text-muted-foreground">Esta app no tiene secretos.</p>}
      </PanelBody>
      )}
        {/* UX v4 §5.9: the list sits flush in the panel, split by separators. */}
        {data && data.secrets.length > 0 && (
          <ul className="flex flex-col divide-y" aria-label="Secretos de la app">
            {data.secrets.map(s => {
              const state = secretStateText[s.state]
              return (
                <li key={s.name} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-5 py-3.5">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-[13px] font-medium">{s.name}</span>
                      <Badge variant={state.tone} title={state.help}>{state.label}</Badge>
                      <ScopeBadge scope={s.services ?? s.request?.services} services={services} />
                    </div>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      {s.request?.note ? `${s.request.note} · ` : ''}
                      {s.source === 'external'
                        ? `${s.store} → ${s.key}${s.property ? ` (${s.property})` : ''}`
                        : s.state === 'failed' && s.failure
                          ? `No llegó ${formatRelative(s.failure.failed_at)}: ${s.failure.error}`
                          : s.loaded_at
                          ? `Cargado ${formatRelative(s.loaded_at)}`
                          : state.help}
                    </p>
                  </div>
                  {canWrite && (
                    <div className="flex items-center gap-1.5">
                      {multi && data.scoped_settings && (
                        <Button size="sm" variant="ghost" disabled={busy} onClick={() => setScoping(s)}>
                          Cambiar quién lo lee
                        </Button>
                      )}
                      {canLoad(s, keyAvailable) && (
                        <Button size="sm" variant={s.state === 'loaded' ? 'outline' : 'primary'} disabled={busy} onClick={() => setLoading(s)}>
                          {loadLabel(s)}
                        </Button>
                      )}
                      {canRemove(s, mayExternal) && (
                        <Button size="icon-sm" variant="ghost" aria-label={`Quitar ${s.name}`} disabled={busy} onClick={() => setRemoving(s)}>
                          <Trash2 />
                        </Button>
                      )}
                    </div>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      {canWrite && (
        <PanelFooter>
          <span>La app recibe cada secreto como una variable de entorno con su nombre.</span>
          <Button size="sm" variant="outline" onClick={() => setAdding(true)} disabled={!data}>
            <Plus /> Agregar secreto
          </Button>
        </PanelFooter>
      )}
      {loading && <LoadSecretDialog project={project} env={env} secret={loading} onClose={() => setLoading(undefined)} />}
      {scoping && data && <ScopeSecretDialog project={project} env={env} secret={scoping} services={services} scoped={data.scoped_settings} onClose={() => setScoping(undefined)} />}
      {adding && data && (
        <AddSecretDialog
          project={project}
          env={env}
          taken={[...data.secrets.map(s => s.name), ...Object.keys(data.env)]}
          stores={canExternal ? data.secret_stores : []}
          keyAvailable={keyAvailable}
          services={services}
          scoped={data.scoped_settings}
          onClose={() => setAdding(false)}
          onRequested={s => setLoading(s)}
        />
      )}
      <AlertDialog open={!!removing} onOpenChange={v => !v && setRemoving(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>¿Quitar {removing?.name}?</AlertDialogTitle>
            <AlertDialogDescription>La app deja de recibirlo. Si está en línea, se vuelve a publicar sin él.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive-solid"
              onClick={() =>
                removing &&
                remove.mutate(removing.name, {
                  onSuccess: out => toast.success(out.deployment ? 'Quitando el secreto de la app…' : 'Secreto quitado'),
                  onSettled: () => setRemoving(undefined),
                })
              }
            >
              Quitar
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Panel>
  )
}

/** Types a value and seals it here; the field is cleared as soon as it is sent or the dialog closes. */
function LoadSecretDialog({ project, env, secret, onClose }: { project: string; env: string; secret: SecretView; onClose: () => void }) {
  const load = useLoadSecret(project, env)
  const [value, setValue] = useState('')
  const tooLong = new TextEncoder().encode(value).byteLength > MAX_SECRET_VALUE
  const close = () => {
    if (load.isPending) return
    setValue('')
    load.reset() // its variables hold the value
    onClose()
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!value || tooLong) return
    load.mutate(
      { name: secret.name, value },
      {
        onSuccess: out => {
          setValue('')
          load.reset()
          toast.success(out.deployment ? 'Secreto guardado. Lo estamos entregando a la app…' : 'Secreto guardado. La app lo recibe con la próxima publicación.')
          onClose()
        },
      },
    )
  }
  return (
    <Dialog open onOpenChange={v => !v && close()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>{loadLabel(secret)}: <span className="font-mono">{secret.name}</span></DialogTitle>
            <DialogDescription>
              {secret.request?.note ? `${secret.request.note}. ` : ''}
              Se cifra en este navegador antes de salir: solo el clúster de la app puede abrirlo. Nadie podrá verlo después, ni tú.
            </DialogDescription>
          </DialogHeader>
          <Field data-invalid={tooLong || undefined}>
            <FieldLabel htmlFor="secret-value">Valor</FieldLabel>
            <Textarea
              id="secret-value"
              className="min-h-24 font-mono [-webkit-text-security:disc]"
              value={value}
              onChange={e => setValue(e.target.value)}
              autoComplete="off"
              autoCorrect="off"
              autoCapitalize="off"
              spellCheck={false}
              data-1p-ignore
              data-lpignore="true"
              autoFocus
            />
            {tooLong ? <FieldError>El valor es demasiado largo (máximo 64 KB).</FieldError> : <FieldDescription>Pégalo tal cual, sin comillas.</FieldDescription>}
          </Field>
          <ErrorNotice error={load.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close} disabled={load.isPending}>Cancelar</Button>
            <Button type="submit" variant="primary" disabled={!value || tooLong || load.isPending}>
              {load.isPending ? 'Cifrando y guardando…' : 'Guardar'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

type Source = 'managed' | 'external'

/** "Cambiar quién lo lee": the parts that receive a secret, without loading its value again. */
function ScopeSecretDialog({ project, env, secret, services, scoped, onClose }: {
  project: string
  env: string
  secret: SecretView
  services: readonly Pick<Service, 'name' | 'kind'>[] | undefined
  scoped: boolean
  onClose: () => void
}) {
  const scope = useScopeSecret(project, env)
  const was = secret.services ?? secret.request?.services ?? []
  const [value, setValue] = useState<string[]>(was)
  const changed = !sameScope(value, was)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!changed) return
    scope.mutate(
      { name: secret.name, services: value },
      {
        onSuccess: out => {
          afterWrite(out, o => toast.success(o.deployment ? 'Cambiando quién recibe el secreto…' : 'Guardado. Se aplica con la próxima publicación.'))
          onClose()
        },
      },
    )
  }
  return (
    <Dialog open onOpenChange={v => !v && !scope.isPending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>¿Quién lee <span className="font-mono">{secret.name}</span>?</DialogTitle>
            <DialogDescription>No hace falta cargar el valor otra vez: solo cambia qué partes de la app lo reciben.</DialogDescription>
          </DialogHeader>
          <ScopePicker value={value} onChange={setValue} services={services} scoped={scoped} disabled={scope.isPending} />
          <ErrorNotice error={scope.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={scope.isPending}>Cancelar</Button>
            <Button type="submit" variant="primary" disabled={!changed || !scoped || scope.isPending}>{scope.isPending ? 'Guardando…' : 'Guardar'}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Declares a secret. `managed` (anyone who edits the app) is loaded next;
 * `external` reads it from the company's vault and is offered only to who
 * holds odyssey:secrets:external on a cluster with the operator.
 */
function AddSecretDialog({ project, env, taken, stores, keyAvailable, services, scoped, onClose, onRequested }: {
  project: string
  env: string
  taken: string[]
  stores: string[]
  keyAvailable: boolean
  services: readonly Pick<Service, 'name' | 'kind'>[] | undefined
  scoped: boolean
  onClose: () => void
  onRequested: (s: SecretView) => void
}) {
  const put = usePutSecret(project, env)
  const [source, setSource] = useState<Source>(keyAvailable || stores.length === 0 ? 'managed' : 'external')
  const [name, setName] = useState('')
  const [note, setNote] = useState('')
  const [store, setStore] = useState(stores[0] ?? '')
  const [key, setKey] = useState('')
  const [property, setProperty] = useState('')
  const [scope, setScope] = useState<string[]>([])
  const n = name.trim()
  const problem = taken.includes(n) ? 'Ya hay una variable o un secreto con este nombre.' : variableNameProblem(n, [], [])
  const ready = !!n && !problem && (source === 'managed' ? keyAvailable : !!store && !!key.trim())
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!ready) return
    const to = scopeField(scoped ? scope : [])
    const body = source === 'managed' ? { source, note: note.trim() || undefined, ...to } : { source, store, key: key.trim(), property: property.trim() || undefined, ...to }
    put.mutate(
      { name: n, body },
      {
        onSuccess: out => {
          onClose()
          if (source === 'managed') onRequested(out as SecretView)
          else toast.success(applied((out as { deployment?: unknown }).deployment))
        },
      },
    )
  }
  return (
    <Dialog open onOpenChange={v => !v && !put.isPending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Agregar un secreto</DialogTitle>
            <DialogDescription>La app lo recibe como una variable de entorno con este nombre.</DialogDescription>
          </DialogHeader>
          {stores.length > 0 && (
            <ChipGroup
              label="De dónde sale el valor"
              value={source}
              onChange={setSource}
              options={[{ value: 'managed', label: 'Lo cargo yo' }, { value: 'external', label: 'Bóveda de la empresa' }]}
            />
          )}
          <FieldGroup>
            <Field data-invalid={!!problem || undefined}>
              <FieldLabel htmlFor="secret-name">Nombre</FieldLabel>
              <Input id="secret-name" className="font-mono" value={name} onChange={e => setName(e.target.value)} placeholder="STRIPE_API_KEY" maxLength={128} autoComplete="off" spellCheck={false} autoFocus />
              {problem ? <FieldError>{problem}</FieldError> : <FieldDescription>El nombre que tu código lee, por ejemplo process.env.STRIPE_API_KEY.</FieldDescription>}
            </Field>
            {source === 'managed' ? (
              <Field>
                <FieldLabel htmlFor="secret-note">Para qué es <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
                <Input id="secret-note" value={note} onChange={e => setNote(e.target.value)} placeholder="Cobros con Stripe" maxLength={200} />
              </Field>
            ) : (
              <>
                <Field>
                  <FieldLabel htmlFor="secret-store">Almacén</FieldLabel>
                  <Select value={store} onValueChange={v => setStore((v as string | null) ?? '')} items={Object.fromEntries(stores.map(s => [s, s]))}>
                    <SelectTrigger id="secret-store" className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      {stores.map(s => <SelectItem key={s} value={s}>{s}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
                <Field>
                  <FieldLabel htmlFor="secret-key">Llave en la bóveda</FieldLabel>
                  <Input id="secret-key" className="font-mono" value={key} onChange={e => setKey(e.target.value)} placeholder="apps/pagos/stripe" maxLength={512} autoComplete="off" />
                </Field>
                <Field>
                  <FieldLabel htmlFor="secret-property">Propiedad <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
                  <Input id="secret-property" className="font-mono" value={property} onChange={e => setProperty(e.target.value)} placeholder="api_key" maxLength={256} autoComplete="off" />
                </Field>
              </>
            )}
          </FieldGroup>
          {source === 'managed' && !keyAvailable && (
            <Callout tone="warn" title="El clúster de esta app todavía no puede guardar secretos.">Pídele a TI que lo habilite.</Callout>
          )}
          <ScopePicker value={scope} onChange={setScope} services={services} scoped={scoped} disabled={put.isPending} />
          <ErrorNotice error={put.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={put.isPending}>Cancelar</Button>
            <Button type="submit" variant="primary" disabled={!ready || put.isPending}>
              {put.isPending ? 'Guardando…' : source === 'managed' ? 'Siguiente: cargar valor' : 'Conectar'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
