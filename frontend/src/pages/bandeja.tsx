import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Check, Inbox, X } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'
import { ApprovalItem, ChipGroup, EmptyState, ErrorNotice, ListSkeleton, PageHeader, Panel, PanelHeader, PanelList, type ApprovalItemData } from '@/components/odyssey'
import { canCancel, canDecide, expiryText, outcomeText, requesterName, ruleNames, statusInfo, tabStatuses, type Tab } from '@/features/approvals/approvals'
import { useApproval, useApprovals, useApprove, useCancelRequest, useReject, type ApprovalFilter } from '@/lib/approvals'
import { useAuth } from '@/lib/auth'
import { formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import type { Approval } from '@/lib/types'

type Scope = 'inbox' | 'mine'

/** The request as the list shows it: plain words, no ids or codes. */
function toItem(a: Approval): ApprovalItemData {
  return {
    id: a.id,
    sentence: a.summary,
    who: requesterName(a),
    why: a.reason,
    rules: a.rules.length ? ruleNames(a) : undefined,
    when: formatRelative(a.created_at),
    until: a.status === 'pending' ? expiryText(a) : outcomeText(a),
    status: statusInfo[a.status],
    note: a.decision_note.trim() || undefined,
    problem: a.execution_error?.trim() || undefined,
  }
}

/** The filter each (scope, tab) asks the API for. */
export function bandejaFilter(scope: Scope, tab: Tab): ApprovalFilter {
  const status = tabStatuses[tab]
  if (scope === 'mine') return { mine: true, status }
  return tab === 'pending' ? { decidable: true, status } : { status }
}

/**
 * Bandeja: what IT must decide (with Aprobar / Rechazar and a note) and
 * "Mis solicitudes" (what I asked for, with Cancelar). Everyone has the
 * second; the first is for whoever holds the approvals permission.
 */
export function BandejaPage() {
  const { can } = useAuth()
  const [params, setParams] = useSearchParams()
  const focusId = params.get('id')
  const focus = useApproval(focusId)
  const decider = can(permissions.approvalsDecide)
  const [scope, setScope] = useState<Scope>(decider ? 'inbox' : 'mine')
  const [tab, setTab] = useState<Tab>('pending')
  const effective: Scope = decider ? scope : 'mine'
  const list = useApprovals(bandejaFilter(effective, tab))
  const items = list.data?.items ?? []

  return (
    <>
      <PageHeader
        title="Bandeja"
        description={decider ? 'Lo que las reglas de TI dejaron esperando una decisión, y lo que tú pediste.' : 'Lo que pediste y las reglas de TI dejaron esperando una decisión.'}
      />
      {focusId && (
        <Panel aria-label="Solicitud enlazada">
          <PanelHeader
            title="La solicitud del enlace"
            actions={<Button variant="ghost" size="sm" onClick={() => setParams({}, { replace: true })}>Cerrar</Button>}
          />
          {focus.isPending ? (
            <ListSkeleton />
          ) : focus.error ? (
            <div className="p-5">
              <EmptyState icon={Inbox} title="No encontramos esa solicitud">Puede ser de otra persona o ya no existe. Mira las de abajo.</EmptyState>
            </div>
          ) : (
            <PanelList>
              <ApprovalItem item={toItem(focus.data)} actions={<Actions approval={focus.data} />} />
            </PanelList>
          )}
        </Panel>
      )}
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
        {decider && (
          <ChipGroup
            label="Qué ver"
            value={scope}
            onChange={setScope}
            options={[{ value: 'inbox', label: 'Para decidir' }, { value: 'mine', label: 'Mis solicitudes' }]}
          />
        )}
        <ChipGroup
          label="Estado"
          value={tab}
          onChange={setTab}
          options={[{ value: 'pending', label: 'Pendientes' }, { value: 'decided', label: 'Decididas' }]}
        />
      </div>
      <Panel>
        <PanelHeader title={effective === 'mine' ? 'Mis solicitudes' : tab === 'pending' ? 'Esperan a TI' : 'Ya decididas'} meta={list.data ? String(list.data.page.total) : undefined} />
        {list.isPending ? (
          <ListSkeleton />
        ) : list.error ? (
          <div className="p-5"><ErrorNotice error={list.error} onRetry={() => list.refetch()} /></div>
        ) : items.length === 0 ? (
          <div className="p-5">
            <EmptyState icon={Inbox} title={tab === 'pending' ? 'Nada esperando' : 'Todavía no hay solicitudes decididas'}>
              {effective === 'mine'
                ? 'Cuando una regla de TI pida aprobar algo que hiciste, lo verás aquí.'
                : 'Cuando una regla de TI deje algo esperando tu decisión, aparecerá aquí.'}
            </EmptyState>
          </div>
        ) : (
          <PanelList>
            {items.map(a => (
              <ApprovalItem key={a.id} item={toItem(a)} actions={<Actions approval={a} />} />
            ))}
          </PanelList>
        )}
      </Panel>
    </>
  )
}

/** Aprobar / Rechazar for who may decide it now; Cancelar for who asked. */
function Actions({ approval: a }: { approval: Approval }) {
  const cancel = useCancelRequest()
  const [deciding, setDeciding] = useState<'approve' | 'reject'>()
  return (
    <>
      <ErrorNotice error={cancel.error} className="w-full" />
      {canCancel(a) && (
        <Button variant="outline" size="sm" disabled={cancel.isPending} onClick={() => cancel.mutate({ id: a.id }, { onSuccess: () => toast.success('Cancelaste la solicitud.') })}>
          {cancel.isPending ? 'Cancelando…' : 'Cancelar'}
        </Button>
      )}
      {canDecide(a) && (
        <>
          <Button variant="outline" size="sm" onClick={() => setDeciding('reject')}>
            <X />
            Rechazar
          </Button>
          <Button variant="primary" size="sm" onClick={() => setDeciding('approve')}>
            <Check />
            Aprobar
          </Button>
        </>
      )}
      {deciding && <DecisionDialog approval={a} decision={deciding} onClose={() => setDeciding(undefined)} />}
    </>
  )
}

/** The decision, with an optional note the person who asked will read. */
function DecisionDialog({ approval: a, decision, onClose }: { approval: Approval; decision: 'approve' | 'reject'; onClose: () => void }) {
  const approve = useApprove()
  const reject = useReject()
  const [note, setNote] = useState('')
  const run = decision === 'approve' ? approve : reject
  const yes = decision === 'approve'
  const submit = () =>
    run.mutate(
      { id: a.id, note },
      {
        onSuccess: () => {
          toast.success(yes ? 'Aprobada. Se hace como lo pidió.' : 'Rechazada. No se hace nada.')
          onClose()
        },
      },
    )
  return (
    <Dialog open onOpenChange={v => !v && !run.isPending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={e => {
            e.preventDefault()
            submit()
          }}
          className="flex flex-col gap-5"
        >
          <DialogHeader>
            <DialogTitle>{yes ? '¿Aprobar?' : '¿Rechazar?'}</DialogTitle>
            <DialogDescription>
              {a.summary}. {yes ? 'Se hace tal como lo pidió quien lo solicitó, con los permisos que tenía.' : 'No se hará y quien lo pidió verá tu nota.'}
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor={`note-${a.id}`}>Nota <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
            <Textarea id={`note-${a.id}`} value={note} maxLength={500} onChange={e => setNote(e.target.value)} placeholder={yes ? 'Por ejemplo: revisado con Seguridad' : 'Por ejemplo: pídelo después del lanzamiento'} />
            <FieldDescription>La verá quien hizo la solicitud.</FieldDescription>
          </Field>
          <ErrorNotice error={run.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={run.isPending}>Volver</Button>
            <Button type="submit" variant={yes ? 'primary' : 'destructive-solid'} disabled={run.isPending}>
              {run.isPending ? 'Guardando…' : yes ? 'Aprobar' : 'Rechazar'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
