import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'

// UX v3 §2: "Mostrar detalles técnicos" is a per-user preference. The screens
// are the same; technical mode only *adds* detail (commit, branch, logs, env
// vars). Stored per user (sub) so a shared browser doesn't leak the setting.
// TODO(api): move to a server-side user preference once the API has one.
interface Preferences { technical: boolean; setTechnical: (on: boolean) => void }

const Context = createContext<Preferences>({ technical: false, setTechnical: () => {} })
const key = (user: string) => `odyssey:prefs:${user}:technical`

export function PreferencesProvider({ user, defaultTechnical = false, children }: { user: string; defaultTechnical?: boolean; children: ReactNode }) {
  const [technical, set] = useState(() => {
    const stored = localStorage.getItem(key(user))
    return stored === null ? defaultTechnical : stored === '1'
  })
  const setTechnical = useCallback((on: boolean) => { localStorage.setItem(key(user), on ? '1' : '0'); set(on) }, [user])
  const value = useMemo(() => ({ technical, setTechnical }), [technical, setTechnical])
  return <Context.Provider value={value}>{children}</Context.Provider>
}

export const usePreferences = () => useContext(Context)

/** Renders children only in technical mode (the mockup's `T(tech)`). */
export function Technical({ children, fallback = null }: { children: ReactNode; fallback?: ReactNode }) {
  return <>{usePreferences().technical ? children : fallback}</>
}
