import type { ReactNode } from 'react'
import { CircleHelp, Lock } from 'lucide-react'
import { Radio } from '@base-ui/react/radio'
import { RadioGroup } from '@base-ui/react/radio-group'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

/**
 * Filter chips (`.chip`): single-select segmented filter, e.g.
 * Todas / Mías / Compartidas conmigo.
 */
export function ChipGroup<T extends string>({ value, onChange, options, label, className }: { value: T; onChange: (v: T) => void; options: { value: T; label: ReactNode }[]; label: string; className?: string }) {
  return (
    <div role="radiogroup" aria-label={label} className={cn('flex flex-wrap items-center gap-2', className)}>
      {options.map(o => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          onClick={() => onChange(o.value)}
          className={cn(
            'inline-flex h-[30px] cursor-pointer items-center gap-1.5 rounded-full border bg-background px-3 text-[13px] whitespace-nowrap text-secondary-foreground outline-none transition-colors hover:border-foreground hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50',
            o.value === value && 'border-foreground text-foreground',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

/** Large radio cards (`.opt`): "Acceso general", sizes, create paths. */
export function OptionCards<T extends string>({ value, onChange, options, label, className }: { value: T; onChange: (v: T) => void; options: { value: T; title: ReactNode; description?: ReactNode; icon?: ReactNode; disabled?: boolean }[]; label: string; className?: string }) {
  return (
    <RadioGroup aria-label={label} value={value} onValueChange={v => onChange(v as T)} className={cn('flex flex-col gap-2', className)}>
      {options.map(o => (
        <Radio.Root
          key={o.value}
          value={o.value}
          disabled={o.disabled}
          render={<button type="button" />}
          nativeButton
          className="group flex cursor-pointer items-start gap-3 rounded-lg border px-4 py-3.5 text-left outline-none transition-colors hover:border-foreground/40 focus-visible:ring-3 focus-visible:ring-ring/50 data-checked:border-foreground data-disabled:cursor-not-allowed data-disabled:opacity-50"
        >
          <span className="mt-0.5 size-4 shrink-0 rounded-full border border-muted-foreground group-data-checked:border-[5px] group-data-checked:border-foreground" aria-hidden />
          {o.icon}
          <span className="min-w-0 flex-1">
            <span className="block font-semibold">{o.title}</span>
            {o.description && <span className="block text-[13px] text-muted-foreground">{o.description}</span>}
          </span>
        </Radio.Root>
      ))}
    </RadioGroup>
  )
}

/**
 * Policy as a sentence with a switch (UX v3 §11): "Las aprobaciones de datos
 * vencen a los <b>6 meses</b>". `locked` rules are mandatory.
 */
export function RuleRow({ children, checked, onCheckedChange, locked, disabled, trailing, className }: { children: ReactNode; checked: boolean; onCheckedChange?: (v: boolean) => void; locked?: boolean; disabled?: boolean; trailing?: ReactNode; className?: string }) {
  return (
    <label className={cn('flex items-center gap-3 border-t px-5 py-3 text-[13px] [&_b]:font-semibold [&_b]:text-foreground', !locked && !disabled && 'cursor-pointer', className)}>
      <span className="flex-1">{children}</span>
      {trailing}
      {locked && <Lock className="size-3.5 text-muted-foreground" aria-label="Obligatoria" />}
      <Switch checked={checked} onCheckedChange={onCheckedChange} disabled={locked || disabled} />
    </label>
  )
}

/** Setting with title, explanation and a switch (Configuración, avatar menu). */
export function SwitchField({ title, description, checked, onCheckedChange, disabled }: { title: ReactNode; description?: ReactNode; checked: boolean; onCheckedChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <label className="flex cursor-pointer items-start gap-3">
      <span className="min-w-0 flex-1">
        <span className="block font-medium">{title}</span>
        {description && <span className="block text-xs text-muted-foreground">{description}</span>}
      </span>
      <Switch checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} className="mt-0.5" />
    </label>
  )
}

/**
 * Jargon with a plain explanation (UX v3 §1: "los términos que pueden
 * confundir llevan un tooltip"). Keyboard reachable.
 */
export function Term({ children, help, icon }: { children: ReactNode; help: ReactNode; icon?: boolean }) {
  return (
    <Tooltip>
      <TooltipTrigger render={<span tabIndex={0} />} className={cn('inline-flex items-center gap-1 outline-none focus-visible:ring-2 focus-visible:ring-ring/50', !icon && 'term')}>
        {children}
        {icon && <CircleHelp className="size-3.5 text-muted-foreground" aria-hidden />}
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{help}</TooltipContent>
    </Tooltip>
  )
}

/** Vertical settings navigation (`.subnav`). */
export function SubNav({ items, value, onChange, label }: { items: { value: string; label: ReactNode }[]; value: string; onChange: (v: string) => void; label: string }) {
  return (
    <nav aria-label={label} className="flex flex-col gap-0.5">
      {items.map(i => (
        <button
          key={i.value}
          type="button"
          aria-current={i.value === value ? 'page' : undefined}
          onClick={() => onChange(i.value)}
          className={cn('cursor-pointer rounded-sm px-2.5 py-[7px] text-left text-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50', i.value === value && 'bg-muted font-medium text-foreground')}
        >
          {i.label}
        </button>
      ))}
    </nav>
  )
}

/**
 * "¿Qué quieres crear?" (UX v3 §5): the big describe-it box that starts the
 * create flow. `footer` holds suggestion chips and the submit button.
 */
export function AskBox({ value, onChange, placeholder = 'Ej.: una app para que los vendedores registren sus visitas a clientes, con fotos y ubicación', footer, label = '¿Qué quieres crear?' }: { value: string; onChange: (v: string) => void; placeholder?: string; footer?: ReactNode; label?: string }) {
  return (
    <div className="mx-auto w-full max-w-[720px] rounded-2xl border bg-card p-1.5">
      <Textarea
        aria-label={label}
        value={value}
        onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        rows={3}
        className="min-h-24 resize-none border-0 bg-transparent text-base! focus-visible:ring-0"
      />
      {footer && <div className="flex flex-wrap items-center gap-2 px-2 py-1.5">{footer}</div>}
    </div>
  )
}
