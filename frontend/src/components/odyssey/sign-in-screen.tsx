import { LogIn } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { ErrorNotice } from './feedback'
import { Wordmark } from './logo'

/** Full-page sign-in card shown by AuthGate until there is a session. */
export function SignInScreen({ loading, error, busy, canSignIn, onSignIn }: { loading?: boolean; error?: unknown; busy?: boolean; canSignIn: boolean; onSignIn: () => void }) {
  return (
    <main className="flex min-h-dvh items-center justify-center bg-sidebar p-6">
      <div className="flex w-full max-w-md flex-col gap-6 rounded-xl border bg-card p-8 shadow-overlay">
        <Wordmark />
        <div>
          <h1 className="font-heading text-title font-bold">Publica tus apps, de forma segura</h1>
          <p className="mt-2 text-muted-foreground">Entra con la cuenta de tu empresa. No necesitas otra contraseña.</p>
        </div>
        <ErrorNotice error={error} />
        {loading ? (
          <div role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner />
            Conectando…
          </div>
        ) : (
          <Button size="lg" block disabled={busy} onClick={onSignIn}>
            {busy ? <Spinner /> : <LogIn />}
            {busy ? 'Redirigiendo…' : canSignIn ? 'Entrar con mi cuenta' : 'Reintentar conexión'}
          </Button>
        )}
        <p className="border-t pt-4 text-xs text-muted-foreground">inEdge · Identidad y acceso protegidos por IAMKit</p>
      </div>
    </main>
  )
}
