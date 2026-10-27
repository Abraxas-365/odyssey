import { Check, ChevronRight, FlaskConical, RotateCcw, X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { IconBadge } from './icon-badge'

/**
 * A version ("Versión", never "deployment"). `live` = currently published,
 * `preview` = Versión de prueba, `failed` = No se publicó, `previous` = an
 * older published version you can restore.
 */
export type VersionKind = 'live' | 'previous' | 'preview' | 'failed'

export interface VersionData {
  id: string
  message: string
  when: string
  by: string
  kind: VersionKind
  sha?: string
  branch?: string
}

const icon = { live: Check, previous: Check, preview: FlaskConical, failed: X }
const tone = { live: 'ok', previous: 'ok', preview: 'neutral', failed: 'bad' } as const

/**
 * One row of the Versiones list. The title is the link to the detail (or to
 * "Estos cambios no se publicaron" for failures); actions sit beside it, so
 * there is no nested interactive content.
 */
export function VersionRow({ version: v, technical, onOpen, onRestore, onPublish }: { version: VersionData; technical?: boolean; onOpen?: () => void; onRestore?: () => void; onPublish?: () => void }) {
  return (
    <li className="flex items-center gap-3.5 px-5 py-3.5">
      <IconBadge icon={icon[v.kind]} tone={tone[v.kind]} size="sm" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          {onOpen ? (
            <button type="button" onClick={onOpen} className="cursor-pointer text-left font-semibold underline-offset-4 outline-none hover:underline focus-visible:underline">
              “{v.message}”
            </button>
          ) : (
            <span className="font-semibold">“{v.message}”</span>
          )}
          {v.kind === 'live' && <Badge variant="success">Publicada ahora</Badge>}
          {v.kind === 'preview' && <Badge>Versión de prueba</Badge>}
          {v.kind === 'failed' && <Badge variant="destructive">No se publicó</Badge>}
        </div>
        <div className="text-xs text-muted-foreground">
          {v.when} · por {v.by}
          {technical && v.sha && (
            <>
              {' · '}
              <span className="font-mono">{v.sha.slice(0, 7)}</span>
              {v.branch && <> · <span className="font-mono">{v.branch}</span></>}
            </>
          )}
        </div>
      </div>
      {v.kind === 'preview' && onPublish && (
        <Button variant="outline" size="sm" onClick={onPublish}>
          Publicar
        </Button>
      )}
      {v.kind === 'previous' && onRestore && (
        <Button variant="outline" size="sm" onClick={onRestore}>
          <RotateCcw />
          Volver a esta
        </Button>
      )}
      {v.kind === 'failed' && onOpen && <ChevronRight className={cn('size-4 text-muted-foreground')} aria-hidden />}
    </li>
  )
}

/** Marks an action done by an AI assistant on someone's behalf. */
export function AssistantBy({ person, className }: { person: string; className?: string }) {
  return (
    <Badge variant="ai" className={className}>
      tu asistente · en nombre de {person}
    </Badge>
  )
}
