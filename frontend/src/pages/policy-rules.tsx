import { useState, type FormEvent } from 'react'
import { Code2, Lock, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { toneBadge, ChipGroup, EmptyState, ErrorNotice, ListSkeleton, Loading, PageHeader, Panel, PanelHeader, RuleRow } from '@/components/odyssey'
import { ApiError } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import { useCreateRule, useDeleteRule, usePolicyRules, useReplaceRule } from '@/lib/policy'
import type { PolicyDraft, PolicyEffect, PolicyEvaluation, PolicyRule } from '@/lib/types'
import { candidateOutcome, draftReady, draftUnchanged, effectInfo, effectOptions, newRuleDraft, ruleOutcome, ruleSentence, toDraft } from '@/features/policy/rules'
import { ExpressionHelp } from '@/features/policy/expression-help'
import { TryRules } from '@/features/policy/try-rules'

/**
 * Configuración → Reglas de TI (policy:manage): the rules every action of the
 * organization is checked against before it happens. Each is a sentence with
 * a switch; "Ver como código" opens the CEL behind it (docs/policy.md) with a
 * dry run. The built-in rules can be switched off, not changed or deleted.
 * Without policy:manage the API would answer 403: say who can see it instead.
 */
export function PolicyRulesPage() {
  const { can } = useAuth()
  if (!can(permissions.policyManage)) {
    return (
      <>
        <PageHeader breadcrumb="Administración" title="Reglas de TI" />
        <EmptyState icon={Lock} title="Solo TI puede ver las reglas">
          Las reglas de TI las administra el equipo de TI de tu organización. Si necesitas que cambie una, pídeselo.
        </EmptyState>
      </>
    )
  }
  return <PolicyRules />
}

function PolicyRules() {
  const rules = usePolicyRules()
  const [creating, setCreating] = useState(false)
  const items = rules.data?.items ?? []

  return (
    <>
      <PageHeader
        breadcrumb="Administración"
        title="Reglas de TI"
        description="Lo que Odicea revisa antes de publicar, abrir una app a más gente o conectar datos. Cada regla bloquea la acción o pide la aprobación de TI."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus />
            Nueva regla
          </Button>
        }
      />
      <Panel>
        <PanelHeader title="Reglas" meta={rules.data ? String(items.length) : undefined} description="Si varias aplican a la vez, bloquear le gana a pedir aprobación, y pedir aprobación le gana a permitir." />
        {rules.isPending ? (
          <ListSkeleton />
        ) : rules.error ? (
          <div className="p-5"><ErrorNotice error={rules.error} onRetry={() => rules.refetch()} /></div>
        ) : items.length === 0 ? (
          <div className="px-5 py-8 text-center text-sm text-muted-foreground">Todavía no hay reglas. Crea la primera con «Nueva regla».</div>
        ) : (
          <div>{items.map(r => <RuleItem key={r.id} rule={r} />)}</div>
        )}
      </Panel>
      {rules.isFetching && !rules.isPending && <Loading label="Actualizando…" className="py-0" />}
      <NewRuleDialog open={creating} onOpenChange={setCreating} />
    </>
  )
}

function RuleItem({ rule }: { rule: PolicyRule }) {
  const replace = useReplaceRule()
  const [open, setOpen] = useState(false)
  const info = effectInfo[rule.effect]
  const builtin = rule.builtin !== null
  const toggle = (enabled: boolean) =>
    replace.mutate({ id: rule.id, body: toDraft(rule, { enabled }) }, { onSuccess: () => toast.success(enabled ? 'Regla encendida' : 'Regla apagada') })

  return (
    <div className="border-t first:border-t-0">
      <RuleRow className="border-t-0 pb-1.5" checked={rule.enabled} onCheckedChange={toggle} disabled={replace.isPending} trailing={
        <>
          {builtin && <Badge variant="outline"><Lock aria-hidden />Incorporada</Badge>}
          <Badge variant={toneBadge[info.tone]}>{info.label}</Badge>
        </>
      }>
        <b>{rule.name}</b>
        {rule.description.trim() && <span className="mt-0.5 block text-muted-foreground">{ruleSentence(rule)}</span>}
      </RuleRow>
      <div className="px-5 pb-3">
        <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen(o => !o)}>
          <Code2 />
          {open ? 'Ocultar el código' : 'Ver como código'}
        </Button>
        <ErrorNotice error={replace.error} className="mt-2" />
        {open && <RuleCode rule={rule} builtin={builtin} />}
      </div>
    </div>
  )
}

