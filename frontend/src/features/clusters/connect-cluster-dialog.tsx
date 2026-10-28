import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Callout, CodeLine, ErrorNotice, StepNumber } from '@/components/odyssey'
import { ApiError } from '@/lib/api'
import { AGENT_INSTALL, secretCommand, useCluster, useCreateCluster } from '@/lib/clusters'
import { formatDateTime } from '@/lib/format'
import type { EnrollmentToken } from '@/lib/types'

/** A token just issued, for the cluster it belongs to (shown once). `serial`: the certificate it had (a reconnect waits for a new one). */
export interface Issued { cluster: string; name: string; token: EnrollmentToken; reconnect?: boolean; serial?: string }

/**
 * "Conectar clúster": a name, then the install steps with the one-time token,
 * and a live wait until the agent connects. Opened with `issued` it skips the
 * name (a new token for an existing cluster).
 */
export function ConnectClusterDialog({ open, onOpenChange, issued: given }: { open: boolean; onOpenChange: (open: boolean) => void; issued?: Issued }) {
  const create = useCreateCluster()
  const [name, setName] = useState('')
  const [created, setCreated] = useState<Issued>()
  const issued = given ?? created
  const fieldError = create.error instanceof ApiError ? create.error.field : undefined

  const close = () => {
    if (create.isPending) return
    onOpenChange(false)
    setName(''); setCreated(undefined); create.reset()
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const res = await create.mutateAsync({ name: name.trim() }).catch(() => null)
    if (res) setCreated({ cluster: res.cluster.id, name: res.cluster.name, token: res.enrollment })
  }

  return (
    <Dialog open={open} onOpenChange={v => { if (!v) close() }}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        {issued ? (
          <Install issued={issued} onDone={close} />
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-5">
            <DialogHeader>
              <DialogTitle>Conectar un clúster</DialogTitle>
              <DialogDescription>
                Un clúster de Kubernetes de tu empresa donde correrán las apps. El clúster se conecta a Odyssey por su cuenta: no hace falta abrirle puertos.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field data-invalid={fieldError === 'name' || undefined}>
                <FieldLabel htmlFor="cluster-name">Nombre</FieldLabel>
                <Input id="cluster-name" autoFocus required maxLength={60} value={name} onChange={e => setName(e.target.value)} placeholder="lima-produccion" aria-invalid={fieldError === 'name' || undefined} />
                <FieldDescription>Para reconocerlo en la consola, por ejemplo dónde está.</FieldDescription>
              </Field>
            </FieldGroup>
            <ErrorNotice error={create.error} />
            <DialogFooter>
              <Button type="button" variant="outline" disabled={create.isPending} onClick={close}>Cancelar</Button>
              <Button type="submit" disabled={create.isPending || !name.trim()}>
                {create.isPending && <Spinner />}
                {create.isPending ? 'Registrando…' : 'Continuar'}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Install({ issued, onDone }: { issued: Issued; onDone: () => void }) {
  // A reconnecting cluster may still show its old tunnel: wait for the
  // certificate the new token buys.
  const [done, setDone] = useState(false)
  const cluster = useCluster(issued.cluster, !done)
  const c = cluster.data
  const connected = !!c?.connected && c.certificate.serial !== '' && c.certificate.serial !== (issued.serial ?? '')
  if (connected && !done) setDone(true)
  return (
    <div className="flex min-w-0 flex-col gap-5">
      <DialogHeader>
        <DialogTitle>{issued.reconnect ? `Reconectar ${issued.name}` : `Conectar ${issued.name}`}</DialogTitle>
        <DialogDescription>Ejecuta esto con acceso de administrador al clúster (kubectl).</DialogDescription>
      </DialogHeader>
      <Callout tone="warn" title="Este código se muestra una sola vez.">
        Sirve una vez y vence el {formatDateTime(issued.token.expires_at)}; si lo pierdes, genera uno nuevo desde esta pantalla.
      </Callout>
      <ol className="flex flex-col gap-4">
        <li className="flex items-start gap-3">
          <StepNumber n={1} />
          <div className="flex min-w-0 flex-1 flex-col gap-2">
            <span>Guarda el código de conexión en el clúster</span>
            <CodeLine code={secretCommand(issued.token)} />
          </div>
        </li>
        <li className="flex items-start gap-3">
          <StepNumber n={2} />
          <div className="flex min-w-0 flex-1 flex-col gap-2">
            <span>{issued.reconnect ? 'Si el agente ya está instalado, reinícialo; si no, instálalo' : 'Instala el agente'} <span className="text-muted-foreground">(desde el repositorio de Odyssey)</span></span>
            <CodeLine code={issued.reconnect ? `${AGENT_INSTALL}\nkubectl -n odyssey-system rollout restart deployment/odyssey-agent` : AGENT_INSTALL} />
          </div>
        </li>
        <li className="flex items-start gap-3">
          <StepNumber n={3} done={connected} />
          <div aria-live="polite" className="flex min-w-0 flex-1 items-center gap-2 pt-0.5">
            {connected ? (
              <span><b>Conectado.</b> Las apps de tu organización ya pueden publicarse aquí.</span>
            ) : (
              <><Spinner /><span className="text-muted-foreground">Esperando al agente…</span></>
            )}
          </div>
        </li>
      </ol>
      <ErrorNotice error={cluster.error} onRetry={() => cluster.refetch()} />
      <DialogFooter>
        <Button onClick={onDone} variant={connected ? 'default' : 'outline'}>{connected ? 'Listo' : 'Cerrar (sigo después)'}</Button>
      </DialogFooter>
    </div>
  )
}
