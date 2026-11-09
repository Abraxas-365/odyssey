import { useState, type FormEvent } from 'react'
import { KeyRound } from 'lucide-react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Callout, ChipGroup, ErrorNotice, ListSkeleton, TechnicalDetails } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { useCatalogStores, usePublishRecipeVersion, useRegisterRecipe } from '@/lib/catalog'
import {
  capital, connectionOf, connectionProblem, connectionRows, givenOf, MAX_DESCRIPTION, MAX_TITLE, meaningOf, missingStores, networkTarget, readersOf,
  recipeIdOf, rowProblem, sameConnection, sourceLabel, sourcesOf, titleProblem, type Row,
} from '@/lib/connection'
import { permissions } from '@/lib/permissions'
import type { CatalogEntry, CatalogKind, CatalogKindVariable, CatalogStore, ResourceKind } from '@/lib/types'
import { kindText } from '@/features/apps/resources'

/**
 * IT registers a resource the company already has (docs/catalog.md «The
 * organization's own recipes»), or publishes the next version of one: what
 * builders will see (a name and a description) and where each piece of the
 * connection comes from, as the kind's contract allows (GET /catalog/kinds).
 * A credential is only ever read from a secret store of the company's
 * clusters, so it never passes through Odicea. A new recipe is registered
 * switched off: IT offers it afterwards like any other.
 */
