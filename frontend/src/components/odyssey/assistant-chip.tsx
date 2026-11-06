import { Link } from 'react-router-dom'
import { Sparkles } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * The header's assistant chip (UX v4 §6.1): "✦ Conecta tu asistente" as a
 * call to action until one is connected (`connect`), then what is connected
 * and when it was used ("✦ Claude Code · hace 5 min"). `unknown` (we could not
 * ask) is quiet, never the call to action: it is not "not connected". Either
 * way it opens the assistant page. Words come from features/assistant/state.ts.
 */
export function AssistantChip({ label, kind, to = '/asistente', className }: { label: string; kind: 'connect' | 'unknown' | 'connected'; to?: string; className?: string }) {
  return (
    <Link
      to={to}
      aria-label={kind === 'connected' ? `Tu asistente: ${label}` : label}
      className={cn(
        'inline-flex h-[30px] max-w-[260px] shrink-0 items-center gap-1.5 rounded-full border px-3 text-[13px] font-medium whitespace-nowrap outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
        kind === 'connect' ? 'border-transparent bg-ai/15 text-ai hover:bg-ai/25' : 'border-border text-secondary-foreground hover:bg-muted/50',
        className,
      )}
    >
      <Sparkles className="size-3.5 shrink-0 text-ai" aria-hidden />
      {kind === 'connected' && <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-success" />}
      <span className="hidden truncate sm:inline">{label}</span>
    </Link>
  )
}
