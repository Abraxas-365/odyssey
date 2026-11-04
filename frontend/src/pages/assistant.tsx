import { useState, type ReactNode } from 'react'
import { Bot, Code2, Sparkles, SquareTerminal } from 'lucide-react'
import { Callout, CodeLine, ErrorNotice, Hero, Loading, OptionCards, Panel, PanelBody, PanelHeader, StepNumber } from '@/components/odyssey'
import { useAssistant } from '@/lib/assistant'
import { Technical } from '@/lib/preferences'
import type { AssistantClient } from '@/lib/types'

const icons: Record<AssistantClient['key'], ReactNode> = {
  claude_code: <Sparkles className="mt-0.5 size-4" />,
  codex: <Code2 className="mt-0.5 size-4" />,
  vscode: <SquareTerminal className="mt-0.5 size-4" />,
  antigravity: <Bot className="mt-0.5 size-4" />,
}

const example = 'crea una app para registrar visitas a clientes y publícala en Odicea'

/**
 * "Conecta tu asistente" (UX v3 §6): one MCP for every assistant, plus the
 * `odicea` skill that teaches it how to prepare and publish apps. Everything
 * shown comes from GET /assistant, which lists only what this installation
 * offers; with the MCP off there is nothing to connect.
 */
export function AssistantPage() {
  const assistant = useAssistant()
  return (
    <>
      <Hero title="Conecta tu asistente">Tu asistente de IA crea, publica y arregla tus apps por ti, con tus permisos. Lo conectas una vez.</Hero>
      {assistant.isPending ? (
        <Loading />
      ) : assistant.error ? (
        <ErrorNotice error={assistant.error} onRetry={() => assistant.refetch()} />
      ) : !assistant.data.available || assistant.data.clients.length === 0 ? (
        <div className="mx-auto w-full max-w-[860px]">
          <Callout tone="info" title="Tu equipo de TI todavía no habilitó el asistente.">
            Cuando lo hagan, aquí verás cómo conectar el tuyo. Mientras tanto puedes publicar desde la consola.
          </Callout>
        </div>
      ) : (
        <Connect data={assistant.data} />
      )}
    </>
  )
}

function Connect({ data }: { data: NonNullable<ReturnType<typeof useAssistant>['data']> }) {
  const [key, setKey] = useState<AssistantClient['key']>(data.clients[0].key)
  const client = data.clients.find(c => c.key === key) ?? data.clients[0]
  return (
    <div className="mx-auto grid w-full max-w-[860px] gap-6 md:grid-cols-[1fr_1.3fr]">
      <OptionCards
        label="Asistente"
        value={client.key}
        onChange={setKey}
        options={data.clients.map(c => ({ value: c.key, title: c.name, icon: icons[c.key] }))}
      />
      <Panel>
        <PanelHeader title={`Conectar ${client.name}`} />
        <PanelBody className="gap-4">
          <Step n={1} title="Agrega Odicea a tu asistente">
            <CodeLine code={client.install} />
          </Step>
          <Step n={2} title="Entra con la cuenta de tu empresa">
            <span className="text-muted-foreground">{client.notes}</span>
          </Step>
          <Step n={3} title="Instala la skill de Odicea (le enseña a preparar y publicar tus apps)">
            <CodeLine code={client.skill_install} />
          </Step>
          <Step n={4} title="Pídele algo">
            <span>
              Por ejemplo: <i>“{example}”</i>.
            </span>
          </Step>
          <Technical>
            <Callout tone="info" title="Detalles técnicos:">
              Servidor MCP <code>odicea</code> en <code>{data.mcp_url}</code>, por HTTP con OAuth 2.1 (IAMKit). Las herramientas disponibles dependen de tus permisos. Skill{' '}
              <code>{data.skill.name}</code> versión <code>{data.skill.version}</code>: <a className="underline underline-offset-4" href={data.skill.url}>descargar</a>.
            </Callout>
          </Technical>
        </PanelBody>
      </Panel>
    </div>
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
