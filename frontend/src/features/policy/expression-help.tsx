import { expressionVariables } from './rules'

/** The variables a condition can read, under the editor (docs/policy.md "The input"). */
export function ExpressionHelp() {
  return (
    <details className="text-[13px] text-muted-foreground">
      <summary className="cursor-pointer select-none font-medium text-foreground">Qué puede leer la condición</summary>
      <dl className="mt-2 grid gap-x-3 gap-y-1 sm:grid-cols-[max-content_1fr]">
        {expressionVariables.map(v => (
          <div key={v.name} className="contents">
            <dt className="font-mono text-[12px] text-foreground">{v.name}</dt>
            <dd>{v.description}</dd>
          </div>
        ))}
      </dl>
    </details>
  )
}
