import { Fragment, type ReactNode } from 'react'
import { CheckCircle2, Circle, Loader, X, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { IconBadge } from './icon-badge'

/**
 * Shipment-style tracker for requests (UX v3 §3):
 * Enviada → En revisión · Carlos (TI) → Lista. `current` is the index of the
 * step in progress; `current === steps.length` means finished.
 */
export function RequestTracker({ steps, current, rejected, className }: { steps: string[]; current: number; rejected?: boolean; className?: string }) {
  return (
    <ol aria-label="Estado de la solicitud" className={cn('mt-2.5 flex flex-wrap items-center gap-y-1.5', className)}>
      {steps.map((label, i) => {
        const state = i < current ? 'done' : i === current ? (rejected ? 'rejected' : 'current') : 'pending'
        const Icon = state === 'done' ? CheckCircle2 : state === 'current' ? Loader : state === 'rejected' ? X : Circle
        return (
          <Fragment key={label}>
            {i > 0 && <span aria-hidden className={cn('mx-2 h-px w-9', i <= current ? 'bg-success' : 'bg-border')} />}
            <li
              aria-current={state === 'current' ? 'step' : undefined}
              className={cn(
                'flex items-center gap-1.5 text-xs whitespace-nowrap text-muted-foreground',
                state === 'done' && 'text-success',
                state === 'current' && 'font-medium text-foreground',
                state === 'rejected' && 'font-medium text-destructive',
              )}
            >
              <Icon className={cn('size-3.5', state === 'current' && 'animate-spin [animation-duration:2.4s]')} aria-hidden />
              {label}
            </li>
          </Fragment>
        )
      })}
    </ol>
  )
}

export const requestSteps = (reviewer: string) => ['Enviada', `En revisión · ${reviewer}`, 'Lista']

export type ProgressState = 'done' | 'running' | 'pending' | 'failed'

export interface ProgressStepItem {
  icon: LucideIcon
  title: string
  description: ReactNode
  /** Shown only in technical mode, e.g. "build: Railpack + BuildKit". */
  technical?: ReactNode
  state: ProgressState
}

/**
 * Publishing / provisioning as progress (UX v3 §7): Preparando tu app →
 * Revisando seguridad → Publicando. Internally build → verify → rollout.
 */
export function ProgressSteps({ steps, technical }: { steps: ProgressStepItem[]; technical?: boolean }) {
  return (
    <ol className="divide-y" aria-label="Progreso">
      {steps.map(s => (
        <li key={s.title} aria-current={s.state === 'running' ? 'step' : undefined} className={cn('flex items-center gap-3.5 px-5 py-4', s.state === 'pending' && 'opacity-45')}>
          <IconBadge
            icon={s.state === 'done' ? CheckCircle2 : s.state === 'failed' ? X : s.state === 'running' ? Loader : s.icon}
            tone={s.state === 'done' ? 'ok' : s.state === 'failed' ? 'bad' : s.state === 'running' ? 'info' : 'neutral'}
            className={s.state === 'running' ? '[&_svg]:animate-spin [&_svg]:[animation-duration:2.4s]' : undefined}
          />
          <div className="min-w-0 flex-1">
            <div className="font-semibold">{s.title}</div>
            <div className="text-[13px] text-muted-foreground">
              {s.description}
              {technical && s.technical && <span className="font-mono text-xs"> · {s.technical}</span>}
            </div>
          </div>
          <span className="text-xs text-muted-foreground">{{ done: 'Listo', running: 'En curso', pending: 'En espera', failed: 'Falló' }[s.state]}</span>
        </li>
      ))}
    </ol>
  )
}
