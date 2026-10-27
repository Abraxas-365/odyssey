import type { ReactNode } from 'react'
import { AlertTriangle, CheckCircle2, CircleAlert, Info, Sparkles, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { toneSurface, toneText, type Tone } from './tone'

const defaultIcon: Record<Tone, LucideIcon> = { ok: CheckCircle2, warn: AlertTriangle, bad: CircleAlert, info: Info, ai: Sparkles, neutral: Info }

/**
 * Mockup `.callout`. Lead with the consequence in bold, explain in muted text
 * (UX v3 §4): "<b>Tu app sigue funcionando.</b> Nadie se vio afectado."
 */
export function Callout({
  tone = 'neutral',
  icon,
  title,
  children,
  action,
  className,
}: {
  tone?: Tone
  icon?: LucideIcon
  title?: ReactNode
  children?: ReactNode
  action?: ReactNode
  className?: string
}) {
  const Icon = icon ?? defaultIcon[tone]
  return (
    <div
      role={tone === 'bad' ? 'alert' : 'status'}
      className={cn('flex items-start gap-3 rounded-lg border px-4 py-3.5 text-[13px]', action && 'items-center', toneSurface({ tone }), className)}
    >
      <Icon className={cn('mt-px size-4 shrink-0', toneText[tone === 'neutral' ? 'info' : tone])} aria-hidden />
      <div className="min-w-0 flex-1">
        {title && <span className="font-semibold text-foreground">{title} </span>}
        {children && <span className="text-muted-foreground">{children}</span>}
      </div>
      {action && <div className="flex shrink-0 items-center gap-2">{action}</div>}
    </div>
  )
}
