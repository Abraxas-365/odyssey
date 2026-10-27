import { Users } from 'lucide-react'
import { cn } from '@/lib/utils'

export function initials(name: string) {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  return ((parts[0]?.[0] ?? '?') + (parts.length > 1 ? parts[parts.length - 1][0] : '')).toUpperCase()
}

/** Team colours from the mockup; unknown teams fall back to a stable hash. */
const palette = ['var(--primary)', 'var(--success)', 'var(--warning)', 'var(--destructive)', 'var(--chart-5)']
export function colorFor(key: string) {
  let h = 0
  for (const c of key) h = (h * 31 + c.charCodeAt(0)) | 0
  return palette[Math.abs(h) % palette.length]
}

/**
 * Initials avatar (`.av`). `color` is a CSS colour (usually a token var);
 * `muted` is used for people that aren't the current subject; `group` shows
 * the users icon for IAM groups.
 */
export function Avatar({ name, color, muted, group, size = 'md', className }: { name: string; color?: string; muted?: boolean; group?: boolean; size?: 'md' | 'lg'; className?: string }) {
  return (
    <span
      role="img"
      aria-label={name}
      className={cn(
        'grid shrink-0 place-items-center rounded-full font-mono font-semibold text-primary-foreground',
        size === 'md' ? 'size-[22px] text-[10px]' : 'size-8 text-[11px]',
        (muted || group) && 'bg-muted text-muted-foreground',
        className,
      )}
      style={muted || group ? undefined : { background: color ?? colorFor(name) }}
    >
      {group ? <Users className="size-3.5" aria-hidden /> : initials(name)}
    </span>
  )
}

/** Person or group with a role line: used in Compartir, Equipo, Bandeja. */
export function PersonRow({ name, detail, group, color, trailing, className }: { name: string; detail?: string; group?: boolean; color?: string; trailing?: React.ReactNode; className?: string }) {
  return (
    <div className={cn('flex items-center gap-2.5 py-2.5', className)}>
      <Avatar name={name} group={group} color={color} muted={!color} size="lg" />
      <div className="min-w-0 flex-1">
        <div className="truncate">{name}</div>
        {detail && <div className="truncate text-xs text-muted-foreground">{detail}</div>}
      </div>
      {trailing}
    </div>
  )
}
