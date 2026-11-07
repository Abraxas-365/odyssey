import { useState } from 'react'
import { Boxes, Plus, ScanSearch, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Callout, ErrorNotice, ListSkeleton, Panel, PanelBody, PanelFooter, PanelHeader, PanelList, PanelRow, Term } from '@/components/odyssey'
import { useProjectRepository } from '@/lib/github'
import { errorMessage } from '@/lib/api'
import { useDetectRepository, useServices, useSetServices } from '@/lib/services'
import type { DetectedServices, Service, ServiceKind } from '@/lib/types'
import { afterWrite } from '@/features/approvals/notice'
import { bodyOf, draftOf, fromDetected, hasProblems, isMulti, isSimple, kindText, MAX_SERVICES, newDraft, problemsOf, sameList, whereText, type Draft } from './services'

/**
 * App → Configuración → Servicios (MS-6): the parts the app is made of. An
 * app of one part shows only the offer to split it; with two or more each
 * part shows its folder, where it answers and its kind. Saving changes the
 * next version: what is online keeps running until then.
 */
export function ServicesPanel({ project, canWrite, technical }: { project: string; canWrite: boolean; technical: boolean }) {
  const services = useServices(project)
  const [editing, setEditing] = useState<Draft[] | null>(null)
  const [notes, setNotes] = useState<string[]>([])

  if (services.isPending) return <Panel><PanelHeader icon={Boxes} title="Partes de la app" /><ListSkeleton rows={2} /></Panel>
  if (services.error) return <ErrorNotice error={services.error} onRetry={() => services.refetch()} />
  const saved = services.data.items
  const edit = (rows: Draft[], detected?: DetectedServices) => {
    setEditing(rows)
    setNotes(detected ? [...detected.notes, ...(detected.truncated ? ['GitHub no entregó todo el código: revisa que no falte ninguna parte.'] : [])] : [])
  }

  if (editing) return <ServicesEditor project={project} saved={saved} initial={editing} notes={notes} onDone={() => setEditing(null)} />

  if (!isMulti(saved)) {
    return (
      <Panel>
        <PanelHeader
          icon={Boxes}
          title="Partes de la app"
          description="Tu app se publica como una sola pieza. Si tu código tiene varias (por ejemplo una web y una API en carpetas distintas), sepáralas: cada una se construye y se revisa por su cuenta, y todas salen juntas en la misma versión."
          actions={canWrite && <SplitActions project={project} onEdit={edit} rows={saved.map(draftOf)} />}
        />
        {technical && <PanelBody><p className="font-mono text-xs text-muted-foreground">{saved.map(s => `${s.name} · ${s.kind} · ${s.route || '—'} · ${s.root || '.'}`).join('\n')}</p></PanelBody>}
      </Panel>
    )
  }

  return (
    <Panel>
      <PanelHeader
        icon={Boxes}
        title={<Term help="Cada servicio se construye desde su carpeta. Todos salen juntos en la misma versión, y volver a una versión anterior los devuelve a todos como estaban.">Servicios</Term>}
        description="Las partes de tu app. Un cambio aquí se aplica en la próxima versión que publiques."
        actions={canWrite && <SplitActions project={project} onEdit={edit} rows={saved.map(draftOf)} editLabel="Editar" />}
      />
      <PanelList aria-label="Servicios de la app">
        {saved.map(s => <ServiceRow key={s.name} service={s} technical={technical} />)}
      </PanelList>
    </Panel>
  )
}

function ServiceRow({ service: s, technical }: { service: Service; technical: boolean }) {
  return (
    <PanelRow className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <span className="font-mono text-[13px] font-medium">{s.name}</span>
      <Badge variant="secondary">{kindText[s.kind].label}</Badge>
      <span className="text-xs text-muted-foreground">{whereText(s)}</span>
      <span className="ml-auto font-mono text-xs text-muted-foreground">{s.root || 'todo el código'}</span>
      {technical && <span className="w-full font-mono text-[11px] text-muted-foreground">kind={s.kind} route={s.route || '""'} strip_prefix={String(s.strip_prefix)} root={s.root || '""'}</span>}
    </PanelRow>
  )
}

/** "Detectar desde GitHub" (when connected) and editing by hand. */
function SplitActions({ project, rows, onEdit, editLabel = 'Separar a mano' }: { project: string; rows: Draft[]; onEdit: (rows: Draft[], detected?: DetectedServices) => void; editLabel?: string }) {
  const repository = useProjectRepository(project)
  const detect = useDetectRepository()
  const conn = repository.data
  const multi = rows.length >= 2
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {conn && (
        <Button
          size="sm"
          variant="outline"
          disabled={detect.isPending}
          onClick={() =>
            detect.mutate(
              { installation: conn.installation_id, repository: conn.repository_id, branch: conn.branch, root: conn.root },
              {
                onSuccess: d => {
                  if (d.items.length === 0) toast.message('No encontramos partes en el código: puedes agregarlas a mano.')
                  onEdit(d.items.length ? d.items.map(fromDetected) : rows, d)
                },
                onError: e => toast.error(errorMessage(e)),
              },
            )
          }
        >
          {detect.isPending ? <Spinner /> : <ScanSearch />}
          Detectar desde GitHub
        </Button>
      )}
      <Button size="sm" variant="outline" onClick={() => onEdit(multi ? rows : [...rows, newDraft(rows)])}>{editLabel}</Button>
    </div>
  )
}

