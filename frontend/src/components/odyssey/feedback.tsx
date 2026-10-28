import type { ReactNode } from 'react'
import { AlertCircle, type LucideIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/api'
import { cn } from '@/lib/utils'
import { IconBadge } from './icon-badge'

/** Empty state: what this is, why it's empty, and the one next step. */
/** `level`: when the empty state is the page itself (404), its title is the page heading. */
export function EmptyState({ icon, title, children, action, level, className }: { icon: LucideIcon; title: ReactNode; children?: ReactNode; action?: ReactNode; level?: 1 | 2; className?: string }) {
  return (
    <Empty className={cn('rounded-xl border border-dashed bg-card', className)}>
      <EmptyHeader>
        <EmptyMedia>
          <IconBadge icon={icon} size="lg" />
        </EmptyMedia>
        <EmptyTitle {...(level && { role: 'heading', 'aria-level': level })}>{title}</EmptyTitle>
        {children && <EmptyDescription>{children}</EmptyDescription>}
      </EmptyHeader>
      {action && <EmptyContent>{action}</EmptyContent>}
    </Empty>
  )
}

export function Loading({ label = 'Cargando…', className }: { label?: string; className?: string }) {
  return (
    <div role="status" className={cn('flex items-center gap-2 py-8 text-sm text-muted-foreground', className)}>
      <Spinner />
      {label}
    </div>
  )
}

/** Placeholder rows matching PanelRow height while a list loads. */
export function ListSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div aria-hidden className="divide-y">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex items-center gap-3.5 px-5 py-3.5">
          <Skeleton className="size-[34px] rounded-full" />
          <div className="flex flex-1 flex-col gap-1.5">
            <Skeleton className="h-3.5 w-1/3" />
            <Skeleton className="h-3 w-1/2" />
          </div>
        </div>
      ))}
    </div>
  )
}

/** API/transport error in plain Spanish (errorMessage), with an optional retry. */
export function ErrorNotice({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  if (!error) return null
  return (
    <div role="alert" className={cn('flex items-start gap-2.5 rounded-lg border border-destructive/35 bg-destructive/7 px-4 py-3 text-[13px] text-destructive', className)}>
      <AlertCircle className="mt-px size-4 shrink-0" aria-hidden />
      <span className="flex-1">{errorMessage(error)}</span>
      {onRetry && (
        <Button variant="outline" size="xs" onClick={onRetry}>
          Reintentar
        </Button>
      )}
    </div>
  )
}
