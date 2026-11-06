import { useState, type ReactNode } from 'react'
import { Bot, Check, Code2, Download, ExternalLink, MousePointer2, Plug, Sparkles, SquareTerminal, X } from 'lucide-react'
import { toast } from 'sonner'
import { Callout, CodeLine, ErrorNotice, Hero, Loading, OptionCards, Panel, PanelBody, PanelHeader, PanelList, PanelRow, StepNumber } from '@/components/odyssey'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button, ButtonLink } from '@/components/ui/button'
import { assistantState, offered, type AssistantState } from '@/features/assistant/state'
import { useAssistant } from '@/lib/assistant'
import { formatDateTime, formatRelative } from '@/lib/format'
import { knownMe, useDisconnectAssistant, useMe } from '@/lib/me'
import { Technical } from '@/lib/preferences'
import type { Assistant, AssistantClient, AssistantConnection, Me } from '@/lib/types'

type Key = AssistantClient['key'] | 'other'

const icons: Record<Key, ReactNode> = {
  claude_code: <Sparkles className="mt-0.5 size-4" />,
  codex: <Code2 className="mt-0.5 size-4" />,
  cursor: <MousePointer2 className="mt-0.5 size-4" />,
  vscode: <SquareTerminal className="mt-0.5 size-4" />,
  antigravity: <Bot className="mt-0.5 size-4" />,
  other: <Plug className="mt-0.5 size-4" />,
}

const example = 'crea una app para registrar visitas a clientes y publícala en Odicea'

/**
 * "Tu asistente" (UX v4 §6.1): whether it is connected (GET /me, never
 * guessed), what it may do with this person's role, and how to connect one —
 * per client, in one click where the client has an install link. Before
 * connecting it says which server and with what permissions. Reached from
 * the header chip and the avatar menu, not from a tab.
 */
export function AssistantPage() {
  const assistant = useAssistant()
  const me = useMe()
  const known = knownMe(me)
  const [leaving, setLeaving] = useState<AssistantConnection>()
  const state = assistantState(known, offered(assistant.data))
  // Not offered here, but something still connected: show it, so it can be disconnected.
  const status = <Status me={known} state={state} loading={me.isPending} onDisconnect={setLeaving} />
  return (
    <>
      <Hero title="Tu asistente">Tu asistente de IA crea, publica y arregla tus apps por ti, con tus permisos. Lo conectas una vez.</Hero>
      {assistant.isPending ? (
        <Loading />
      ) : assistant.error ? (
        <ErrorNotice error={assistant.error} onRetry={() => assistant.refetch()} />
      ) : !offered(assistant.data) ? (
        <div className="mx-auto flex w-full max-w-[860px] flex-col gap-6">
          <Callout tone="info" title="Tu equipo de TI todavía no habilitó el asistente.">
            Cuando lo hagan, aquí verás cómo conectar el tuyo. Mientras tanto puedes publicar desde la consola.
          </Callout>
          {state.kind === 'connected' && status}
        </div>
      ) : (
        <div className="mx-auto flex w-full max-w-[860px] flex-col gap-6">
          {status}
          {known?.assistant && <Abilities me={known} />}
          <Connect data={assistant.data} me={known} />
        </div>
      )}
      {/* Outside Status: disconnecting the last assistant swaps Status for a callout, and the dialog must outlive it. */}
      <Disconnect connection={leaving} onClose={() => setLeaving(undefined)} />
    </>
  )
}

function Status({ me, state, loading, onDisconnect }: { me?: Me; state: AssistantState; loading: boolean; onDisconnect: (c: AssistantConnection) => void }) {
  if (loading) return <Loading label="Viendo si tu asistente está conectado…" />
  if (state.kind === 'unknown') {
    return (
      <Callout tone="neutral" title="No pudimos ver si tu asistente está conectado.">
        Puedes conectarlo igual con los pasos de abajo; si ya lo hiciste, sigue funcionando.
      </Callout>
    )
  }
  if (state.kind !== 'connected' || !me?.assistant) {
    return (
      <Callout tone="ai" title="Todavía no conectaste tu asistente.">
        Elige el tuyo abajo y sigue los pasos. Toma un par de minutos.
      </Callout>
    )
  }
  return (
    <Panel>
      <PanelHeader title="Conectado" description="Puedes desconectarlo cuando quieras: deja de poder usar Odicea hasta que lo conectes de nuevo." />
      <PanelList>
        {me.assistant.connected.map(c => (
          <PanelRow key={c.id} className="gap-3">
            <span aria-hidden className="size-2 shrink-0 rounded-full bg-success" />
            <span className="min-w-0 flex-1">
              <span className="block font-medium">{c.name}</span>
              <span className="block text-xs text-muted-foreground">
                {c.last_used_at ? `Lo usaste ${formatRelative(c.last_used_at)}` : 'Sin sesión abierta ahora'}
                {c.consented_at ? ` · conectado el ${formatDateTime(c.consented_at)}` : ' · habilitado por tu equipo de TI'}
              </span>
            </span>
            <Button size="sm" variant="outline" onClick={() => onDisconnect(c)}>Desconectar</Button>
          </PanelRow>
        ))}
      </PanelList>
    </Panel>
  )
}