/** The CEL behind a rule, editable when IT wrote it, with "Probar" under it. */
function RuleCode({ rule, builtin }: { rule: PolicyRule; builtin: boolean }) {
  const replace = useReplaceRule()
  const remove = useDeleteRule()
  const savedIds = (usePolicyRules().data?.items ?? []).map(r => r.id)
  const [draft, setDraft] = useState<PolicyDraft>(() => toDraft(rule))
  const [confirmDelete, setConfirmDelete] = useState(false)
  const unchanged = draftUnchanged(rule, draft)
  const save = () => replace.mutate({ id: rule.id, body: draft }, { onSuccess: () => toast.success('Regla guardada') })
  const field = replace.error instanceof ApiError ? replace.error.field : undefined

  return (
    <div className="mt-2 flex flex-col gap-3">
      <Field data-invalid={field === 'expression' || undefined}>
        <FieldLabel htmlFor={`cel-${rule.id}`}>Condición (CEL)</FieldLabel>
        <Textarea
          id={`cel-${rule.id}`}
          className="min-h-20 font-mono text-[13px]"
          spellCheck={false}
          readOnly={builtin}
          value={draft.expression}
          onChange={e => setDraft(d => ({ ...d, expression: e.target.value }))}
          aria-invalid={field === 'expression' || undefined}
        />
        <FieldDescription>
          {builtin ? 'Las reglas incorporadas se pueden apagar, pero no cambiar. Para otra condición, apágala y crea la tuya.' : 'La regla aplica cuando esta condición es verdadera. Si no se puede evaluar, cuenta con su misma fuerza: una que bloquea, bloquea; una que pide aprobación, la pide.'}
        </FieldDescription>
        {!builtin && <ExpressionHelp />}
      </Field>
      {!builtin && (
        <FieldGroup className="gap-3">
          <Field>
            <FieldLabel htmlFor={`name-${rule.id}`}>Nombre</FieldLabel>
            <Input id={`name-${rule.id}`} maxLength={200} value={draft.name} onChange={e => setDraft(d => ({ ...d, name: e.target.value }))} />
          </Field>
          <Field>
            <FieldLabel htmlFor={`desc-${rule.id}`}>En palabras</FieldLabel>
            <Input id={`desc-${rule.id}`} maxLength={500} value={draft.description ?? ''} onChange={e => setDraft(d => ({ ...d, description: e.target.value }))} />
          </Field>
          <ChipGroup label="Qué hace la regla" value={draft.effect} onChange={(effect: PolicyEffect) => setDraft(d => ({ ...d, effect }))} options={effectOptions.map(o => ({ value: o.value, label: o.title }))} />
        </FieldGroup>
      )}
      <ErrorNotice error={replace.error} />
      {!builtin && (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Button variant="destructive" size="sm" onClick={() => setConfirmDelete(true)}>
            <Trash2 />
            Eliminar regla
          </Button>
          <Button size="sm" disabled={replace.isPending || unchanged || !draftReady(draft)} onClick={save}>
            {replace.isPending ? 'Guardando…' : 'Guardar'}
          </Button>
        </div>
      )}
      <TryRules
        {...(builtin ? { outcome: (e: PolicyEvaluation) => ruleOutcome(e, rule.id).text } : { candidate: { draft, replaces: rule.id }, outcome: (e: PolicyEvaluation) => candidateOutcome(e, savedIds).text })}
        disabled={!builtin && !draftReady(draft)}
      />
      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>¿Eliminar «{rule.name}»?</AlertDialogTitle>
            <AlertDialogDescription>Deja de aplicarse enseguida. Si solo quieres pausarla, apágala con el interruptor.</AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorNotice error={remove.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={remove.isPending} onClick={e => { e.preventDefault(); remove.mutate(rule.id, { onSuccess: () => { setConfirmDelete(false); toast.success('Regla eliminada') } }) }}>
              {remove.isPending ? 'Eliminando…' : 'Eliminar'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function NewRuleDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const create = useCreateRule()
  const savedIds = (usePolicyRules().data?.items ?? []).map(r => r.id)
  const [draft, setDraft] = useState<PolicyDraft>(newRuleDraft)
  const field = create.error instanceof ApiError ? create.error.field : undefined
  const close = (v: boolean) => {
    if (create.isPending) return
    onOpenChange(v)
    if (!v) { setDraft(newRuleDraft()); create.reset() }
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    create.mutate({ ...draft, name: draft.name.trim() }, { onSuccess: () => { close(false); toast.success('Regla creada') } })
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Nueva regla</DialogTitle>
            <DialogDescription>Escribe qué situación vigila y qué hace Odicea cuando ocurre.</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field data-invalid={field === 'name' || undefined}>
              <FieldLabel htmlFor="rule-name">Nombre</FieldLabel>
              <Input id="rule-name" autoFocus required maxLength={200} value={draft.name} onChange={e => setDraft(d => ({ ...d, name: e.target.value }))} placeholder="Los datos de clientes necesitan aprobación" />
            </Field>
            <Field>
              <FieldLabel htmlFor="rule-description">En palabras <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
              <Input id="rule-description" maxLength={500} value={draft.description ?? ''} onChange={e => setDraft(d => ({ ...d, description: e.target.value }))} />
            </Field>
            <ChipGroup label="Qué hace la regla" value={draft.effect} onChange={(effect: PolicyEffect) => setDraft(d => ({ ...d, effect }))} options={effectOptions.map(o => ({ value: o.value, label: o.title }))} />
            <Field data-invalid={field === 'expression' || undefined}>
              <FieldLabel htmlFor="rule-expression">Condición (CEL)</FieldLabel>
              <Textarea id="rule-expression" required className="min-h-20 font-mono text-[13px]" spellCheck={false} value={draft.expression} onChange={e => setDraft(d => ({ ...d, expression: e.target.value }))} aria-invalid={field === 'expression' || undefined} />
              <FieldDescription>Por ejemplo: <span className="font-mono">action == 'set_access' &amp;&amp; access.mode == 'public'</span>. Se revisa al guardar; con «Probar» la ves en acción antes.</FieldDescription>
              <ExpressionHelp />
            </Field>
          </FieldGroup>
          <TryRules candidate={{ draft }} outcome={e => candidateOutcome(e, savedIds).text} disabled={!draftReady(draft)} />
          <ErrorNotice error={create.error} />
          <DialogFooter>
            <Button type="button" variant="outline" disabled={create.isPending} onClick={() => close(false)}>Cancelar</Button>
            <Button type="submit" disabled={create.isPending || !draftReady(draft)}>{create.isPending ? 'Creando…' : 'Crear regla'}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
