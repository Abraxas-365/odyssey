import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Bot } from 'lucide-react'
import { ButtonLink, Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { AttentionItem, AttentionList, Callout, ErrorNotice } from '@/components/odyssey'
import { formatRelative } from '@/lib/format'
import { useAttention } from '@/lib/inventory'
import type { Attention, AttentionItem as Item } from '@/lib/types'
import { fixPrompt } from '@/features/apps/overview'
import { AssistantDialog } from '@/features/apps/publish-paths'
import { kindView, severityTone, unavailableText } from './inventory'

/** How many items show before "Ver N más": Inicio stays a glance, not a report. */
export const VISIBLE = 6

/**
 * "All in order": nothing to show and every source answered. A source that
 * could not be read is never "in order" (UX v3 §3).
 */
export const allInOrder = (a: Attention | undefined) => !!a && a.items.length === 0 && a.unavailable.length === 0

/**
 * "Necesita tu atención" (UX v4 §5.2): it appears only when there are real
 * signals, each with its action; a source that failed is said, never hidden.
 * Nothing to say renders nothing (Inicio's greeting says "todo en orden").
 */
export function AttentionSection() {
  const attention = useAttention()
  const [all, setAll] = useState(false)
  const [fix, setFix] = useState<string>()

  if (attention.error && !attention.data) return <ErrorNotice error={attention.error} onRetry={() => attention.refetch()} />
  const data = attention.data
  if (!data) return null

  const items = data.items
  const shown = all ? items : items.slice(0, VISIBLE)
  const incomplete = data.unavailable.length > 0
  return (
    <>
      {incomplete && (
        <Callout tone="warn" title={items.length === 0 ? 'No pudimos revisar todo.' : 'La lista puede estar incompleta.'}>
          {unavailableText(data.unavailable)}
        </Callout>
      )}
      <AttentionList count={items.length}>
        {shown.map(i => <Row key={`${i.kind}:${i.request_id ?? i.app_id ?? i.cluster_id ?? ''}:${i.environment_id ?? ''}:${i.detail ?? ''}`} item={i} onFix={setFix} />)}
        {items.length > VISIBLE && (
          <li className="px-5 py-3">
            <Button variant="ghost" size="sm" onClick={() => setAll(v => !v)}>{all ? 'Ver menos' : `Ver ${items.length - VISIBLE} más`}</Button>
          </li>
        )}
      </AttentionList>
      {data.truncated && <p className="text-[13px] text-muted-foreground">Hay más de lo que cabe aquí: resuelve estos y vuelve a mirar.</p>}
      <AssistantDialog open={!!fix} onOpenChange={o => !o && setFix(undefined)} prompt={fix ? fixPrompt(fix) : ''} />
    </>
  )
}

/** Your own version that did not go online: your assistant can fix it (UX v4 §5.2). */
const fixable = (i: Item) => i.kind === 'deployment_failed' && i.scope === 'mine' && !!i.app_slug

function Row({ item: i, onFix }: { item: Item; onFix: (slug: string) => void }) {
  const v = kindView[i.kind]
  return (
    <AttentionItem
      tone={severityTone[i.severity]}
      icon={v.icon}
      title={
        <span className="flex flex-wrap items-center gap-2">
          {i.sentence}
          {i.scope === 'it' && <Badge variant="outline">TI</Badge>}
        </span>
      }
      action={
        <>
          {fixable(i) && (
            <Button variant="outline" size="sm" onClick={() => onFix(i.app_slug!)}>
              <Bot />
              Pedir a mi asistente que lo arregle
            </Button>
          )}
          <ButtonLink variant="outline" size="sm" render={<Link to={i.link} />}>{v.action}</ButtonLink>
        </>
      }
    >
      {i.at ? formatRelative(i.at) : undefined}
    </AttentionItem>
  )
}
