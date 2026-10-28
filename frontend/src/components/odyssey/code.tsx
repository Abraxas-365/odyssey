import { useState, type ReactNode } from 'react'
import { Check, ChevronRight, Copy, Terminal } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    toast.error('No se pudo copiar. Selecciona el texto y cópialo a mano.')
    return false
  }
}

function useCopy(text: string) {
  const [copied, setCopied] = useState(false)
  return {
    copied,
    onCopy: async () => {
      if (await copy(text)) {
        setCopied(true)
        setTimeout(() => setCopied(false), 1600)
      }
    },
  }
}

/** One-line command with a copy button (`.code`): MCP install, CLI, URLs. */
export function CodeLine({ code, className }: { code: string; className?: string }) {
  const { copied, onCopy } = useCopy(code)
  return (
    <div className={cn('flex items-center justify-between gap-2 rounded-md border bg-inset py-1.5 pr-1.5 pl-3 font-mono text-[13px] leading-relaxed', className)}>
      <code tabIndex={0} className="min-w-0 overflow-x-auto rounded-sm whitespace-pre outline-none focus-visible:ring-2 focus-visible:ring-ring/50">{code}</code>
      <Button variant="ghost" size="icon-sm" onClick={onCopy} aria-label={copied ? 'Copiado' : 'Copiar'}>
        {copied ? <Check className="text-success" /> : <Copy />}
      </Button>
    </div>
  )
}

/**
 * The prepared message for the user's AI assistant (`.prompt`), e.g. "Arreglar
 * con mi asistente" (UX v3 §4). The primary action copies it.
 */
export function AssistantPrompt({ text, children, actions }: { text: string; children?: ReactNode; actions?: ReactNode }) {
  const { copied, onCopy } = useCopy(text)
  return (
    <div className="flex flex-col gap-3">
      <div className="rounded-md border bg-inset p-3.5 text-[13px] leading-relaxed text-secondary-foreground">{children ?? text}</div>
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={onCopy}>
          {copied ? <Check /> : <Copy />}
          {copied ? 'Copiado' : 'Copiar para mi asistente'}
        </Button>
        {actions}
      </div>
    </div>
  )
}

export type LogTone = 'time' | 'ok' | 'warn' | 'error' | 'key' | 'plain'
export type LogSegment = string | { text: string; tone: LogTone }

const logTone: Record<LogTone, string> = {
  time: 'text-muted-foreground',
  ok: 'text-success',
  warn: 'text-warning',
  error: 'text-destructive',
  key: 'text-primary',
  plain: '',
}

/** Monospace log viewer (`.logs`). Lines are arrays of tinted segments. */
export function LogViewer({ lines, className, label = 'Registros' }: { lines: LogSegment[][]; className?: string; label?: string }) {
  return (
    <pre aria-label={label} tabIndex={0} className={cn('overflow-auto bg-inset px-5 py-3 font-mono text-[12.5px] leading-[1.7] whitespace-pre text-secondary-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50', className)}>
      {lines.map((line, i) => (
        <div key={i}>
          {line.map((seg, j) =>
            typeof seg === 'string' ? (
              <span key={j}>{seg}</span>
            ) : (
              <span key={j} className={logTone[seg.tone]}>
                {seg.text}
              </span>
            ),
          )}
        </div>
      ))}
    </pre>
  )
}

/**
 * "Ver detalles técnicos" disclosure (`details/summary`): technical evidence
 * stays one click away for everybody, open by default in technical mode.
 */
export function TechnicalDetails({ title = 'Ver detalles técnicos', defaultOpen, children }: { title?: ReactNode; defaultOpen?: boolean; children: ReactNode }) {
  return (
    <Collapsible defaultOpen={defaultOpen}>
      <CollapsibleTrigger className="group flex w-full cursor-pointer items-center gap-2.5 px-5 py-3.5 text-left outline-none hover:bg-muted/40 focus-visible:bg-muted/40">
        <ChevronRight className="size-4 text-muted-foreground transition-transform group-data-[panel-open]:rotate-90" aria-hidden />
        <Terminal className="size-4" aria-hidden />
        <span className="flex-1 font-semibold">{title}</span>
      </CollapsibleTrigger>
      <CollapsibleContent className="border-t">{children}</CollapsibleContent>
    </Collapsible>
  )
}
