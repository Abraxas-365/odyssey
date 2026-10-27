import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { SignInScreen } from '@/components/odyssey/sign-in-screen'
import type { Permission } from './permissions'
import { accessToken, clearTokens, decode, endSession, loadConfig, readTokens, signIn, signOut, storageKeys, userInfo, type PublicConfig, type UserInfo } from './session'

export interface Auth {
  can: (permission: Permission) => boolean
  user: UserInfo & { id: string }
  organization?: string
  logout: () => void
  switchOrganization: () => void
}

const Context = createContext<Auth | null>(null)

export function useAuth() {
  const value = useContext(Context)
  if (!value) throw new Error('useAuth needs <AuthGate>')
  return value
}

/** Test/showcase seam: render children with a fixed identity. */
export function AuthProvider({ value, children }: { value: Auth; children: ReactNode }) {
  return <Context.Provider value={value}>{children}</Context.Provider>
}

export function AuthGate({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [cfg, setCfg] = useState<PublicConfig>()
  const [ready, setReady] = useState(false)
  const [claims, setClaims] = useState(() => decode(readTokens()?.access_token))
  const [user, setUser] = useState<UserInfo>({})
  const [error, setError] = useState<unknown>()
  const [signingIn, setSigningIn] = useState(false)
  const [retry, setRetry] = useState(0)

  useEffect(() => {
    let alive = true
    const expire = () => {
      clearTokens(); queryClient.clear()
      if (alive) { setReady(false); setError(new Error('Tu sesión expiró. Vuelve a ingresar.')) }
    }
    const update = () => { if (alive) setClaims(decode(readTokens()?.access_token)) }
    const storage = (event: StorageEvent) => { if (event.key === storageKeys.ended) expire() }
    window.addEventListener('storage', storage)
    window.addEventListener('session-expired', expire)
    window.addEventListener('session-updated', update)
    loadConfig()
      .then(async config => {
        if (!alive) return
        setCfg(config)
        if (!(await accessToken(config)) || !alive) return
        const info = await userInfo(config)
        if (alive && readTokens()) { setClaims(decode(readTokens()?.access_token)); setUser(info ?? {}); setReady(true) }
      })
      .catch(e => { if (alive) setError(e) })
    return () => {
      alive = false
      window.removeEventListener('storage', storage)
      window.removeEventListener('session-expired', expire)
      window.removeEventListener('session-updated', update)
    }
  }, [queryClient, retry])

  if (!ready) {
    return (
      <SignInScreen
        loading={!cfg && !error}
        error={error}
        busy={signingIn}
        canSignIn={!!cfg}
        onSignIn={async () => {
          if (signingIn) return
          setError(undefined)
          if (!cfg) { setRetry(v => v + 1); return }
          setSigningIn(true)
          try { await signIn(cfg, location.pathname + location.search) } catch (e) { setSigningIn(false); setError(e) }
        }}
      />
    )
  }

  const value: Auth = {
    can: p => claims.permissions?.includes(p) ?? false,
    user: { ...user, id: claims.sub ?? '' },
    organization: claims.organization_id,
    logout: () => { queryClient.clear(); if (cfg) signOut(cfg) },
    switchOrganization: () => { queryClient.clear(); endSession(); setReady(false); if (cfg) void signIn(cfg).catch(setError) },
  }
  return <Context.Provider value={value}>{children}</Context.Provider>
}
