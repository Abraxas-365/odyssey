import type { ReactNode } from 'react'
import { BellRing, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { IconBadge } from './icon-badge'
import { Panel, PanelHeader } from './panel'
import type { Tone } from './tone'

/**
 * One "Necesita tu atención" item (UX v3 §3): what happened, whether it
 * affects anyone, and exactly ONE action. Put a RequestTracker in `children`
 * for requests in flight.
 */
export function AttentionItem({ tone, icon, title, children, action, extra, className }: { tone: Tone; icon: LucideIcon; title: ReactNode; children?: ReactNode; action?: ReactNode; extra?: ReactNode; className?: string }) {
  return (
    <li className={cn('flex items-start gap-3.5 px-5 py-4', className)}>
      <IconBadge icon={icon} tone={tone} />
      <div className="min-w-0 flex-1">
        <div className="font-semibold">{title}</div>
        {children && <div className="mt-0.5 max-w-[640px] text-[13px] text-muted-foreground [&_b]:font-semibold [&_b]:text-foreground">{children}</div>}
        {extra}
      </div>
      {action && <div className="flex shrink-0 items-center gap-2">{action}</div>}
    </li>
  )
}

/** The home-page card that holds AttentionItems; renders nothing when empty. */
export function AttentionList({ title = 'Necesita tu atención', children, count }: { title?: ReactNode; children: ReactNode; count: number }) {
  if (count === 0) return null
  return (
    <Panel aria-label={typeof title === 'string' ? title : undefined}>
      <PanelHeader icon={BellRing} title={title} meta={count} />
      <ul role="list" className="divide-y">
        {children}
      </ul>
    </Panel>
  )
}
