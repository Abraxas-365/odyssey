import { ChevronRight } from 'lucide-react'
import { Callout, ErrorNotice, Loading, Panel, PanelHeader } from '@/components/odyssey'
import type { Tone } from '@/components/odyssey'
import { cn } from '@/lib/utils'
import { useUses } from '@/lib/uses'
import type { EnvironmentUses } from '@/lib/types'
import { alertsOf, attentionAnchor, ASK_IT, unavailableText, usesRows, type UsesRow } from './uses'

const dot: Record<Tone, string> = {
  ok: 'bg-success',
  warn: 'bg-warning',
  bad: 'bg-destructive',
  info: 'bg-info',
  ai: 'bg-ai',
  neutral: 'bg-muted-foreground/40',
}

/** The dot says it for people who see colours; the words say it for everyone else. */
const toneWord: Record<Tone, string> = { ok: 'Bien', warn: 'Falta algo', bad: 'Con problemas', info: 'En curso', ai: 'En curso', neutral: 'Sin uso' }

const calloutTone = (t: Tone): 'warn' | 'bad' | 'info' => (t === 'bad' ? 'bad' : t === 'info' ? 'info' : 'warn')

/** Scrolls to the panel below that changes it, and moves focus there so a keyboard user lands on it. */
function goTo(id: string, e: React.MouseEvent) {
  const el = document.getElementById(id)
  if (!el) return
  e.preventDefault()
  el.scrollIntoView({ behavior: 'smooth', block: 'start' })
  el.focus({ preventScroll: true })
}

/** The link accessible name is its own content (label, state and the words for screen readers): no aria-label hides them. */
function Anchor({ id, children, className }: { id: string; children: React.ReactNode; className?: string }) {
  return (
    <a href={`#${id}`} className={cn('rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring', className)} onClick={e => goTo(id, e)}>
      {children}
    </a>
  )
}

function Row({ row, technical }: { row: UsesRow; technical: boolean }) {
  const body = (
    <>
      <row.icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium">{row.label}</div>
        {technical && row.technical && <div className="font-mono text-xs break-words text-muted-foreground">{row.technical}</div>}
      </div>
      <span className="flex items-center gap-2 text-[13px] text-muted-foreground">
        <span aria-hidden className={cn('inline-block size-[9px] shrink-0 rounded-full', dot[row.tone], row.tone === 'info' && 'animate-pulse-dot')} />
        <span className="sr-only">{toneWord[row.tone]}: </span>
        {row.text}
      </span>
      {row.anchor && (
        <>
          <ChevronRight className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="sr-only">. Ir a cambiarlo</span>
        </>
      )}
    </>
  )
  return (
    <li className="flex items-center gap-3 px-5 py-3 hover:bg-muted/40 has-[a:focus-visible]:bg-muted/40">
      {row.anchor ? (
        <Anchor id={row.anchor} className="flex min-w-0 flex-1 items-center gap-3">
          {body}
        </Anchor>
      ) : (
        <div className="flex min-w-0 flex-1 items-center gap-3">{body}</div>
      )}
    </li>
  )
}

/**
 * Alerts in plain language, worst first, each with the panel that solves it.
 * Only what needs a person: the owner's own to-dos, and what only IT can do
 * (which says so, since IT is not notified yet). What is in progress is the
 * state of its row, not an alert.
 */
export function UsesAlerts({ uses, technical }: { uses: EnvironmentUses; technical: boolean }) {
  const items = alertsOf(uses)
  const unavailable = unavailableText(uses)
  if (items.length === 0 && !unavailable) return null
  return (
    <div className="flex flex-col gap-2" role="group" aria-label="Lo que necesita tu atención">
      {items.map(a => {
        const to = attentionAnchor[a.kind]
        return (
          <Callout
            key={`${a.kind}:${a.message}`}
            tone={calloutTone(a.tone)}
            title={a.message}
            action={
              to && (
                <Anchor id={to} className="text-[13px] font-medium underline underline-offset-2">
                  Ir a cambiarlo
                </Anchor>
              )
            }
          >
            {a.who === 'it' && <span>{ASK_IT} </span>}
            {technical && <span className="font-mono text-xs">{a.kind}</span>}
          </Callout>
        )
      })}
      {unavailable && <Callout tone="info" title={unavailable} />}
    </div>
  )
}

/**
 * "Lo que usa esta app" (cut 2e): one card with what the app depends on and
 * what stands in its way. Each row leads to the panel below that changes it.
 * Read-only: it shows the state, the panels below act on it.
 */
export function UsesCard({ project, env, technical }: { project: string; env: string; technical: boolean }) {
  const uses = useUses(project, env)
  if (uses.isPending) return <Panel><Loading label="Cargando lo que usa la app…" className="px-5 py-4" /></Panel>
  if (!uses.data) return <ErrorNotice error={uses.error} onRetry={() => uses.refetch()} />
  const rows = usesRows(uses.data)
  return (
    <>
      <UsesAlerts uses={uses.data} technical={technical} />
      <Panel aria-label="Lo que usa esta app">
        <PanelHeader title="Lo que usa esta app" description="De un vistazo: quién la abre, sus datos, secretos, IA y salida a internet." />
        <ul role="list" className="divide-y" aria-label="Lo que usa la app">
          {rows.map(r => (
            <Row key={r.key} row={r} technical={technical} />
          ))}
        </ul>
      </Panel>
    </>
  )
}
