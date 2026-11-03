import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { useClaimInstallation, safeReturnTo } from '@/lib/github'

/**
 * What GitHub's setup callback leaves in the URL of /configuracion/github:
 * a `github_claim` to redeem (once), a `github_error` code, or
 * `github=requested` (an owner on GitHub must approve the install).
 */
export function useGitHubCallback() {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const { mutateAsync } = useClaimInstallation()
  const claim = params.get('github_claim')
  const posted = useRef<string | undefined>(undefined)
  const [claiming, setClaiming] = useState(false)
  const [claimError, setClaimError] = useState<unknown>()

  useEffect(() => {
    // The ref survives StrictMode's second run of the effect; a claim is single-use.
    if (!claim || posted.current === claim) return
    posted.current = claim
    setClaiming(true)
    // Out of the URL right away: a reload or a shared link must not retry it.
    setParams(p => { const next = new URLSearchParams(p); next.delete('github_claim'); return next }, { replace: true })
    mutateAsync(claim).then(
      claimed => {
        toast.success(`Conectamos la cuenta ${claimed.installation.account} de GitHub.`)
        setClaiming(false)
        navigate(safeReturnTo(claimed.return_to), { replace: true })
      },
      e => { setClaimError(e); setClaiming(false) },
    )
  }, [claim, mutateAsync, navigate, setParams])

  return {
    claiming,
    claimError,
    errorCode: params.get('github_error') ?? undefined,
    requested: params.get('github') === 'requested',
  }
}
