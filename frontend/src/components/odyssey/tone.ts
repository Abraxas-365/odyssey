import { cva } from 'class-variance-authority'

/**
 * The semantic tones of the Odicea mockup (`.ok/.warn/.bad/.info/.ai`).
 * Every status-bearing component takes a `tone`, never a raw colour, so the
 * meaning stays consistent: ok = working/approved, warn = needs something,
 * bad = failed/blocked, info = in progress/waiting on someone, ai = done by
 * or for an AI assistant, neutral = no state.
 */
export type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'ai' | 'neutral'

export const toneText: Record<Tone, string> = {
  ok: 'text-success',
  warn: 'text-warning',
  bad: 'text-destructive',
  info: 'text-info',
  ai: 'text-ai',
  neutral: 'text-muted-foreground',
}

export const toneBadge = {
  ok: 'success',
  warn: 'warning',
  bad: 'destructive',
  info: 'info',
  ai: 'ai',
  neutral: 'outline',
} as const satisfies Record<Tone, string>

/** Tinted surface (callouts): 35% border, 7% fill. */
export const toneSurface = cva('', {
  variants: {
    tone: {
      ok: 'border-success/35 bg-success/7',
      warn: 'border-warning/35 bg-warning/7',
      bad: 'border-destructive/35 bg-destructive/7',
      info: 'border-info/35 bg-info/7',
      ai: 'border-ai/35 bg-ai/7',
      neutral: 'border-border bg-muted/50',
    },
  },
  defaultVariants: { tone: 'neutral' },
})