const kindItems: Record<ServiceKind, string> = { web: kindText.web.label, worker: kindText.worker.label }

function ServicesEditor({ project, saved, initial, notes, onDone }: { project: string; saved: Service[]; initial: Draft[]; notes: string[]; onDone: () => void }) {
  const set = useSetServices(project)
  const [rows, setRows] = useState(initial)
  const problems = problemsOf(rows)
  const unchanged = sameList(rows, saved)
  const update = (i: number, patch: Partial<Draft>) => setRows(rs => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))

  return (
    <Panel>
      <form
        onSubmit={e => {
          e.preventDefault()
          set.mutate({ items: bodyOf(rows) }, {
            onSuccess: out => afterWrite(out, () => {
              toast.success(isSimple(rows) ? 'Listo. Tu app vuelve a ser una sola pieza desde la próxima versión.' : 'Guardado. La próxima versión que publiques ya sale con estas partes.')
              onDone()
            }, onDone),
          })
        }}
      >
        <PanelHeader icon={Boxes} title="Partes de la app" description="Cada parte se construye desde su carpeta. La que va en / es la dirección principal; las demás responden en su ruta." />
        <PanelBody className="flex flex-col gap-3">
          {notes.map(n => <Callout key={n} tone="info">{n}</Callout>)}
          <ul className="flex flex-col gap-3" aria-label="Partes a guardar">
            {rows.map((r, i) => {
              const p = problems[i]
              const worker = r.kind === 'worker'
              return (
                <li key={i} className="grid grid-cols-1 gap-2 rounded-lg border p-3 sm:grid-cols-[8rem_10rem_1fr_1fr_auto]">
                  <label className="flex flex-col gap-1 text-xs font-medium">
                    Nombre
                    <Input value={r.name} maxLength={20} aria-invalid={!!p.name} onChange={e => update(i, { name: e.target.value.toLowerCase() })} />
                    {p.name && <span className="font-normal text-destructive">{p.name}</span>}
                  </label>
                  <div className="flex flex-col gap-1 text-xs font-medium">
                    <span id={`svc-kind-${i}`}>Tipo</span>
                    <Select value={r.kind} items={kindItems} onValueChange={v => update(i, { kind: (v ?? 'web') as ServiceKind })}>
                      <SelectTrigger aria-labelledby={`svc-kind-${i}`} className="w-full"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        {(Object.keys(kindItems) as ServiceKind[]).map(k => <SelectItem key={k} value={k}>{kindItems[k]}</SelectItem>)}
                      </SelectContent>
                    </Select>
                  </div>
                  <label className="flex flex-col gap-1 text-xs font-medium">
                    Carpeta
                    <Input value={r.root} placeholder="todo el código" maxLength={200} aria-invalid={!!p.root} onChange={e => update(i, { root: e.target.value })} className="font-mono" />
                    {p.root && <span className="font-normal text-destructive">{p.root}</span>}
                  </label>
                  <label className="flex flex-col gap-1 text-xs font-medium">
                    Ruta
                    <Input value={worker ? '' : r.route} disabled={worker} placeholder={worker ? 'sin dirección' : 'vacía: solo interna'} maxLength={100} aria-invalid={!!p.route} onChange={e => update(i, { route: e.target.value })} className="font-mono" />
                    {p.route && <span className="font-normal text-destructive">{p.route}</span>}
                    {!worker && r.route.trim() !== '' && r.route.trim() !== '/' && (
                      <span className="flex items-center gap-1.5 font-normal text-muted-foreground">
                        <Checkbox checked={r.strip_prefix} onCheckedChange={v => update(i, { strip_prefix: !!v })} aria-label={`Quitar ${r.route} antes de llegar a ${r.name}`} />
                        Recibe la ruta sin {r.route.trim()}
                      </span>
                    )}
                  </label>
                  <div className="flex items-end">
                    <Button type="button" size="icon" variant="ghost" disabled={rows.length <= 1} aria-label={`Quitar ${r.name || 'esta parte'}`} onClick={() => setRows(rs => rs.filter((_, j) => j !== i))}><Trash2 /></Button>
                  </div>
                </li>
              )
            })}
          </ul>
          <div>
            <Button type="button" size="sm" variant="outline" disabled={rows.length >= MAX_SERVICES} onClick={() => setRows(rs => [...rs, newDraft(rs)])}><Plus />Agregar una parte</Button>
          </div>
          <ErrorNotice error={set.error} />
        </PanelBody>
        <PanelFooter className="justify-end">
          <Button type="button" variant="outline" onClick={onDone}>Cancelar</Button>
          <Button type="submit" disabled={set.isPending || hasProblems(problems) || unchanged}>{set.isPending ? 'Guardando…' : 'Guardar'}</Button>
        </PanelFooter>
      </form>
    </Panel>
  )
}
