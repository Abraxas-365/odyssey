import type { ReactNode } from 'react'
import { Check, ShieldAlert, X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { Callout } from './callout'
import { Avatar } from './identity'
import { KeyValue } from './stat'
import { toneBadge, type Tone } from './tone'

/**
 * Request in an approver's Bandeja (UX v3 §10) or TI's Solicitudes (§11).
 * Every request explains, in plain words: who asks, why, what they'll be
 * able / not able to do, for how long, and what was checked automatically.
 */
export interface ApprovalRequestData {
  id: string
  requester: { name: string; team?: string; color?: string }
  title: string
  context: string
  /** "Lo pidió su asistente de IA, en su nombre". */
  viaAssistant?: boolean
  reason?: string
  allows: string[]
  denies: string[]
  audience?: ReactNode
  checks?: { label: string; ok: boolean; fix?: ReactNode }[]
  /** Extra approver needed (e.g. Seguridad for public apps). */
  alsoNeeds?: ReactNode
  state: 'pending' | 'approved' | 'rejected'
}

/** Inbox list row: avatar, one-line summary, pending dot. */
export function ApprovalRow({ request: r, selected, onSelect }: { request: ApprovalRequestData; selected?: boolean; onSelect?: () => void }) {
  const done = r.state !== 'pending'
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected || undefined}
        className={cn('flex w-full cursor-pointer items-center gap-3.5 px-5 py-3.5 text-left outline-none hover:bg-muted/50 focus-visible:bg-muted/50', selected && 'bg-muted', done && 'text-muted-foreground')}
      >
        {done ? (
          <span className={cn('grid size-8 shrink-0 place-items-center rounded-full border', r.state === 'approved' ? 'border-success/40 text-success' : 'border-destructive/45 text-destructive')}>
            {r.state === 'approved' ? <Check className="size-4" /> : <X className="size-4" />}
          </span>
        ) : (
          <Avatar name={r.requester.name} color={r.requester.color} size="lg" />
        )}
        <div className="min-w-0 flex-1">
          <div className={cn('truncate', selected && !done && 'font-semibold')}>{r.title}</div>
          <div className="truncate text-xs text-muted-foreground">{r.context}</div>
        </div>
        {!done && <span className="size-[9px] shrink-0 rounded-full bg-ai" aria-label="Pendiente" />}
      </button>
    </li>
  )
}

/** Detail body of a request (goes inside a Panel, above the decision footer). */
export function ApprovalDetail({ request: r, duration }: { request: ApprovalRequestData; duration?: ReactNode }) {
  return (
    <div className="flex flex-col gap-3.5 p-5">
      {r.reason && <blockquote className="rounded-md border bg-inset p-3.5 text-[13px] text-foreground">“{r.reason}”</blockquote>}
      <div className="grid gap-4 sm:grid-cols-2">
        <CanList title="Si dices que sí, la app podrá" items={r.allows} ok />
        <CanList title="No podrá" items={r.denies} />
      </div>
      {r.checks && (
        <div className="flex flex-col gap-1.5">
          <div className="text-xs text-muted-foreground">Revisiones automáticas</div>
          {r.checks.map(c => (
            <div key={c.label} className="flex items-center gap-2 text-[13px]">
              {c.ok ? <Check className="size-4 text-success" aria-label="Cumple" /> : <ShieldAlert className="size-4 text-warning" aria-label="Pendiente" />}
              {c.label}
              {!c.ok && c.fix}
            </div>
          ))}
        </div>
      )}
      {r.alsoNeeds && <Callout tone="warn">{r.alsoNeeds}</Callout>}
      {(r.audience || duration) && (
        <KeyValue
          items={[
            ...(r.audience ? [['Quién lo verá', r.audience] as [ReactNode, ReactNode]] : []),
            ...(duration ? [['Duración', duration] as [ReactNode, ReactNode]] : []),
          ]}
        />
      )}
    </div>
  )
}

function CanList({ title, items, ok }: { title: string; items: string[]; ok?: boolean }) {
  return (
    <div className="flex flex-col gap-2">
      <div className="text-xs text-muted-foreground">{title}</div>
      <ul className="flex flex-col gap-1.5">
        {items.map(t => (
          <li key={t} className="flex items-center gap-2 text-[13px]">
            {ok ? <Check className="size-4 shrink-0 text-success" aria-hidden /> : <X className="size-4 shrink-0 text-destructive" aria-hidden />}
            {t}
          </li>
        ))}
      </ul>
    </div>
  )
}

/**
 * One request of the Bandeja (docs/approvals.md): the sentence, who and why,
 * the rule, when and until when. `actions` carries Aprobar / Rechazar for
 * IT, or Cancelar for the person who asked.
 */
export interface ApprovalItemData {
  id: string
  /** "Hacer pública «Inventario»": the action in one sentence. */
  sentence: string
  who: string
  /** Why it was held, in words ("Hacer pública una app requiere aprobación de TI"). */
  why: string
  rules?: string
  when: string
  /** "Vence en 6 días" (pending) or how it ended (decided). */
  until?: string
  status: { label: string; tone: Tone }
  /** The note the decider left. */
  note?: string
  /** An approved action that could not run. */
  problem?: string
}

export function ApprovalItem({ item: i, actions }: { item: ApprovalItemData; actions?: ReactNode }) {
  return (
    <li className="flex flex-col gap-2.5 px-5 py-4">
      <div className="flex flex-wrap items-start gap-3.5">
        <Avatar name={i.who} size="lg" />
        <div className="min-w-0 flex-1">
          <div className="font-semibold">{i.sentence}</div>
          <div className="text-xs text-muted-foreground">
            {i.who} · {i.when}
            {i.until && <> · {i.until}</>}
          </div>
        </div>
        <Badge variant={toneBadge[i.status.tone]}>{i.status.label}</Badge>
      </div>
      <div className="pl-[46px] text-[13px] text-muted-foreground">
        {i.why}
        {i.rules && <> · Regla {i.rules}</>}
      </div>
      {i.note && <blockquote className="ml-[46px] rounded-md border bg-inset p-3 text-[13px] text-foreground">“{i.note}”</blockquote>}
      {i.problem && (
        <Callout tone="warn" title="Se aprobó, pero no se pudo hacer." className="ml-[46px]">
          {i.problem}
        </Callout>
      )}
      {actions && <div className="flex flex-wrap items-center justify-end gap-2">{actions}</div>}
    </li>
  )
}
