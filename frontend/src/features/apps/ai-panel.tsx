import { useState } from 'react'
import { Sparkles } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ErrorNotice, TechnicalDetails } from '@/components/odyssey'
import { useAI, useDisableAI, useEnableAI, useRotateAI } from '@/lib/ai'
import { aiActions, aiState, aiUnavailable, aiVariablesText } from './ai'
import { anchors } from './uses'

/**
 * The AI row of "Datos e IA": the app gets its own key for the company's AI
 * models and reads it from environment variables. The key is never shown.
 * Turning it on, renewing the key and turning it off are one change at a time;
 * the console asks again while one is running.
 */
export function AIRow({ project, env, canWrite, busy, technical }: { project: string; env: string; canWrite: boolean; busy: boolean; technical: boolean }) {
  const ai = useAI(project, env)
  const enable = useEnableAI(project, env)
  const rotate = useRotateAI(project, env)
  const disable = useDisableAI(project, env)
  const [confirming, setConfirming] = useState(false)
  const a = ai.data
  if (!a) return <ErrorNotice error={ai.error} onRetry={() => ai.refetch()} />
  const state = aiState(a)
  const can = aiActions(a, canWrite, busy)
  const pending = enable.isPending || rotate.isPending || disable.isPending
  const why = aiUnavailable(a)

  return (
    <div id={anchors.ai} tabIndex={-1} className="flex scroll-mt-20 flex-col gap-2 rounded-lg border px-4 py-3 outline-none" aria-label="IA de la app" role="group">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <Sparkles className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[13px] font-medium">IA</span>
            <span className="text-xs text-muted-foreground">Los modelos de IA de la empresa, con una llave propia de esta app</span>
            <Badge variant={state.tone}>{state.label}</Badge>
          </div>
        </div>
        {canWrite && (
          <div className="flex items-center gap-1.5">
            {(a.status === 'off' || a.status === 'revoke_failed') && (
              <Button size="sm" variant="outline" disabled={!can.enable || pending} onClick={() => enable.mutate(undefined, { onSuccess: () => toast.success('Activando la IA. La app recibe la llave en unos instantes.') })}>
                Activar
              </Button>
            )}
            {a.status === 'revoke_failed' && (
              <Button size="sm" variant="outline" disabled={!can.retry || pending} onClick={() => disable.mutate(undefined, { onSuccess: () => toast.success('Reintentando revocar la llave…') })}>
                Reintentar
              </Button>
            )}
            {a.status !== 'off' && a.status !== 'revoke_failed' && (
              <>
                <Button size="sm" variant="outline" disabled={!can.rotate || pending} onClick={() => rotate.mutate(undefined, { onSuccess: () => toast.success('Renovando la llave. La anterior deja de servir cuando la app use la nueva.') })}>
                  Renovar llave
                </Button>
                <Button size="sm" variant="outline" disabled={!can.disable || pending} onClick={() => setConfirming(true)}>
                  Desactivar
                </Button>
              </>
            )}
          </div>
        )}
      </div>
      {a.warning && (
        <p className="text-xs text-amber-700 dark:text-amber-400" role="status">
          {a.warning} {a.status === 'revoke_failed' ? 'La app ya no usa esa llave; puedes reintentar o dejar que se reintente sola.' : 'Es una llave anterior; se reintentará sola.'}
          {technical && a.revoke_error ? ` Motivo: ${a.revoke_error}` : ''}
        </p>
      )}
      {why && <p className="text-xs text-muted-foreground" role="status">{why}</p>}
      {a.status === 'enabled' && <p className="text-xs text-muted-foreground">La app ya puede usar los modelos de IA de la empresa sin configurar nada. La llave no se muestra en ningún lugar.</p>}
      {technical && a.status !== 'off' && (
        <div className="rounded-md border">
          <TechnicalDetails title="Variables de la IA" defaultOpen>
            <p className="px-5 py-3 font-mono text-xs break-all text-muted-foreground">{aiVariablesText(a)}</p>
            <p className="px-5 pb-3 font-mono text-xs break-all text-muted-foreground">{a.base_url}</p>
          </TechnicalDetails>
        </div>
      )}
      <ErrorNotice error={enable.error ?? rotate.error ?? disable.error} />
      {confirming && (
        <AlertDialog open onOpenChange={v => !v && !disable.isPending && setConfirming(false)}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>¿Desactivar la IA de esta app?</AlertDialogTitle>
              <AlertDialogDescription>
                La app deja de recibir la llave y sus variables, y la llave se revoca. Si el código de la app usa IA, dejará de funcionar hasta que la actives de nuevo.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel type="button">Cancelar</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive-solid"
                disabled={disable.isPending}
                onClick={() => disable.mutate(undefined, { onSuccess: () => toast.success('Desactivando la IA…'), onSettled: () => setConfirming(false) })}
              >
                {disable.isPending ? 'Desactivando…' : 'Desactivar'}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  )
}
