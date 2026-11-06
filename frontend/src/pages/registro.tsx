import { Link, useSearchParams } from 'react-router-dom'
import { ScrollText, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Callout, EmptyState, ErrorNotice, IntegritySeal, ListSkeleton, LogEntry, PageHeader, Panel, PanelFooter, PanelList, type LogEntryData } from '@/components/odyssey'
import { channelFilters } from '@/features/apps/attribution'
import { channelOf, personLabel, projectOf, sealOf, weekSentence, whoOf } from '@/features/audit/audit'
import { useAuditEntries, useAuditPeople, useAuditSummary, useAuditVerification } from '@/lib/audit'
import { useAuth } from '@/lib/auth'
import { formatDateTime, formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import { useProjects } from '@/lib/projects'
import type { AuditEntry, AuditQuery } from '@/lib/types'

const PAGE = 25
const ALL = 'all'

/** The log's filters live in the address: links and "ver toda la cadena" open it already narrowed. */
const keys = { person: 'persona', channel: 'canal', app: 'app', chain: 'cadena', offset: 'desde' } as const

/**
 * Registro (UX v4 §5.5, TI only): everything that happened in the company —
 * who, through what (console, an assistant, GitHub, the platform) and when —
 * with the chain's integrity seal. Technical mode adds the evidence (seq,
 * hash, correlation, payload) and "ver toda la cadena".
 */
export function RegistroPage() {
  const { can, user } = useAuth()
  const allowed = can(permissions.auditRead)
  const { technical } = usePreferences()
  const [search, setSearch] = useSearchParams()
  const person = search.get(keys.person) ?? ''
  const channel = search.get(keys.channel) ?? ''
  const app = search.get(keys.app) ?? ''
  const chain = search.get(keys.chain) ?? ''
  const offset = Math.max(0, Number(search.get(keys.offset)) || 0)
  const set = (changes: Partial<Record<keyof typeof keys, string>>) => {
    const next = new URLSearchParams(search)
    for (const [k, v] of Object.entries(changes)) {
      if (v && v !== ALL) next.set(keys[k as keyof typeof keys], v)
      else next.delete(keys[k as keyof typeof keys])
    }
    if (!('offset' in changes)) next.delete(keys.offset)
    setSearch(next, { replace: true })
  }

  const q: AuditQuery = {
    actor_id: person || undefined,
    channel: channel || undefined,
    project_id: app || undefined,
    correlation_id: chain || undefined,
    limit: PAGE,
    offset,
  }
  // Anyone without the permission would get a 403: do not ask.
  const entries = useAuditEntries(q, allowed)
  const summary = useAuditSummary(allowed)
  const people = useAuditPeople(allowed)
  const verification = useAuditVerification(allowed)
  const apps = useProjects({ limit: 100 })

  if (!allowed) {
    return (
      <>
        <PageHeader title="Registro" />
        <EmptyState icon={ScrollText} title="Solo TI puede ver el registro de la empresa">Lo que pasó con tus apps está en el Historial de cada una.</EmptyState>
      </>
    )
  }

  const appName = new Map((apps.data?.items ?? []).map(p => [p.id, p.name]))
  const state = sealOf(verification.data, !!verification.error)
  const filtered = !!(person || channel || app || chain)
  const rows = entries.data?.items ?? []
  const total = entries.data?.page.total ?? 0

  const toRow = (e: AuditEntry): LogEntryData => {
    const id = projectOf(e)
    return {
      id: e.id,
      summary: e.summary,
      who: whoOf(e, user.id),
      channel: channelOf(e.channel),
      when: formatRelative(e.occurred_at),
      app: id && !app ? <Link to={`/apps/${id}`} className="hover:underline">{appName.get(id) ?? 'una app'}</Link> : undefined,
      technical: technical
        ? { seq: e.seq, type: e.type, hash: e.hash, correlation: e.correlation_id, payload: JSON.stringify(e.payload, null, 2) }
        : undefined,
      onChain: technical && e.correlation_id && e.correlation_id !== chain ? () => set({ chain: e.correlation_id, person: '', channel: '', app: '' }) : undefined,
    }
  }

  const items = (all: string, pairs: [string, string][]): Record<string, string> => Object.fromEntries([[ALL, all], ...pairs])
  const personItems = items('Todas las personas', (people.data?.items ?? []).map(p => [p.actor_id, personLabel(p, user.id)]))
  const channelItems = items('Todos los canales', channelFilters.map(c => [c.value, c.label]))
  const appItems = items('Todas las apps', (apps.data?.items ?? []).map(p => [p.id, p.name]))

  return (
    <>
      <PageHeader
        title="Registro"
        description="Todo lo que se hizo en la empresa: quién, por dónde y cuándo."
        actions={
          state.kind === 'ok' ? <IntegritySeal kind="ok">{state.text}</IntegritySeal>
          : state.kind === 'broken' ? <IntegritySeal kind="broken">Registro alterado</IntegritySeal>
          : state.kind === 'unknown' ? <IntegritySeal kind="unknown">No pudimos verificar el registro</IntegritySeal>
          : state.kind === 'checking' ? <IntegritySeal kind="checking">Verificando…</IntegritySeal>
          : null
        }
      />

      {state.kind === 'broken' && (
        <Callout tone="bad" title={state.at ? `El registro fue alterado a partir de la entrada #${state.at}.` : 'El registro fue alterado.'}>
          {state.at
            ? `Esa entrada no coincide con su huella: alguien cambió o borró historial directamente en la base de datos, saltándose Odicea. Lo registrado antes de la #${state.at} sigue siendo confiable. `
            : 'Alguien cambió o borró historial directamente en la base de datos, saltándose Odicea. '}
          Avisa de inmediato a quien lleva la seguridad de la información en tu empresa y no corrijas nada a mano: la evidencia es lo que está guardado ahora.
        </Callout>
      )}
      {state.kind === 'unknown' && (
        <ErrorNotice error={verification.error} onRetry={() => verification.refetch()} />
      )}

      {summary.data && <p className="text-[13px] text-muted-foreground">{weekSentence(summary.data)}</p>}

      <div className="flex flex-wrap items-center gap-2">
        <Select value={person || ALL} items={personItems} onValueChange={v => set({ person: v ?? ALL })}>
          <SelectTrigger size="sm" className="w-52" aria-label="Persona"><SelectValue /></SelectTrigger>
          <SelectContent>
            {Object.entries(personItems).map(([v, label]) => <SelectItem key={v} value={v}>{label}</SelectItem>)}
          </SelectContent>
        </Select>
        <Select value={channel || ALL} items={channelItems} onValueChange={v => set({ channel: v ?? ALL })}>
          <SelectTrigger size="sm" className="w-48" aria-label="Canal"><SelectValue /></SelectTrigger>
          <SelectContent>
            {Object.entries(channelItems).map(([v, label]) => <SelectItem key={v} value={v}>{label}</SelectItem>)}
          </SelectContent>
        </Select>
        <Select value={app || ALL} items={appItems} onValueChange={v => set({ app: v ?? ALL })}>
          <SelectTrigger size="sm" className="w-52" aria-label="App"><SelectValue /></SelectTrigger>
          <SelectContent>
            {Object.entries(appItems).map(([v, label]) => <SelectItem key={v} value={v}>{label}</SelectItem>)}
          </SelectContent>
        </Select>
        {chain && (
          <span className="inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs">
            Toda la cadena de <span className="font-mono">{chain.slice(0, 12)}</span>
          </span>
        )}
        {filtered && (
          <Button variant="ghost" size="sm" onClick={() => set({ person: '', channel: '', app: '', chain: '' })}>
            <X />
            Quitar filtros
          </Button>
        )}
      </div>

      <ErrorNotice error={entries.error} onRetry={() => entries.refetch()} />
      {entries.isPending ? (
        <Panel aria-busy><ListSkeleton rows={5} /></Panel>
      ) : rows.length === 0 && !entries.error ? (
        filtered ? (
          <EmptyState icon={ScrollText} title="Nada coincide con estos filtros">Prueba con otra persona, canal o app.</EmptyState>
        ) : (
          <EmptyState icon={ScrollText} title="Aún no hay nada registrado">
            Aquí aparecerá cada cosa que se haga en Odicea: apps creadas, versiones publicadas (desde la consola, un asistente o GitHub), cambios de quién puede abrirlas, aprobaciones y reglas de TI. Cada entrada queda encadenada a la anterior, así que nadie puede borrarla ni cambiarla sin que se note.
          </EmptyState>
        )
      ) : rows.length > 0 ? (
        <Panel>
          <PanelList aria-label="Registro">
            {rows.map(e => <LogEntry key={e.id} entry={toRow(e)} />)}
          </PanelList>
          <PanelFooter>
            <span>
              {`${offset + 1}–${offset + rows.length} de ${total}`}
              {rows[rows.length - 1] && <> · hasta el {formatDateTime(rows[rows.length - 1].occurred_at)}</>}
            </span>
            <span className="flex gap-2">
              <Button variant="outline" size="sm" disabled={offset === 0} onClick={() => set({ offset: String(Math.max(0, offset - PAGE)) })}>Más recientes</Button>
              <Button variant="outline" size="sm" disabled={offset + PAGE >= total} onClick={() => set({ offset: String(offset + PAGE) })}>Anteriores</Button>
            </span>
          </PanelFooter>
        </Panel>
      ) : null}
    </>
  )
}
