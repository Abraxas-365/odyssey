import type { ReactNode } from 'react'
import { Users } from 'lucide-react'
import { cn } from '@/lib/utils'
import { GitLine } from './git-line'
import { AppStatus, type AppStatusKind } from './status'

/**
 * Abstract app preview (`.thumb`): a schematic dashboard or form, never a real
 * screenshot, so cards render instantly and leak no data.
 */
export function AppThumbnail({ kind = 'chart', off, mini, className }: { kind?: 'chart' | 'form'; off?: boolean; mini?: boolean; className?: string }) {
  return (
    <div
      aria-hidden
      className={cn(
        'flex aspect-[16/10] flex-col gap-1.5 overflow-hidden rounded-md border bg-inset p-2.5',
        off && 'opacity-45 grayscale',
        mini && 'w-24 shrink-0 gap-[3px] p-1.5',
        className,
      )}
    >
      <div className="h-2 w-2/5 rounded-[3px] bg-muted" />
      {kind === 'chart' ? (
        <>
          <div className="flex gap-1.5">
            {[0, 1, 2].map(i => <i key={i} className={cn('flex-1 rounded bg-muted', mini ? 'h-2' : 'h-[18px]')} />)}
          </div>
          <div className="flex flex-1 items-end gap-1">
            {[40, 65, 50, 80, 70, 95, 60, 85].map((h, i) => <i key={i} className="flex-1 rounded-t-[2px] bg-primary/55" style={{ height: `${h}%` }} />)}
          </div>
        </>
      ) : (
        <div className="flex flex-col gap-1.5">
          {[0, 1, 2].map(i => <i key={i} className={cn('rounded-[3px] bg-muted', mini ? 'h-1.5' : 'h-2.5')} />)}
          <i className={cn('w-1/3 rounded-[3px] bg-primary/55', mini ? 'h-2' : 'h-4')} />
        </div>
      )}
    </div>
  )
}

export interface AppCardData {
  name: string
  description?: string
  /** Absent while it is not known: the card then claims no state (a placeholder while `loading`). */
  status?: AppStatusKind
  loading?: boolean
  /** Overrides the status word, e.g. "Sin publicar" before the first version. */
  statusLabel?: string
  updated: string
  /** Where it answers, e.g. "visitas.apps.empresa.com"; only when a version is online. */
  host?: string
  /** Who can open it, only when the API said so: never a default. */
  audience?: string
  people?: number
  thumbnail?: 'chart' | 'form'
  git?: { sha: string; message: string; branch?: string }
}

/** Home/apps grid card. The whole card is the link (pass `href` + `linkAs`). */
export function AppCard({ app, technical, resources, href, linkAs: Link = 'a' }: { app: AppCardData; technical?: boolean; resources?: ReactNode; href?: string; linkAs?: React.ElementType }) {
  const footer = app.audience !== undefined || resources !== undefined
  return (
    <article className="group relative overflow-hidden rounded-xl border bg-card transition-colors hover:border-foreground/30 has-[a:focus-visible]:border-foreground/30">
      <div className="px-3.5 pt-3.5">
        <AppThumbnail kind={app.thumbnail} off={!app.status || app.status === 'paused' || app.status === 'archived'} />
      </div>
      <div className="flex flex-col gap-2 px-4 pt-3.5 pb-4">
        <div>
          <h3 className="font-semibold">
            {href ? (
              <Link href={href} to={href} className="outline-none after:absolute after:inset-0">
                {app.name}
              </Link>
            ) : (
              app.name
            )}
          </h3>
          {app.description && <p className="truncate text-xs text-muted-foreground">{app.description}</p>}
          {app.host && <p className="truncate font-mono text-xs text-muted-foreground">{app.host}</p>}
        </div>
        <div className="flex items-center gap-2">
          {app.status ? (
            <AppStatus status={app.status} label={app.statusLabel} />
          ) : app.loading ? (
            <span aria-hidden className="h-4 w-20 animate-pulse rounded bg-muted" />
          ) : null}
          <span className="flex-1" />
          <span className="text-xs text-muted-foreground">{app.updated}</span>
        </div>
        {technical && app.git && <GitLine branch="main" {...app.git} />}
        {footer && (
          <div className="mt-0.5 flex items-center gap-2 border-t pt-2.5 text-xs text-muted-foreground">
            {app.audience !== undefined && (
              <>
                <Users className="size-[13px]" aria-label="Quién puede abrirla" />
                <span className="flex-1 truncate">
                  {app.audience}
                  {app.people !== undefined && ` · ${app.people} ${app.people === 1 ? 'persona' : 'personas'}`}
                </span>
              </>
            )}
            {app.audience === undefined && <span className="flex-1" />}
            {resources}
          </div>
        )}
      </div>
    </article>
  )
}

/** Dashed "Crear una app" tile that closes the grid. */
export function CreateAppTile({ onClick, title = 'Crear una app', description = 'Descríbela y tu asistente de IA la construye' }: { onClick?: () => void; title?: string; description?: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex min-h-[260px] cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border border-dashed bg-card p-5 text-center transition-colors outline-none hover:border-foreground/30 focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <span className="grid size-[34px] place-items-center rounded-full border bg-background text-lg">+</span>
      <span className="font-semibold">{title}</span>
      <span className="text-xs text-muted-foreground">{description}</span>
    </button>
  )
}
