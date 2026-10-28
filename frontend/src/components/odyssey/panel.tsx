import type { ComponentProps, ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * Mockup `.card` with optional `.cardhead` / `.cardfoot`. Use Panel for every
 * bordered surface; put PanelList (`.list`) or PanelBody inside.
 */
export function Panel({ className, interactive, dashed, ...props }: ComponentProps<'section'> & { interactive?: boolean; dashed?: boolean }) {
  return (
    <section
      data-slot="panel"
      className={cn(
        'overflow-hidden rounded-xl border bg-card text-card-foreground',
        interactive && 'cursor-pointer transition-colors hover:border-foreground/30 focus-within:border-foreground/30',
        dashed && 'border-dashed',
        className,
      )}
      {...props}
    />
  )
}

export function PanelHeader({ icon: Icon, title, description, meta, actions, className }: { icon?: LucideIcon; title: ReactNode; description?: ReactNode; meta?: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <header className={cn('flex items-center gap-2.5 border-b px-5 py-3.5', className)}>
      {Icon && <Icon className="size-4 shrink-0" aria-hidden />}
      <div className="min-w-0 flex-1">
        <h2 className="text-sm font-semibold">{title}</h2>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
      {meta && <span className="text-xs text-muted-foreground">{meta}</span>}
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </header>
  )
}

export function PanelBody({ className, ...props }: ComponentProps<'div'>) {
  return <div className={cn('flex flex-col gap-3 p-5', className)} {...props} />
}

export function PanelFooter({ className, ...props }: ComponentProps<'footer'>) {
  return <footer className={cn('flex flex-wrap items-center justify-between gap-3 border-t bg-muted/50 px-5 py-3 text-[13px] text-muted-foreground', className)} {...props} />
}

export function PanelList({ className, ...props }: ComponentProps<'ul'>) {
  return <ul role="list" className={cn('divide-y', className)} {...props} />
}

/** A `.list .it` row. Pass `onClick`/`href` via `render` children for clickable rows. */
export function PanelRow({ className, selected, interactive, ...props }: ComponentProps<'li'> & { selected?: boolean; interactive?: boolean }) {
  return (
    <li
      data-selected={selected || undefined}
      className={cn(
        'flex items-center gap-3.5 px-5 py-3.5',
        interactive && 'cursor-pointer hover:bg-muted/50 has-[a:focus-visible]:bg-muted/50',
        selected && 'bg-muted',
        className,
      )}
      {...props}
    />
  )
}
