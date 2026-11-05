import { useState } from 'react'
import { Link } from 'react-router-dom'
import { CheckCircle2 } from 'lucide-react'
import { ButtonLink, Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { AttentionItem, AttentionList, Callout, ErrorNotice, ListSkeleton, Panel, PanelHeader } from '@/components/odyssey'
import { formatRelative } from '@/lib/format'
import { useAttention } from '@/lib/inventory'
import type { AttentionItem as Item } from '@/lib/types'
import { kindView, severityTone, unavailableText } from './inventory'

/** How many items show before "Ver N más": Inicio stays a glance, not a report. */
export const VISIBLE = 6

/**
 * "Necesita tu atención" (UX v3 §3): the caller's own things and, for IT, what
 * IT watches over, each with one action. Empty is "Todo en orden", but only
 * when every source answered: a source that failed is said, never hidden.
 */
export function AttentionSection() {
  const attention = useAttention()
  const [all, setAll] = useState(false)

  if (attention.isPending) return <Panel aria-busy aria-label="Necesita tu atención"><PanelHeader title="Necesita tu atención" /><ListSkeleton rows={2} /></Panel>
  if (attention.error && !attention.data) return <ErrorNotice error={attention.error} onRetry={() => attention.refetch()} />
  const data = attention.data
  if (!data) return null

  const items = data.items
  const shown = all ? items : items.slice(0, VISIBLE)
  const incomplete = data.unavailable.length > 0
  return (
    <>
      {items.length === 0 && !incomplete && (
        <Panel aria-label="Necesita tu atención">
          <PanelHeader icon={CheckCircle2} title="Todo en orden" description="Nada necesita tu atención ahora." />
        </Panel>
      )}
      {incomplete && (
        <Callout tone="warn" title={items.length === 0 ? 'No pudimos revisar todo.' : 'La lista puede estar incompleta.'}>
          {unavailableText(data.unavailable)}
        </Callout>
      )}
      <AttentionList count={items.length}>
        {shown.map(i => <Row key={`${i.kind}:${i.request_id ?? i.app_id ?? i.cluster_id ?? ''}:${i.environment_id ?? ''}:${i.detail ?? ''}`} item={i} />)}
        {items.length > VISIBLE && (
          <li className="px-5 py-3">
            <Button variant="ghost" size="sm" onClick={() => setAll(v => !v)}>{all ? 'Ver menos' : `Ver ${items.length - VISIBLE} más`}</Button>
          </li>
        )}
      </AttentionList>
      {data.truncated && <p className="text-[13px] text-muted-foreground">Hay más de lo que cabe aquí: resuelve estos y vuelve a mirar.</p>}
    </>
  )
}

function Row({ item: i }: { item: Item }) {
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
      action={<ButtonLink variant="outline" size="sm" render={<Link to={i.link} />}>{v.action}</ButtonLink>}
    >
      {i.at ? formatRelative(i.at) : undefined}
    </AttentionItem>
  )
}
