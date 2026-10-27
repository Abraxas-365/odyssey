import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/** h1 + muted lead + right-aligned actions (mockup `.head`). */
export function PageHeader({ title, description, status, actions, breadcrumb, className }: { title: ReactNode; description?: ReactNode; status?: ReactNode; actions?: ReactNode; breadcrumb?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex flex-wrap items-center justify-between gap-3', className)}>
      <div className="min-w-0">
        {breadcrumb && <div className="mb-1 text-xs text-muted-foreground">{breadcrumb}</div>}
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="font-heading text-title font-bold">{title}</h1>
          {status}
        </div>
        {description && <p className="mt-1 text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

/** Section title in the monospace heading face (mockup h2) with inline actions. */
export function SectionHeader({ title, actions, className }: { title: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex flex-wrap items-center gap-2', className)}>
      <h2 className="flex-1 font-heading text-section font-semibold">{title}</h2>
      {actions}
    </div>
  )
}

/** Centred welcome block for onboarding and full-page flows (mockup `.hero`). */
export function Hero({ title, children, className }: { title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex flex-col items-center gap-2 pt-4 pb-1 text-center', className)}>
      <h1 className="font-heading text-display font-bold">{title}</h1>
      {children && <p className="max-w-[640px] text-[15px] text-muted-foreground">{children}</p>}
    </div>
  )
}

/** Small uppercase-free field label used above read-only values (`.label`). */
export function Label({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('mb-1 text-xs text-muted-foreground', className)}>{children}</div>
}