function Disconnect({ connection, onClose }: { connection?: AssistantConnection; onClose: () => void }) {
  const disconnect = useDisconnectAssistant()
  return (
    <AlertDialog open={!!connection} onOpenChange={open => { if (!open) { onClose(); disconnect.reset() } }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>¿Desconectar {connection?.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            Se cierran sus sesiones y deja de poder ver o cambiar tus apps. Tus apps y lo que ya publicó se quedan como están; puedes volver a conectarlo cuando quieras.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <ErrorNotice error={disconnect.error} />
        <AlertDialogFooter>
          <AlertDialogCancel>Cancelar</AlertDialogCancel>
          <AlertDialogAction
            disabled={disconnect.isPending}
            onClick={() => connection && disconnect.mutate(connection.id, { onSuccess: () => { onClose(); toast.success(`${connection.name} desconectado`) } })}
          >
            {disconnect.isPending ? 'Desconectando…' : 'Desconectar'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/** "Con tu rol, tu asistente podrá… / no podrá…": the server's sentences, from the real permissions. */
function Abilities({ me }: { me: Me }) {
  const a = me.assistant
  if (!a || a.can.length + a.cannot.length === 0) return null
  return (
    <Panel>
      <PanelHeader title={me.role ? `Con tu rol (${me.role}), tu asistente…` : 'Con tus permisos, tu asistente…'} description="Usa tus permisos: nunca puede hacer más que tú." />
      <PanelBody className={a.cannot.length > 0 ? 'grid gap-5 sm:grid-cols-2' : 'grid gap-5'}>
        <AbilityList title="Podrá" items={a.can} ok />
        {a.cannot.length > 0 && <AbilityList title="No podrá" items={a.cannot} />}
      </PanelBody>
      <Technical>
        <PanelBody className="border-t pt-4">
          <span className="text-xs text-muted-foreground">Herramientas MCP que puede usar ({a.tools.length})</span>
          <ul aria-label="Herramientas MCP" className="flex flex-wrap gap-1.5">
            {a.tools.map(t => <li key={t}><code className="rounded-sm bg-inset px-1.5 py-0.5 font-mono text-xs">{t}</code></li>)}
          </ul>
        </PanelBody>
      </Technical>
    </Panel>
  )
}

function AbilityList({ title, items, ok }: { title: string; items: string[]; ok?: boolean }) {
  const Icon = ok ? Check : X
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold">{title}</h3>
      <ul className="flex flex-col gap-1.5">
        {items.map(s => (
          <li key={s} className="flex items-start gap-2 text-sm">
            <Icon aria-hidden className={ok ? 'mt-0.5 size-4 shrink-0 text-success' : 'mt-0.5 size-4 shrink-0 text-muted-foreground'} />
            <span>{s}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function Connect({ data, me }: { data: Assistant; me?: Me }) {
  const [key, setKey] = useState<Key>(data.clients[0].key)
  const client = data.clients.find(c => c.key === key)
  const name = client?.name ?? 'tu asistente'
  return (
    <section aria-labelledby="connect-title" className="flex flex-col gap-3">
      <h2 id="connect-title" className="text-base font-semibold">Conectar un asistente</h2>
      <div className="grid gap-6 md:grid-cols-[1fr_1.4fr]">
        <OptionCards
          label="Asistente"
          value={key}
          onChange={setKey}
          options={[...data.clients.map(c => ({ value: c.key as Key, title: c.name, icon: icons[c.key] })), { value: 'other' as Key, title: 'Otro', icon: icons.other }]}
        />
        <Panel>
          <PanelHeader title={client ? `Conectar ${client.name}` : 'Conectar otro asistente'} />
          <PanelBody className="gap-4">
            <p className="text-sm text-muted-foreground">
              Vas a agregar el servidor <b className="font-medium text-foreground">odicea</b> en <code className="font-mono text-xs">{data.mcp_url}</code>.
              Entra con la cuenta de tu empresa y actúa con tus permisos{me?.role ? ` de ${me.role}` : ''}; no guarda contraseñas.
            </p>
            <Step n={1} title={`Agrega Odicea a ${name}`}>
              {client?.install_link && (
                <ButtonLink href={client.install_link} className="self-start">
                  <ExternalLink />
                  Agregar a {client.name}
                </ButtonLink>
              )}
              {client?.install_link && <span className="text-xs text-muted-foreground">O a mano:</span>}
              <CodeLine code={client?.install ?? data.mcp_url} />
            </Step>
            <Step n={2} title="Entra con la cuenta de tu empresa">
              <span className="text-muted-foreground">
                {client?.notes ?? 'Agrega un servidor MCP remoto (HTTP) con esa dirección. Tu asistente abrirá el navegador para que entres con la cuenta de tu empresa (OAuth).'}
              </span>
            </Step>
            <Step n={3} title="Instala la skill de Odicea (le enseña a preparar y publicar tus apps)">
              {client ? (
                <CodeLine code={client.skill_install} />
              ) : (
                <span className="text-muted-foreground">Descomprímela en la carpeta de skills de tu asistente.</span>
              )}
              <a className="inline-flex items-center gap-1.5 self-start text-sm underline underline-offset-4" href={data.skill.url}>
                <Download className="size-3.5" aria-hidden />
                Descargar la skill
              </a>
            </Step>
            <Step n={4} title="Pídele algo">
              <span>
                Por ejemplo: <i>“{example}”</i>.
              </span>
            </Step>
            <Technical>
              <Callout tone="info" title="Detalles técnicos:">
                Servidor MCP <code>odicea</code> en <code>{data.mcp_url}</code>, por HTTP con OAuth 2.1 (IAMKit). Las herramientas disponibles dependen de tus permisos. Skill{' '}
                <code>{data.skill.name}</code> versión <code>{data.skill.version}</code>.
              </Callout>
            </Technical>
          </PanelBody>
        </Panel>
      </div>
    </section>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <div className="flex items-start gap-3">
      <StepNumber n={n} />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <span>{title}</span>
        {children}
      </div>
    </div>
  )
}
