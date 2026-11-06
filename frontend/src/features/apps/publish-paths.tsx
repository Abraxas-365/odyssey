import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Bot, FolderUp, GitBranch, Rocket } from 'lucide-react'
import { Button, ButtonLink } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { AssistantPrompt, IconBadge, Panel, PanelBody, PanelHeader } from '@/components/odyssey'
import type { Project } from '@/lib/types'
import { ConnectRepoDialog } from '@/features/github/code-panel'
import { assistantPrompt } from './overview'

/** "Pedir a mi asistente": what to say, ready to copy, and where to connect one. */
export function AssistantDialog({ open, onOpenChange, prompt }: { open: boolean; onOpenChange: (open: boolean) => void; prompt: string }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Pedir a mi asistente</DialogTitle>
          <DialogDescription>Abre Claude Code, Codex o Antigravity en la carpeta de la app y pídele lo que quieres cambiar. Para publicarlo, dile:</DialogDescription>
        </DialogHeader>
        <AssistantPrompt
          text={prompt}
          actions={
            <ButtonLink variant="ghost" render={<Link to="/asistente" onClick={() => onOpenChange(false)} />}>
              ¿Aún no lo conectas? Conéctalo
            </ButtonLink>
          }
        />
        <p className="text-xs text-muted-foreground">Tu asistente le cuenta a Odicea qué cambió: así se llamará la versión en el historial.</p>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Nothing online yet (UX v4 §5.3): the three ways in, side by side and
 * visible, no menu. GitHub needs permission to change the app.
 */
export function FirstVersion({ project, canConnect, onUpload }: { project: Project; canConnect: boolean; onUpload: () => void }) {
  const [connect, setConnect] = useState(false)
  const prompt = assistantPrompt(project.slug, { live: false, github: false })
  return (
    <Panel>
      <PanelHeader icon={Rocket} title="Publica la primera versión" description="Elige cómo llega tu código. Puedes cambiar de camino cuando quieras." />
      <PanelBody>
        <ul className="grid gap-3 md:grid-cols-3">
          <Path icon={Bot} title="Pedir a mi asistente" body="Desde la carpeta de tu app, dile a Claude Code, Codex o Antigravity:">
            <AssistantPrompt text={prompt} />
          </Path>
          {canConnect && (
            <Path icon={GitBranch} title="Conectar GitHub" body="Cada cambio que subas a la rama se publica solo.">
              <Button variant="outline" onClick={() => setConnect(true)}>Conectar un repositorio</Button>
            </Path>
          )}
          <Path icon={FolderUp} title="Subir una carpeta" body="Arrastra la carpeta de tu app. La construimos y la publicamos por ti.">
            <Button variant="outline" onClick={onUpload}>Subir una carpeta</Button>
          </Path>
        </ul>
      </PanelBody>
      {canConnect && <ConnectRepoDialog project={project} open={connect} onOpenChange={setConnect} />}
    </Panel>
  )
}

function Path({ icon, title, body, children }: { icon: typeof Bot; title: string; body: string; children: React.ReactNode }) {
  return (
    <li className="flex flex-col gap-3 rounded-lg border p-4">
      <div className="flex items-center gap-2.5">
        <IconBadge icon={icon} size="sm" />
        <h3 className="font-semibold">{title}</h3>
      </div>
      <p className="text-[13px] text-muted-foreground">{body}</p>
      <div className="mt-auto">{children}</div>
    </li>
  )
}
