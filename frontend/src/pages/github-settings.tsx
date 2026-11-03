import { useState } from 'react'
import { Building2, ExternalLink, Plus, Unplug, User } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button, ButtonLink } from '@/components/ui/button'
import { Callout, CodeLine, EmptyState, ErrorNotice, IconBadge, ListSkeleton, Loading, PageHeader, Panel, PanelHeader, PanelList, PanelRow } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { githubErrorText } from '@/lib/github-errors'
import { manageOnGitHubURL, useGitHubInfo, useInstallations, useInstallURL, useUnlinkInstallation } from '@/lib/github'
import { permissions } from '@/lib/permissions'
import { Technical } from '@/lib/preferences'
import type { GitHubInstallation } from '@/lib/types'
import { useGitHubCallback } from '@/features/github/use-github-callback'

/**
 * Configuración → GitHub (TI): the GitHub accounts connected to the
 * organization. "Conectar GitHub" installs Odicea's App on one more account;
 * GitHub then sends the browser back here with a claim we redeem.
 */
export function GitHubSettingsPage() {
  const { can } = useAuth()
  const canManage = can(permissions.integrationsManage)
  const info = useGitHubInfo()
  const callback = useGitHubCallback()
  const enabled = !!info.data?.enabled
  const installations = useInstallations(enabled)
  const install = useInstallURL()
  const [unlinking, setUnlinking] = useState<GitHubInstallation>()
  const unlink = useUnlinkInstallation()
  const items = installations.data?.items ?? []

  const connect = () => install.mutate(undefined, { onSuccess: r => window.location.assign(r.url) })

  return (
    <>
      <PageHeader
        breadcrumb="Configuración"
        title="GitHub"
        description="Conecta las cuentas de GitHub donde vive el código de tus apps. Cada cambio que se suba se publica solo."
        actions={enabled && canManage && (
          <Button onClick={connect} disabled={install.isPending}>
            <Plus />
            {install.isPending ? 'Abriendo GitHub…' : 'Conectar GitHub'}
          </Button>
        )}
      />

      {callback.claiming && <Loading label="Conectando tu cuenta de GitHub…" />}
      {callback.claimError != null && <ErrorNotice error={callback.claimError} />}
      {callback.errorCode && (
        <Callout tone="bad" title="No se pudo conectar GitHub.">
          {githubErrorText(callback.errorCode)}
          <Technical><span className="mt-1.5 block font-mono text-xs">{callback.errorCode}</span></Technical>
        </Callout>
      )}
      {callback.requested && (
        <Callout tone="info" title="Falta una aprobación.">
          Un administrador de la organización en GitHub debe aprobar la instalación.
        </Callout>
      )}
      <ErrorNotice error={install.error} />

      {info.isPending ? (
        <Loading />
      ) : info.error ? (
        <ErrorNotice error={info.error} onRetry={() => info.refetch()} />
      ) : !enabled ? (
        <EmptyState icon={Building2} title="GitHub aún no está disponible">
          TI de tu empresa debe terminar de configurarlo en Odicea. Cuando esté listo, aquí podrás conectar tus cuentas.
          <Technical>
            <span className="mt-3 block text-left">
              <span className="mb-1.5 block text-xs">Quien administra el servidor registra la App de GitHub una vez:</span>
              <CodeLine code="odyssey-api github-app create" />
            </span>
          </Technical>
        </EmptyState>
      ) : (
        <Panel>
          <PanelHeader title="Cuentas conectadas" meta={installations.data ? String(items.length) : undefined} />
          {installations.isPending ? (
            <ListSkeleton />
          ) : installations.error ? (
            <div className="p-5"><ErrorNotice error={installations.error} onRetry={() => installations.refetch()} /></div>
          ) : items.length === 0 ? (
            <div className="px-5 py-8 text-center text-sm text-muted-foreground">
              Todavía no hay ninguna cuenta conectada. {canManage ? 'Conecta GitHub para importar tus repositorios.' : 'Pídele a TI que conecte GitHub.'}
            </div>
          ) : (
            <PanelList>
              {items.map(i => (
                <InstallationRow key={i.id} installation={i} canManage={canManage} onUnlink={() => setUnlinking(i)} />
              ))}
            </PanelList>
          )}
        </Panel>
      )}

      <AlertDialog open={!!unlinking} onOpenChange={open => { if (!open) { setUnlinking(undefined); unlink.reset() } }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>¿Desconectar {unlinking?.account}?</AlertDialogTitle>
            <AlertDialogDescription>
              Las apps conectadas a sus repositorios dejan de publicarse solas; lo que ya está en línea sigue funcionando. Para quitar Odicea de GitHub, hazlo desde “Administrar en GitHub”.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorNotice error={unlink.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={unlink.isPending}
              onClick={() => unlinking && unlink.mutate(unlinking.id, { onSuccess: () => { setUnlinking(undefined); toast.success('Cuenta desconectada') } })}
            >
              {unlink.isPending ? 'Desconectando…' : 'Desconectar'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function InstallationRow({ installation: i, canManage, onUnlink }: { installation: GitHubInstallation; canManage: boolean; onUnlink: () => void }) {
  const org = i.account_type === 'Organization'
  return (
    <PanelRow>
      <IconBadge icon={org ? Building2 : User} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-semibold">{i.account}</span>
          <Badge>{org ? 'Organización' : 'Cuenta personal'}</Badge>
          {i.suspended && <Badge variant="warning">Suspendida</Badge>}
        </div>
        <div className="text-xs text-muted-foreground">
          {i.suspended ? 'GitHub la suspendió: sus repositorios no se pueden publicar hasta reactivarla.' : i.all_repositories ? 'Todos los repositorios' : 'Solo los repositorios que elegiste en GitHub'}
          <Technical>{' · '}<span className="font-mono">{i.external_id}</span></Technical>
        </div>
      </div>
      <ButtonLink variant="outline" size="sm" href={manageOnGitHubURL(i)} target="_blank" rel="noreferrer">
        <ExternalLink />
        Administrar en GitHub
      </ButtonLink>
      {canManage && (
        <Button variant="ghost" size="sm" onClick={onUnlink}>
          <Unplug />
          Desconectar
        </Button>
      )}
    </PanelRow>
  )
}
