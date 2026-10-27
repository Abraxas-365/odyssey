import { useState } from 'react'
import { Bot, Code2, Sparkles } from 'lucide-react'
import { Callout, CodeLine, Hero, OptionCards, Panel, PanelBody, PanelHeader, StepNumber } from '@/components/odyssey'
import { Technical } from '@/lib/preferences'

const clients = {
  claude: { label: 'Claude Code', icon: <Sparkles className="mt-0.5 size-4" />, install: 'claude mcp add --transport http odyssey https://odyssey.local/mcp' },
  codex: { label: 'Codex', icon: <Code2 className="mt-0.5 size-4" />, install: 'codex mcp add odyssey --url https://odyssey.local/mcp' },
  other: { label: 'Otro asistente', icon: <Bot className="mt-0.5 size-4" />, install: 'https://odyssey.local/mcp' },
} as const

type Client = keyof typeof clients

/**
 * "Conecta tu asistente" (UX v3 §6): one MCP for every assistant. The MCP
 * server is not built yet; the URL is a placeholder until it ships.
 * TODO(api): take the MCP URL from /api/config.
 */
export function AssistantPage() {
  const [client, setClient] = useState<Client>('claude')
  return (
    <>
      <Hero title="Conecta tu asistente">Tu asistente de IA crea, publica y arregla tus apps por ti, con tus permisos. Lo conectas una vez.</Hero>
      <div className="mx-auto grid w-full max-w-[860px] gap-6 md:grid-cols-[1fr_1.3fr]">
        <OptionCards
          label="Asistente"
          value={client}
          onChange={setClient}
          options={(Object.keys(clients) as Client[]).map(k => ({ value: k, title: clients[k].label, icon: clients[k].icon }))}
        />
        <Panel>
          <PanelHeader title={`Conectar ${clients[client].label}`} />
          <PanelBody className="gap-4">
            <div className="flex items-start gap-3">
              <StepNumber n={1} />
              <div className="flex min-w-0 flex-1 flex-col gap-2">
                <span>{client === 'other' ? 'Agrega esta dirección como servidor MCP' : 'Pega esto en tu terminal'}</span>
                <CodeLine code={clients[client].install} />
              </div>
            </div>
            <div className="flex items-start gap-3">
              <StepNumber n={2} />
              <span>La primera vez te pedirá entrar con la cuenta de tu empresa.</span>
            </div>
            <div className="flex items-start gap-3">
              <StepNumber n={3} />
              <span>Pídele: <i>“crea una app para registrar visitas a clientes y publícala”</i>.</span>
            </div>
            <Technical>
              <Callout tone="info" title="Detalles técnicos:">MCP por HTTP con OAuth 2.1 (IAMKit). Las herramientas disponibles dependen de tus permisos.</Callout>
            </Technical>
          </PanelBody>
        </Panel>
      </div>
    </>
  )
}
