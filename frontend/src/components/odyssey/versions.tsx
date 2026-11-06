import { Check, ChevronRight, Clock, FlaskConical, RotateCcw, X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { ChannelChip, type ChannelData } from './channel'
import { GitLine } from './git-line'
import { IconBadge } from './icon-badge'

/**
 * A version ("Versión", never "deployment"). `live` = currently published,
 * `preview` = Versión de prueba, `failed` = No se publicó, `previous` = an
 * older published version you can restore, `waiting` = IT has to approve it
 * before it starts.
 */
export type VersionKind = 'live' | 'previous' | 'preview' | 'failed' | 'waiting'

export interface VersionData {
  id: string
  message: string
  when: string
  /** Who asked: "ti", a name, "tu equipo", "la plataforma". */
  by: string
  /** Through what (from the API's `requested_via`); absent in samples that do not say. */
  channel?: ChannelData
  kind: VersionKind
  sha?: string
  branch?: string
  /** Technical mode: the uploaded folder and its size. */
  source?: string
  /** Published from a GitHub commit: `message` is its title. */
  git?: { branch: string; sha: string; author: string }
}

const icon = { live: Check, previous: Check, preview: FlaskConical, failed: X, waiting: Clock }
const tone = { live: 'ok', previous: 'ok', preview: 'neutral', failed: 'bad', waiting: 'info' } as const

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
          {v.kind === 'waiting' && <Badge variant="info">Esperando aprobación de TI</Badge>}
        </div>
        {v.git ? (
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <span>{v.when}</span>
            {v.channel && <span>· por {v.by}</span>}
            {v.channel && <ChannelChip channel={v.channel} />}
            <GitLine branch={technical ? v.git.branch : undefined} sha={technical ? v.git.sha : undefined} author={v.git.author} className="w-auto" />
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <span>
              {v.when} · por {v.by}
            </span>
            {v.channel && <ChannelChip channel={v.channel} />}
            {technical && v.sha && (
              <span>
                <span className="font-mono">{v.sha.slice(0, 7)}</span>
                {v.branch && <> · <span className="font-mono">{v.branch}</span></>}
              </span>
            )}
            {technical && v.source && <span>{v.source}</span>}
          </div>
        )}
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
