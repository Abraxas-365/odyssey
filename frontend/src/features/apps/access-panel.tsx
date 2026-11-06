import { useState } from 'react'
import { Lock } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Callout, ErrorNotice, OptionCards, Panel, PanelBody, PanelFooter, PanelHeader } from '@/components/odyssey'
import { anchors } from './uses'
import { useAuth } from '@/lib/auth'
import { afterWrite } from '@/features/approvals/notice'
import { permissions } from '@/lib/permissions'
import { useSetAccess, useSettings } from '@/lib/releases'
import type { AccessMode, CurrentRelease } from '@/lib/types'
import { accessHeadline, accessNotice, accessOptions, noticeText, optionDisabled } from './access'

/**
 * "Quién puede abrirla": who may open the app. Saving while the app is online
 * publishes the same version again with the new access (the server starts it);
 * the page's polling shows that progress like any other publication.
 */
export function AccessPanel({ project, env, current, canWrite, busy }: { project: string; env: string; current: CurrentRelease | undefined; canWrite: boolean; busy: boolean }) {
  const { can } = useAuth()
  const settings = useSettings(project, env)
  const save = useSetAccess(project, env)
  const [chosen, setChosen] = useState<AccessMode>()
  const [asked, setAsked] = useState(false)
  const saved = settings.data?.access.mode
  const mode = chosen ?? saved
  const notice = accessNotice(settings.data, current, chosen)
  const protectionAvailable = settings.data?.access_protection_available !== false
  const applying = notice === 'applying'

  return (
    <Panel id={anchors.access} tabIndex={-1} className="scroll-mt-20 outline-none">
      <PanelHeader icon={Lock} title="Quién puede abrirla" description={accessHeadline(settings.data, current)} />
      <PanelBody>
        <ErrorNotice error={settings.error} onRetry={() => settings.refetch()} />
        {mode && (
          <OptionCards
            label="Quién puede abrir la app"
            value={mode}
            onChange={m => {
              setAsked(false)
              setChosen(m)
            }}
            options={accessOptions.map(o => ({
              value: o.value,
              title: o.title,
              description: o.description,
              icon: <o.icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />,
              disabled: optionDisabled(o.value, { canWrite, canPublic: can(permissions.accessPublic), saved, protectionAvailable }),
            }))}
          />
        )}
        {asked && (
          <Callout tone="info" title="Le pedimos a TI que lo apruebe.">
            Hasta que lo decidan, la app sigue como estaba. Puedes seguir la solicitud en «Mis solicitudes».
          </Callout>
        )}
        {notice && (
          <Callout tone={notice === 'applying' || notice === 'pending' ? 'neutral' : 'warn'} title={noticeText[notice].title}>
            {noticeText[notice].body}
          </Callout>
        )}
        <ErrorNotice error={save.error} />
      </PanelBody>
      {canWrite && (
        <PanelFooter className="justify-end">
          <Button
            disabled={save.isPending || busy || applying || !mode || mode === saved}
            onClick={() =>
              mode &&
              save.mutate(mode, {
                onSuccess: out =>
                  afterWrite(
                    out,
                    done => {
                      setAsked(false)
                      setChosen(undefined)
                      toast.success(done.deployment ? 'Cambiando quién puede abrirla…' : 'Guardado')
                    },
                    () => {
                      // IT's rules hold it: nothing changed yet, so the options go back to what is in force
                      setChosen(undefined)
                      setAsked(true)
                    },
                  ),
              })
            }
          >
            {save.isPending ? 'Guardando…' : applying ? 'Aplicando el cambio…' : 'Guardar'}
          </Button>
        </PanelFooter>
      )}
    </Panel>
  )
}
