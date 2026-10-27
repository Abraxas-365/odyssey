import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { ErrorNotice, Loading, Wordmark } from '@/components/odyssey'
import { complete } from '@/lib/session'

/** OAuth redirect target (/auth/callback): exchange the code, then go back. */
export function AuthCallbackPage() {
  const navigate = useNavigate()
  const [error, setError] = useState<unknown>()
  useEffect(() => {
    complete(new URLSearchParams(location.search))
      .then(back => navigate(back, { replace: true }))
      .catch(setError)
  }, [navigate])
  return (
    <main className="flex min-h-dvh items-center justify-center bg-sidebar p-6">
      <div className="flex w-full max-w-md flex-col gap-5 rounded-xl border bg-card p-8">
        <Wordmark />
        {error ? (
          <>
            <ErrorNotice error={error} />
            <Button onClick={() => navigate('/', { replace: true })}>Volver a ingresar</Button>
          </>
        ) : (
          <Loading label="Entrando…" className="py-0" />
        )}
      </div>
    </main>
  )
}
