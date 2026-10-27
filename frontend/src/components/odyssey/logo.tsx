import { cn } from '@/lib/utils'

/** The Odicea mark from the mockup: rounded square with a hexagonal ring. */
export function Logo({ className, title = 'Odyssey' }: { className?: string; title?: string }) {
  return (
    <svg viewBox="0 0 64 64" role="img" aria-label={title} className={cn('size-[26px] text-foreground', className)}>
      <path
        fill="currentColor"
        fillRule="evenodd"
        d="M14 0H50A14 14 0 0 1 64 14V50A14 14 0 0 1 50 64H14A14 14 0 0 1 0 50V14A14 14 0 0 1 14 0ZM32 14L50 24V40L32 50L14 40V24ZM32 22L21 28V36L32 42L43 36V28Z"
      />
    </svg>
  )
}

export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={cn('flex items-center gap-2.5', className)}>
      <Logo />
      <span className="font-heading text-lg font-bold tracking-tight">Odyssey</span>
    </span>
  )
}
