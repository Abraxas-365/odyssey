import { toast } from 'sonner'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Term } from '@/components/odyssey'
import { errorMessage } from '@/lib/api'
import { useSetTier } from '@/lib/policy'
import type { Project, ProjectTier } from '@/lib/types'
import { tierLabel, tierOptions } from '@/features/policy/rules'

const items = Object.fromEntries(tierOptions.map(o => [o.value, o.title]))

/**
 * The app's "Nivel" (sandbox / interna / producción): what IT's rules ask
 * about. Everyone sees it; only who holds projects:tier changes it
 * (PUT /projects/:id/tier).
 */
export function TierField({ project, canChange }: { project: Project; canChange: boolean }) {
  const set = useSetTier(project.id)
  const change = (tier: ProjectTier) => {
    if (tier === project.tier) return
    set.mutate(tier, {
      onSuccess: () => toast.success(`La app ahora es ${tierLabel(tier).toLowerCase()}.`),
      onError: e => toast.error(errorMessage(e)),
    })
  }
  const help = tierOptions.find(o => o.value === project.tier)?.description

  if (!canChange || project.status === 'archived') return <Term help={help}>{tierLabel(project.tier)}</Term>
  return (
    <Select value={project.tier} items={items} onValueChange={v => change((v ?? project.tier) as ProjectTier)} disabled={set.isPending}>
      <SelectTrigger size="sm" className="w-44" aria-label="Nivel de la app"><SelectValue /></SelectTrigger>
      <SelectContent>
        {tierOptions.map(o => <SelectItem key={o.value} value={o.value}>{o.title}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}
