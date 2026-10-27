import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Status vocabulary (UX v3 §1). One word set for everyone: the technical mode
 * adds detail around it but never renames a state.
 */
export const appStatuses = {
  live: { label: 'En línea', dot: 'bg-success' },
  building: { label: 'Publicando…', dot: 'bg-warning animate-pulse-dot' },
  attention: { label: 'Necesita atención', dot: 'bg-destructive' },
  paused: { label: 'En pausa', dot: 'bg-muted-foreground' },
  waiting: { label: 'Esperando aprobación', dot: 'bg-ai' },
  archived: { label: 'Archivada', dot: 'bg-muted-foreground/50' },
} as const

export type AppStatusKind = keyof typeof appStatuses

export function StatusDot({ status, className, ...props }: { status: AppStatusKind } & ComponentProps<'span'>) {
  return <span aria-hidden className={cn('inline-block size-[9px] shrink-0 rounded-full', appStatuses[status].dot, className)} {...props} />
}

/** Dot + plain-language label, e.g. "● En línea". */
export function AppStatus({ status, label, className }: { status: AppStatusKind; label?: string; className?: string }) {
  return (
    <span className={cn('inline-flex items-center gap-1.5 text-[13px] whitespace-nowrap', className)}>
      <StatusDot status={status} />
      {label ?? appStatuses[status].label}
    </span>
  )
}
