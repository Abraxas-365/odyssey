import type { LucideIcon } from 'lucide-react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'
import type { Tone } from './tone'

// Mockup `.ico`: a round, outlined icon whose tone carries the meaning.
const iconBadge = cva('grid shrink-0 place-items-center rounded-full border bg-background', {
  variants: {
    tone: {
      neutral: 'border-border text-foreground',
      ok: 'border-success/40 text-success',
      warn: 'border-warning/45 text-warning',
      bad: 'border-destructive/45 text-destructive',
      info: 'border-info/45 text-info',
      ai: 'border-ai/45 text-ai',
    } satisfies Record<Tone, string>,
    size: {
      sm: 'size-7 [&_svg]:size-3.5',
      md: 'size-[34px] [&_svg]:size-4',
      lg: 'size-11 [&_svg]:size-5',
    },
  },
  defaultVariants: { tone: 'neutral', size: 'md' },
})

export function IconBadge({ icon: Icon, tone, size, className, label }: { icon: LucideIcon; className?: string; label?: string } & VariantProps<typeof iconBadge>) {
  return (
    <span className={cn(iconBadge({ tone, size }), className)} role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : true}>
      <Icon />
    </span>
  )
}

/** Numbered step marker (`.num`), green with a check when done. */
export function StepNumber({ n, done, className }: { n: number; done?: boolean; className?: string }) {
  return (
    <span
      className={cn(
        'grid size-[26px] shrink-0 place-items-center rounded-full border font-mono text-xs font-semibold',
        done && 'border-success bg-success text-success-foreground',
        className,
      )}
    >
      {done ? '✓' : n}
    </span>
  )
}
