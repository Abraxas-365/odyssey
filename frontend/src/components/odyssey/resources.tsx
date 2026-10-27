import type { ReactNode } from 'react'
import { AppWindow, Bot, ChevronRight, Database, Folder, KeyRound, ListChecks, Sparkles, Table2, Zap, type LucideIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import { IconBadge } from './icon-badge'
import { UsageMeter } from './stat'

/**
 * What an app can use ("Almacenamiento" + services). Builders see `label`;
 * technical mode adds `technical` (engine · env var). Mirrors the mockup KIND.
 */
export const resourceKinds = {
  app: { icon: AppWindow, label: 'La app', technical: 'Deployment · HPA' },
  postgres: { icon: Database, label: 'Base de datos', technical: 'Postgres 17 · DATABASE_URL' },
  redis: { icon: Zap, label: 'Caché rápida', technical: 'Redis 7 · REDIS_URL' },
  files: { icon: Folder, label: 'Archivos', technical: 'Bucket S3 · S3_BUCKET' },
  queue: { icon: ListChecks, label: 'Tareas en segundo plano', technical: 'River · QUEUE_URL' },
  vector: { icon: Sparkles, label: 'Búsqueda con IA', technical: 'pgvector · VECTOR_DATABASE_URL' },
  ai: { icon: Bot, label: 'IA', technical: 'freerouter · OPENAI_BASE_URL' },
  erp: { icon: Table2, label: 'Datos de la empresa', technical: 'réplica de solo lectura' },
  key: { icon: KeyRound, label: 'Servicio externo', technical: 'secreto' },
} as const satisfies Record<string, { icon: LucideIcon; label: string; technical: string }>

export type ResourceKind = keyof typeof resourceKinds
export type ResourceFlag = 'ok' | 'warn' | 'waiting'

export interface ResourceUse {
  kind: ResourceKind
  /** Plain usage line: "340 MB de 10 GB", "2 copias en línea…". */
  detail: ReactNode
  /** 0–100; ≥ 85 turns the row to warning. */
  usage?: number
  /** Monthly cost in USD; 0/undefined renders "incluido". */
  cost?: number
  flag?: ResourceFlag
  technical?: ReactNode
}

export const money = (n?: number) => (n ? `$${n.toFixed(2)}` : '—')

function needsCare(r: ResourceUse) {
  return r.flag === 'warn' || (r.usage ?? 0) >= 85
}

/** One "Lo que usa esta app" row (mockup resRow). */
export function ResourceRow({ resource: r, technical, onOpen }: { resource: ResourceUse; technical?: boolean; onOpen?: () => void }) {
  const kind = resourceKinds[r.kind]
  const content = (
    <>
      <IconBadge icon={kind.icon} size="sm" tone={needsCare(r) ? 'warn' : r.flag === 'waiting' ? 'info' : 'ok'} />
      <div className="min-w-0 flex-1 text-left">
        <div className="font-semibold">{kind.label}</div>
        <div className="text-xs text-muted-foreground">
          {r.detail}
          {technical && <span className="font-mono"> · {r.technical ?? kind.technical}</span>}
        </div>
      </div>
      {r.usage ? <UsageMeter value={r.usage} label={`${kind.label}: ${Math.round(r.usage)}% usado`} /> : r.flag === 'waiting' ? <Badge>En revisión</Badge> : r.flag === 'warn' ? <Badge variant="warning">Falta algo</Badge> : null}
      <span className="w-[76px] text-right text-xs text-muted-foreground">{r.cost ? `${money(r.cost)} / mes` : 'incluido'}</span>
      {onOpen ? <ChevronRight className="size-4 text-muted-foreground" aria-hidden /> : <span className="w-4" />}
    </>
  )
  return (
    <li>
      {onOpen ? (
        <button type="button" onClick={onOpen} className="flex w-full cursor-pointer items-center gap-3.5 px-5 py-3.5 outline-none hover:bg-muted/50 focus-visible:bg-muted/50">
          {content}
        </button>
      ) : (
        <div className="flex items-center gap-3.5 px-5 py-3.5">{content}</div>
      )}
    </li>
  )
}

/** Compact icon strip for app cards and the "Qué usa cada app" table. */
export function ResourceIcons({ resources, className }: { resources: ResourceUse[]; className?: string }) {
  return (
    <span className={cn('flex items-center gap-2', className)}>
      {resources
        .filter(r => r.kind !== 'app')
        .map((r, i) => {
          const { icon: Icon, label } = resourceKinds[r.kind]
          return (
            <Tooltip key={`${r.kind}-${i}`}>
              <TooltipTrigger render={<span className={cn('flex', needsCare(r) && 'text-warning')} />} aria-label={label}>
                <Icon className="size-3.5" aria-hidden />
              </TooltipTrigger>
              <TooltipContent>{label}</TooltipContent>
            </Tooltip>
          )
        })}
    </span>
  )
}

/** Picker card for "Agregar" (base de datos, caché, archivos…). */
export function ResourceOption({ kind, description, selected, onSelect, technical }: { kind: ResourceKind; description: ReactNode; selected?: boolean; onSelect?: () => void; technical?: boolean }) {
  const k = resourceKinds[kind]
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={cn('flex w-full cursor-pointer items-start gap-3 rounded-lg border bg-card px-4 py-3.5 text-left outline-none transition-colors hover:border-foreground/30 focus-visible:ring-3 focus-visible:ring-ring/50', selected && 'border-foreground')}
    >
      <IconBadge icon={k.icon} tone="ok" />
      <span className="min-w-0 flex-1">
        <span className="block font-semibold">{k.label}</span>
        <span className="block text-[13px] text-muted-foreground">{description}</span>
        {technical && <span className="mt-1 block font-mono text-xs text-muted-foreground">{k.technical}</span>}
      </span>
    </button>
  )
}
