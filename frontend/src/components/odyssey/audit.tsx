import type { ReactNode } from 'react'
import { Link2, ShieldAlert, ShieldCheck, ShieldQuestion } from 'lucide-react'
import { cn } from '@/lib/utils'
import { ChannelChip, type ChannelData } from './channel'

/**
 * The company log's integrity seal (UX v4 §5.5): whether the chain is
 * intact and when it was last verified. Icon + text, never colour alone. A
 * broken chain is the page's red alert, not this seal.
 */
export type SealKind = 'ok' | 'checking' | 'unknown' | 'broken'

const seal: Record<SealKind, { icon: typeof ShieldCheck; className: string }> = {
  ok: { icon: ShieldCheck, className: 'border-success/40 text-success' },
  checking: { icon: ShieldQuestion, className: 'border-border text-muted-foreground' },
  unknown: { icon: ShieldQuestion, className: 'border-dashed border-border text-muted-foreground' },
  broken: { icon: ShieldAlert, className: 'border-destructive/45 text-destructive' },
}

export function IntegritySeal({ kind, children, className }: { kind: SealKind; children: ReactNode; className?: string }) {
  const { icon: Icon, className: tone } = seal[kind]
  return (
    <span role="status" className={cn('inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium whitespace-nowrap', tone, className)}>
      <Icon className="size-3.5" aria-hidden />
      {children}
    </span>
  )
}

export interface LogEntryData {
  id: string
  /** The Spanish sentence the event carries ("Publicó la versión …"). */
  summary: string
  /** "Tú", a name, "GitHub", "La plataforma", "Alguien de tu equipo". */
  who: string
  channel: ChannelData
  when: string
  /** The app it is about, when known (a link in the page). */
  app?: ReactNode
  /** Technical mode: seq, type, hash, correlation, payload. */
  technical?: { seq: number; type: string; hash: string; correlation: string; payload: string }
  /** "Ver toda la cadena": everything the same request caused. */
  onChain?: () => void
}

/**
 * One line of the company log: what happened in words, who and through
 * what, when, and (technical mode) the evidence a TI person checks. Rows are
 * separated by lines, not panels (medium density).
 */
export function LogEntry({ entry: e }: { entry: LogEntryData }) {
  return (
    <li className="flex flex-col gap-1 px-5 py-3">
      <div className="text-[13px] font-medium">{e.summary}</div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
        <span>{e.who}</span>
        <ChannelChip channel={e.channel} />
        {e.app && <span>· {e.app}</span>}
        <span>· {e.when}</span>
      </div>
      {e.technical && (
        <div className="mt-1 flex flex-col gap-1 text-xs text-muted-foreground">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="font-mono">#{e.technical.seq}</span>
            <span className="font-mono">{e.technical.type}</span>
            <span className="font-mono" title={e.technical.hash}>{e.technical.hash.slice(0, 12)}</span>
            {e.technical.correlation && <span className="font-mono" title="correlation_id">{e.technical.correlation}</span>}
            {e.onChain && (
              <button type="button" onClick={e.onChain} className="inline-flex cursor-pointer items-center gap-1 text-foreground underline-offset-4 outline-none hover:underline focus-visible:underline">
                <Link2 className="size-3" aria-hidden />
                Ver toda la cadena
              </button>
            )}
          </div>
          <pre className="max-h-40 overflow-auto rounded-md bg-muted/60 px-3 py-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap">{e.technical.payload}</pre>
        </div>
      )}
    </li>
  )
}
