import { useState } from 'react'
import { Lock } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Callout, ErrorNotice, OptionCards, Panel, PanelBody, PanelFooter, PanelHeader } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import { useSetAccess, useSettings } from '@/lib/releases'
import type { AccessMode, CurrentRelease } from '@/lib/types'
import { accessLabel, accessNotice, accessOptions, noticeText, optionDisabled } from './access'

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
  const inForce = settings.data?.access.mode
  const mode = chosen ?? inForce
  const notice = accessNotice(settings.data, current, chosen)
  const protectionAvailable = settings.data?.access_protection_available !== false
  const applying = notice === 'applying'

  return (
    <Panel>
      <PanelHeader icon={Lock} title="Quién puede abrirla" description={inForce ? `Ahora: ${accessLabel(inForce).toLowerCase()}.` : undefined} />
      <PanelBody>
        <ErrorNotice error={settings.error} onRetry={() => settings.refetch()} />
        {mode && (
          <OptionCards
            label="Quién puede abrir la app"
            value={mode}
            onChange={setChosen}
            options={accessOptions.map(o => ({
              value: o.value,
              title: o.title,
              description: o.description,
              icon: <o.icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />,
              disabled: optionDisabled(o.value, { canWrite, canPublic: can(permissions.accessPublic), inForce, protectionAvailable }),
            }))}
          />
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
            disabled={save.isPending || busy || applying || !mode || mode === inForce}
            onClick={() =>
              mode &&
              save.mutate(mode, {
                onSuccess: out => {
                  setChosen(undefined)
                  toast.success(out.deployment ? 'Cambiando quién puede abrirla…' : 'Guardado')
                },
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