export function RecipeDialog({ kinds, entries, entry, technical, onClose, onDone }: {
  /** The kinds' contracts (only those a resource of the company can fulfil are offered). */
  kinds: CatalogKind[]
  /** The whole catalog: a new recipe's id must not repeat one. */
  entries: CatalogEntry[]
  /** The recipe a new version is for; absent, a new one is registered. */
  entry?: CatalogEntry
  technical: boolean
  onClose: () => void
  /** The published version (to go on and offer it). */
  onDone: (published: CatalogEntry) => void
}) {
  const stores = useCatalogStores()
  const allowed = kinds.filter(k => k.existing)
  return (
    <Dialog open onOpenChange={v => !v && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        {stores.data ? (
          <RecipeForm kinds={allowed} entries={entries} entry={entry} stores={stores.data.items} technical={technical} onClose={onClose} onDone={onDone} />
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{entry ? `Versión nueva de ${entry.title}` : 'Registrar un recurso que ya existe'}</DialogTitle>
              <DialogDescription>Buscando los almacenes de secretos de tus clústeres…</DialogDescription>
            </DialogHeader>
            {stores.error ? <ErrorNotice error={stores.error} onRetry={() => stores.refetch()} /> : <ListSkeleton rows={2} />}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

function RecipeForm({ kinds, entries, entry, stores, technical, onClose, onDone }: {
  kinds: CatalogKind[]
  entries: CatalogEntry[]
  entry?: CatalogEntry
  stores: CatalogStore[]
  technical: boolean
  onClose: () => void
  onDone: (published: CatalogEntry) => void
}) {
  const { can } = useAuth()
  const register = useRegisterRecipe()
  const publish = usePublishRecipeVersion()
  const write = entry ? publish : register
  const firstStore = stores[0]?.name ?? ''
  const [kind, setKind] = useState<ResourceKind>(entry?.kind ?? kinds[0]?.kind ?? 'postgres')
  const contract = kinds.find(k => k.kind === kind)
  const [rows, setRows] = useState<Record<string, Row>>(() => (contract ? connectionRows(contract, entry?.spec.existing, firstStore) : {}))
  const [title, setTitle] = useState(entry?.title ?? '')
  const [description, setDescription] = useState(entry?.description ?? '')
  const [tried, setTried] = useState(false)

  if (!contract) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>Registrar un recurso que ya existe</DialogTitle>
          <DialogDescription>Esta instalación de Odicea todavía no puede conectar recursos de la empresa.</DialogDescription>
        </DialogHeader>
        <DialogFooter><Button type="button" variant="outline" onClick={onClose}>Cerrar</Button></DialogFooter>
      </>
    )
  }

  const nextVersion = entry ? Math.max(...entry.versions) + 1 : 1
  const id = entry?.recipe ?? recipeIdOf(title, entries.map(e => e.recipe))
  const named = Object.values(rows).some(r => r.source === 'store')
  const mayStores = can(permissions.secretsExternal)
  const needsStore = givenOf(contract).some(v => v.secret && !v.optional)
  const tProblem = titleProblem(title, description)
  const cProblem = connectionProblem(contract, rows)
  const missing = missingStores(rows, stores)
  const readers = readersOf(rows, stores)
  const target = networkTarget(contract, rows)
  const connection = connectionOf(contract, rows)
  const unchanged = !!entry && title.trim() === entry.title && description.trim() === entry.description && sameConnection(connection, entry.spec.existing)
  const blocked = (named && !mayStores) || (needsStore && stores.length === 0)
  const ready = !tProblem && !cProblem && missing.length === 0 && !blocked && !unchanged && (readers.all || readers.clusters.length > 0)

  const pick = (k: ResourceKind) => {
    const c = kinds.find(x => x.kind === k)
    setKind(k)
    if (c) setRows(connectionRows(c, undefined, firstStore))
    setTried(false)
  }
  const setRow = (name: string, patch: Partial<Row>) => setRows(r => ({ ...r, [name]: { ...r[name], ...patch } }))

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setTried(true)
    if (!ready) return
    const draft = { title: title.trim(), ...(description.trim() ? { description: description.trim() } : {}), existing: connection }
    const done = (published: CatalogEntry) => {
      // the answer is the entry at its offered version: the new one waits until IT picks it in Editar
      toast.success(entry ? `Versión ${nextVersion} publicada. Elígela en Editar para ofrecerla.` : 'Registrado. Ofrécelo para que las apps puedan usarlo.')
      onDone(published)
    }
    if (entry) publish.mutate({ recipe: entry.recipe, body: draft }, { onSuccess: done })
    else register.mutate({ recipe: id, kind, ...draft }, { onSuccess: done })
  }

  const text = kindText[kind]
  return (
    <form onSubmit={submit} className="flex flex-col gap-5" noValidate>
      <DialogHeader>
        <DialogTitle>{entry ? `Versión nueva de ${entry.title}` : 'Registrar un recurso que ya existe'}</DialogTitle>
        <DialogDescription>
          {entry
            ? `Será la versión ${nextVersion} y parte de la ${entry.version}, la que se ofrece. Lo ya creado sigue en la suya, y la nueva no se ofrece hasta que la elijas en Editar.`
            : 'Una base de datos, una caché o un bucket que la empresa ya tiene. Odicea no la crea ni la borra: solo la conecta a las apps que TI deje.'}
        </DialogDescription>
      </DialogHeader>
      {!entry && kinds.length > 1 && (
        <ChipGroup label="Tipo" value={kind} onChange={pick} options={kinds.map(k => ({ value: k.kind, label: kindText[k.kind].label }))} />
      )}
      <FieldGroup>
        <Field data-invalid={(tried && !!tProblem) || undefined}>
          <FieldLabel htmlFor="recipe-title">Nombre</FieldLabel>
          <Input id="recipe-title" value={title} maxLength={MAX_TITLE} autoComplete="off" placeholder={kind === 'object-storage' ? 'Bucket de reportes' : kind === 'redis' ? 'Caché de sesiones' : 'Base de reportes'} onChange={e => setTitle(e.target.value)} />
          {tried && tProblem ? <FieldError>{tProblem}</FieldError> : <FieldDescription>Es lo que verán quienes crean apps al agregar {text.the}.</FieldDescription>}
        </Field>
        <Field>
          <FieldLabel htmlFor="recipe-description">Descripción <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
          <Textarea id="recipe-description" value={description} maxLength={MAX_DESCRIPTION} placeholder="Para qué sirve y quién responde por ella." onChange={e => setDescription(e.target.value)} />
        </Field>
      </FieldGroup>

      <section className="flex flex-col gap-1" aria-label="Conexión">
        <h3 className="text-[13px] font-medium">De dónde sale cada dato</h3>
        <p className="text-xs text-muted-foreground">Lo que escribas aquí lo verán solo TI y el clúster. Las contraseñas y llaves se leen del almacén de secretos de la empresa.</p>
        <div className="mt-2 flex flex-col divide-y">
          {contract.variables.map(v =>
            v.derived ? (
              <div key={v.name} className="flex flex-col gap-0.5 py-3 text-[13px]">
                <span className="font-medium">{capital(meaningOf(v).what)}</span>
                <span className="text-xs text-muted-foreground">Se arma sola con los datos de abajo.{technical ? ` (${v.name})` : ''}</span>
              </div>
            ) : (
              <VariableRow key={v.name} v={v} row={rows[v.name]} stores={stores} technical={technical} disabled={write.isPending}
                showProblem={tried || touched(rows[v.name])} onChange={patch => setRow(v.name, patch)} />
            ),
          )}
        </div>
        {tried && cProblem && !rowProblem(contract.variables.find(x => x.name === cProblem.variable)!, rows[cProblem.variable!]) && <FieldError>{cProblem.text}</FieldError>}
      </section>

      {needsStore && stores.length === 0 ? (
        <Callout tone="warn" title="Ningún clúster de tu empresa lee de un almacén de secretos.">
          La contraseña de {text.the} solo se lee de ahí. Configura uno en el clúster y vuelve a intentarlo.
        </Callout>
      ) : named && !mayStores ? (
        <Callout tone="warn" title="Tu cuenta no puede usar el almacén de secretos de la empresa.">
          Pide ese permiso a quien administra los accesos de Odicea.
        </Callout>
      ) : missing.length > 0 ? (
        <Callout tone="warn" title={`Ningún clúster lee ahora del almacén ${missing.map(s => `«${s}»`).join(', ')}.`}>Elige otro almacén.</Callout>
      ) : (
        named && (readers.clusters.length > 0 ? (
          <p className="text-xs text-muted-foreground">Lo pueden usar las apps de {readers.clusters.length === 1 ? 'el clúster' : 'los clústeres'} {readers.clusters.join(', ')}: {readers.clusters.length === 1 ? 'es el que lee' : 'son los que leen'} de esos almacenes.</p>
        ) : (
          <Callout tone="warn" title="Ningún clúster lee de todos esos almacenes a la vez.">Ninguna app podría usarlo. Usa almacenes que lea un mismo clúster.</Callout>
        ))
      )}

      <Callout tone="info" title="Revisa la salida a la red.">
        {target?.internal
          ? `${target.address} es una dirección de una red privada: las apps no llegan a ella hasta que la agregues a la salida permitida de su clúster (Administración → Clústeres).`
          : `Si la salida del clúster está restringida, agrega la red de ${target ? target.address : 'este recurso'} a la salida permitida (Administración → Clústeres).`}
      </Callout>

      {technical && (
        <TechnicalDetails title="Qué recibe la app">
          <div className="flex flex-col gap-1 px-5 py-3 text-xs text-muted-foreground">
            <span className="font-mono">{id}@{nextVersion}</span>
            <span className="font-mono break-all">{contract.variables.filter(v => v.derived || rows[v.name]?.source !== 'none').map(v => v.name).join('  ')}</span>
          </div>
        </TechnicalDetails>
      )}
      <ErrorNotice error={write.error} />
      <DialogFooter>
        <span className="mr-auto self-center text-xs text-muted-foreground">{entry ? (unchanged ? 'No cambiaste nada todavía.' : '') : 'Se registra apagado: después eliges dónde se ofrece.'}</span>
        <Button type="button" variant="outline" onClick={onClose} disabled={write.isPending}>Cancelar</Button>
        <Button type="submit" variant="primary" disabled={write.isPending || unchanged || blocked}>
          {write.isPending ? 'Guardando…' : entry ? `Publicar versión ${nextVersion}` : 'Registrar'}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** A row the person started filling: its problem may be said before they send. */
const touched = (r: Row | undefined) => !!r && (r.value !== '' || r.key !== '' || r.property !== '')

const placeholders: Record<string, string> = { host: 'db.empresa.com', port: '5432', url: 'https://s3.empresa.com' }

/** One variable of the connection: a written value, a reference to a secret store, or nothing (optional ones). */
function VariableRow({ v, row, stores, technical, disabled, showProblem, onChange }: {
  v: CatalogKindVariable
  row: Row
  stores: CatalogStore[]
  technical: boolean
  disabled: boolean
  showProblem: boolean
  onChange: (patch: Partial<Row>) => void
}) {
  const sources = sourcesOf(v)
  const problem = showProblem ? rowProblem(v, row) : undefined
  const { what, hint } = meaningOf(v)
  const label = capital(what)
  const fieldId = `var-${v.name}`
  const storeNames = [...new Set([...stores.map(s => s.name), ...(row.store ? [row.store] : [])])]
  return (
    <div className="flex flex-col gap-2 py-3" role="group" aria-label={label}>
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <span className="font-medium">{label}</span>
        {v.secret && <Badge variant="secondary"><KeyRound /> Credencial</Badge>}
        {v.optional && <span className="text-xs text-muted-foreground">Opcional{hint ? ` · ${capital(hint)}` : ''}</span>}
        {!v.optional && hint && <span className="text-xs text-muted-foreground">{capital(hint)}</span>}
        {technical && <span className="ml-auto font-mono text-xs text-muted-foreground">{v.name}</span>}
      </div>
      {sources.length > 1 && (
        <ChipGroup label={`De dónde sale ${what}`} value={row.source} onChange={source => onChange({ source })} options={sources.map(s => ({ value: s, label: sourceLabel[s] }))} />
      )}
      {row.source === 'value' && (
        v.choices.length > 0 ? (
          <Select value={row.value} onValueChange={x => onChange({ value: (x as string | null) ?? '' })} items={Object.fromEntries(v.choices.map(c => [c, c]))}>
            <SelectTrigger id={fieldId} className="w-full" aria-label={label} disabled={disabled}><SelectValue placeholder="Elige uno" /></SelectTrigger>
            <SelectContent>{v.choices.map(c => <SelectItem key={c} value={c}>{c}</SelectItem>)}</SelectContent>
          </Select>
        ) : (
          <Input id={fieldId} aria-label={label} className="font-mono" value={row.value} autoComplete="off" spellCheck={false} disabled={disabled}
            placeholder={placeholders[v.format] ?? ''} inputMode={v.format === 'port' ? 'numeric' : undefined} onChange={e => onChange({ value: e.target.value })} />
        )
      )}
      {row.source === 'store' && (
        <div className="grid gap-2 sm:grid-cols-[1fr_1.4fr_1fr]">
          <Field>
            <FieldLabel htmlFor={`${fieldId}-store`} className="text-xs">Almacén</FieldLabel>
            <Select value={row.store} onValueChange={x => onChange({ store: (x as string | null) ?? '' })} items={Object.fromEntries(storeNames.map(s => [s, s]))}>
              <SelectTrigger id={`${fieldId}-store`} className="w-full" disabled={disabled || storeNames.length === 0}><SelectValue placeholder="Ninguno" /></SelectTrigger>
              <SelectContent>{storeNames.map(s => <SelectItem key={s} value={s}>{s}</SelectItem>)}</SelectContent>
            </Select>
          </Field>
          <Field>
            <FieldLabel htmlFor={`${fieldId}-key`} className="text-xs">Llave en el almacén</FieldLabel>
            <Input id={`${fieldId}-key`} className="font-mono" value={row.key} autoComplete="off" spellCheck={false} disabled={disabled} placeholder="rds/reportes" onChange={e => onChange({ key: e.target.value })} />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${fieldId}-property`} className="text-xs">Propiedad <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
            <Input id={`${fieldId}-property`} className="font-mono" value={row.property} autoComplete="off" spellCheck={false} disabled={disabled} placeholder={v.secret ? 'password' : ''} onChange={e => onChange({ property: e.target.value })} />
          </Field>
        </div>
      )}
      {problem ? <FieldError>{problem}</FieldError> : row.source === 'store' && (
        <FieldDescription className="text-xs">
          {v.secret ? 'El clúster la lee del almacén: nunca pasa por Odicea. ' : ''}La propiedad es el campo dentro del secreto, si guarda varios.
        </FieldDescription>
      )}
    </div>
  )
}
