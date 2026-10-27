import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/** Big monospace number + caption (`.stat`), optionally with a budget bar. */
export function Stat({ value, label, progress, className }: { value: ReactNode; label: ReactNode; progress?: number; className?: string }) {
  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <div>
        <div className="font-mono text-stat font-semibold tabular-nums">{value}</div>
        <div className="text-xs text-muted-foreground">{label}</div>
      </div>
      {progress !== undefined && <UsageMeter value={progress} className="w-full" />}
    </div>
  )
}

/**
 * Usage bar (`.meter`): green, turns warning at ≥ 85% — the point where the
 * UI offers "Ampliar". Always labelled for screen readers.
 */
export function UsageMeter({ value, label, className }: { value: number; label?: string; className?: string }) {
  const pct = Math.max(0, Math.min(100, Math.round(value)))
  return (
    <span
      role="meter"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct}
      aria-label={label ?? `${pct}% usado`}
      title={`${pct}% usado`}
      className={cn('inline-block h-1.5 w-[90px] shrink-0 overflow-hidden rounded-full bg-muted', className)}
    >
      <span className={cn('block h-full rounded-full', pct >= 85 ? 'bg-warning' : 'bg-success')} style={{ width: `${Math.max(pct, 2)}%` }} />
    </span>
  )
}

/** Definition list (`.kv`): muted terms, values aligned in a second column. */
export function KeyValue({ items, className }: { items: [ReactNode, ReactNode][]; className?: string }) {
  return (
    <dl className={cn('m-0 grid grid-cols-[150px_1fr] gap-x-4 gap-y-2.5 text-[13px]', className)}>
      {items.map(([k, v], i) => (
        <div key={i} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="m-0 min-w-0">{v}</dd>
        </div>
      ))}
    </dl>
  )
}
