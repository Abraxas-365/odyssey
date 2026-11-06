import { Bot, CircleHelp, GitBranch, Monitor, RotateCcw, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * Where a request came from (UX v4 §2): the console, an AI assistant (by
 * name), GitHub, the platform itself, or — for versions published before it
 * was recorded — "Canal no registrado". Pages build it from the API's
 * `requested_via` only (features/apps/attribution.ts); it is never inferred.
 */
export type ChannelKind = 'console' | 'assistant' | 'github' | 'platform' | 'unrecorded'

export interface ChannelData {
  kind: ChannelKind
  /** What people read: "Consola", "Claude Code", "GitHub", "Automático", "Canal no registrado". */
  label: string
}

const icons: Record<ChannelKind, LucideIcon> = {
  console: Monitor,
  assistant: Bot,
  github: GitBranch,
  platform: RotateCcw,
  unrecorded: CircleHelp,
}

const tones: Record<ChannelKind, string> = {
  console: 'border-border text-secondary-foreground',
  assistant: 'border-transparent bg-ai/15 text-ai',
  github: 'border-border text-secondary-foreground',
  platform: 'border-border text-muted-foreground',
  unrecorded: 'border-dashed border-border text-muted-foreground',
}

/** Icon + text, never colour alone; the aria-label says what it is. */
export function ChannelChip({ channel, className }: { channel: ChannelData; className?: string }) {
  const Icon = icons[channel.kind]
  return (
    <span
      role="img"
      aria-label={`Canal: ${channel.label}`}
      className={cn('inline-flex h-5 shrink-0 items-center gap-1 rounded-full border px-1.5 text-[11px] font-medium whitespace-nowrap', tones[channel.kind], className)}
    >
      <Icon className="size-3" aria-hidden />
      {channel.label}
    </span>
  )
}
