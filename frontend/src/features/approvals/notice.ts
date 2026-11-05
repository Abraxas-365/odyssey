import { toast } from 'sonner'
import { isPending, pendingNotice } from '@/lib/approvals'
import type { PendingApproval } from '@/lib/types'

/**
 * What to do with the answer of an action IT's rules may hold: a calm notice
 * when it was held (nothing failed, nothing changed yet; `held` can add more
 * on the screen), else the caller's own success handling.
 */
export function afterWrite<T>(out: T | PendingApproval, ok: (out: T) => void, held?: (out: PendingApproval) => void): void {
  if (isPending(out)) {
    toast.message(pendingNotice)
    held?.(out)
    return
  }
  ok(out)
}
