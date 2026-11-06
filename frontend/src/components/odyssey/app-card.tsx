import type { ReactNode } from 'react'
import { Users } from 'lucide-react'
import { cn } from '@/lib/utils'
import { ChannelChip, type ChannelData } from './channel'
import { AppUrl } from './code'
import { GitLine } from './git-line'
import { AppStatus, type AppStatusKind } from './status'

/**
 * One app as Inicio says it (UX v4 §5.2). Every field is what the API read:
 * an unknown value is absent, never a default presented as a fact.
 */
export interface AppRowData {
  name: string
  /** Absent while it is not known: it then claims no state (a placeholder while `loading`). */
  status?: AppStatusKind
  /** Overrides the status word, e.g. "Sin publicar" before the first version. */
  statusLabel?: string
  loading?: boolean
  /** The whole address, copied and opened; only when a version answers there. */
  url?: string
  /** "Ana publicó hace 2 horas", from the API's last change (or when it last changed). */
  change: string
  /** Through what, from the API's `requested_via` only. */
  channel?: ChannelData
  /** Who can open it, only when the API said so: never a default. */
  audience?: string
  /** Technical mode: the slug. */
  detail?: string
}

/**
 * Inicio's apps (UX v4 §5.2, §5.10): a semantic list with one main link per
 * row (the name, stretched over the row) and the address's copy and open
 * above it.
 */
export function AppList({ children, label = 'Apps', className }: { children: ReactNode; label?: string; className?: string }) {
  return (
    <ul role="list" aria-label={label} className={cn('divide-y rounded-xl border bg-card', className)}>
      {children}
    </ul>
  )
}

/**
 * Two lines a person reads, not columns: name · state · address, then who
 * changed it last and through what · who can open it. Under 640 px the two
 * lines stay and the address becomes its copy and open buttons (44 px).
 */
export function AppRow({ app, href, linkAs: Link = 'a' }: { app: AppRowData; href: string; linkAs?: React.ElementType }) {
  return (
    <li className="relative flex min-h-14 flex-col justify-center gap-1 px-4 py-2.5 transition-colors first:rounded-t-xl last:rounded-b-xl hover:bg-accent/40 has-[a[data-row-link]:focus-visible]:ring-3 has-[a[data-row-link]:focus-visible]:ring-ring/50">
      <div className="flex min-w-0 items-center gap-x-3">
        <h3 className="min-w-0 truncate font-semibold">
          <Link data-row-link href={href} to={href} className="outline-none after:absolute after:inset-0">
            {app.name}
          </Link>
        </h3>
        <Status app={app} />
        <span className="flex-1" />
        {app.url && <AppUrl url={app.url} compact className="relative z-10 text-muted-foreground" />}
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-muted-foreground">
        {app.detail && <span className="font-mono text-xs">{app.detail}</span>}
        <span className="min-w-0 truncate">{app.change}</span>
        {app.channel && <ChannelChip channel={app.channel} />}
        {app.audience !== undefined && <Audience audience={app.audience} className="sm:ml-auto" />}
      </div>
    </li>
  )
}

export interface AppCardData extends AppRowData {
  description?: string
  people?: number
  git?: { sha: string; message: string; branch?: string }
}

/**
 * Inicio's optional grid: the same facts as a row, in a card. No preview
 * picture: a drawing that is not the app is not shown (UX v4 §5.2).
 */
export function AppCard({ app, technical, resources, href, linkAs: Link = 'a' }: { app: AppCardData; technical?: boolean; resources?: ReactNode; href?: string; linkAs?: React.ElementType }) {
  const footer = app.audience !== undefined || resources !== undefined
  return (
    <article className="group relative flex flex-col gap-2 rounded-xl border bg-card px-4 py-3.5 transition-colors hover:border-foreground/30 has-[a[data-row-link]:focus-visible]:border-foreground/30">
      <div className="flex min-w-0 items-center gap-2">
        <h3 className="min-w-0 flex-1 truncate font-semibold">
          {href ? (
            <Link data-row-link href={href} to={href} className="outline-none after:absolute after:inset-0">
              {app.name}
            </Link>
          ) : (
            app.name
          )}
        </h3>
        <Status app={app} />
      </div>
      {app.description && <p className="-mt-1 truncate text-xs text-muted-foreground">{app.description}</p>}
      {app.url && <AppUrl url={app.url} compact className="relative z-10 -ml-0.5 text-muted-foreground" />}
      <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <span className="min-w-0 truncate">{app.change}</span>
        {app.channel && <ChannelChip channel={app.channel} />}
      </div>
      {technical && app.git && <GitLine branch="main" {...app.git} />}
      {footer && (
        <div className="mt-auto flex items-center gap-2 border-t pt-2.5 text-xs text-muted-foreground">
          {app.audience !== undefined ? (
            <Audience audience={app.people !== undefined ? `${app.audience} · ${app.people} ${app.people === 1 ? 'persona' : 'personas'}` : app.audience} className="flex-1" />
          ) : (
            <span className="flex-1" />
          )}
          {resources}
        </div>
      )}
    </article>
  )
}

function Status({ app }: { app: Pick<AppRowData, 'status' | 'statusLabel' | 'loading'> }) {
  if (app.status) return <AppStatus status={app.status} label={app.statusLabel} className="shrink-0" />
  if (app.loading) return <span aria-hidden className="h-4 w-20 shrink-0 animate-pulse rounded bg-muted" />
  return null
}

function Audience({ audience, className }: { audience: string; className?: string }) {
  return (
    <span className={cn('inline-flex min-w-0 items-center gap-1.5', className)}>
      <Users className="size-[13px] shrink-0" aria-label="Quién puede abrirla" />
      <span className="truncate">{audience}</span>
    </span>
  )
}

/** Dashed "Crear una app" tile that closes the grid. */
export function CreateAppTile({ onClick, title = 'Crear una app', description = 'Descríbela y tu asistente de IA la construye' }: { onClick?: () => void; title?: string; description?: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex min-h-[140px] cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border border-dashed bg-card p-5 text-center transition-colors outline-none hover:border-foreground/30 focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <span className="grid size-[34px] place-items-center rounded-full border bg-background text-lg">+</span>
      <span className="font-semibold">{title}</span>
      <span className="text-xs text-muted-foreground">{description}</span>
    </button>
  )
}
