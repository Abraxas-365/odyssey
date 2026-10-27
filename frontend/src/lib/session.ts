// Hosted-login OAuth code + PKCE against IAMKit (same flow as carimbo/PP).
// GET /api/config tells the SPA where IAMKit is and which public client to
// use (docs/api.md §1). JWT payloads are display-only: the API verifies
// identity, permissions and tenant on every request.
import type { PublicConfig } from './types'

export type { PublicConfig }
export interface Tokens { access_token: string; refresh_token?: string; id_token?: string; expires_at: number }
export interface Claims { sub?: string; organization_id?: string; permissions?: string[]; nonce?: string; name?: string; email?: string }
interface Pending { state: string; nonce: string; verifier: string; redirect: string; client: string; iam: string; back: string }

export const storageKeys = {
  pending: 'odyssey:pending-sign-in',
  tokens: 'odyssey:tokens',
  ended: 'odyssey:session-ended',
} as const

export class SignInError extends Error {}

const trim = (url: string) => url.replace(/\/+$/, '')
const base64url = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
export const randomString = (bytes = 32) => base64url(crypto.getRandomValues(new Uint8Array(bytes)))
export async function challenge(verifier: string) {
  return base64url(new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))))
}

export function decode(token?: string): Claims {
  try {
    const payload = token!.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')
    const bytes = Uint8Array.from(atob(payload.padEnd(Math.ceil(payload.length / 4) * 4, '=')), c => c.charCodeAt(0))
    return JSON.parse(new TextDecoder().decode(bytes))
  } catch {
    return {}
  }
}

let config: Promise<PublicConfig> | null = null
export function loadConfig(): Promise<PublicConfig> {
  config ??= fetch('/api/config', { credentials: 'omit' })
    .then(async res => {
      if (!res.ok) throw new SignInError('Odyssey no responde. Intenta de nuevo en unos segundos.')
      const cfg = (await res.json()) as PublicConfig
      if (!cfg.iam_url || !cfg.client_id || !cfg.environment_id) throw new SignInError('El inicio de sesión todavía no está configurado. Avísale a TI.')
      return cfg
    })
    .catch(e => {
      config = null
      throw e instanceof SignInError ? e : new SignInError('Odyssey no responde. Intenta de nuevo en unos segundos.')
    })
  return config
}

// Tokens are tab-scoped (sessionStorage); sign-out is broadcast to other tabs
// through a non-secret localStorage key.
export function readTokens(): Tokens | null {
  try { return JSON.parse(sessionStorage.getItem(storageKeys.tokens) ?? 'null') } catch { return null }
}
export function saveTokens(tokens: Tokens) {
  sessionStorage.setItem(storageKeys.tokens, JSON.stringify(tokens))
  window.dispatchEvent(new Event('session-updated'))
}
let sessionGeneration = 0
export function clearTokens() { sessionGeneration++; sessionStorage.removeItem(storageKeys.tokens) }
export function endSession() { clearTokens(); localStorage.setItem(storageKeys.ended, randomString()) }

export function safeBack(back: string) {
  return back.startsWith('/') && !back.startsWith('//') && !back.includes('\\') && !back.startsWith('/auth/') ? back : '/'
}

export async function signIn(cfg: PublicConfig, back = '/') {
  const verifier = randomString(48), state = randomString(), nonce = randomString()
  const pending: Pending = { state, nonce, verifier, redirect: `${location.origin}/auth/callback`, client: cfg.client_id, iam: trim(cfg.iam_url), back: safeBack(back) }
  sessionStorage.setItem(storageKeys.pending, JSON.stringify(pending))
  const q = new URLSearchParams({
    client_id: pending.client,
    redirect_uri: pending.redirect,
    response_type: 'code',
    scope: 'openid profile email offline_access',
    state,
    nonce,
    code_challenge_method: 'S256',
    code_challenge: await challenge(verifier),
  })
  location.assign(`${pending.iam}/oauth/authorize?${q}`)
}

async function token(iam: string, form: Record<string, string>): Promise<Tokens> {
  const res = await fetch(`${iam}/oauth/token`, {
    method: 'POST',
    credentials: 'omit',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams(form),
  })
  const body = await res.json().catch(() => null)
  if (!res.ok || typeof body?.access_token !== 'string') throw new SignInError('No se pudo recuperar la sesión. Vuelve a ingresar.')
  return { access_token: body.access_token, refresh_token: body.refresh_token, id_token: body.id_token, expires_at: Date.now() + (Number(body.expires_in) || 300) * 1000 }
}

// React StrictMode mounts the callback twice: exchange each code only once.
const completing = new Map<string, Promise<string>>()
export function complete(params: URLSearchParams): Promise<string> {
  const key = params.get('state') ?? ''
  let running = completing.get(key)
  if (!running) { running = redeem(params); completing.set(key, running) }
  return running
}
async function redeem(params: URLSearchParams) {
  const raw = sessionStorage.getItem(storageKeys.pending)
  sessionStorage.removeItem(storageKeys.pending)
  if (params.get('error')) throw new SignInError('Se canceló el inicio de sesión. Vuelve a ingresar.')
  const p: Pending | null = raw ? JSON.parse(raw) : null
  if (!p || !params.get('state') || params.get('state') !== p.state || !params.get('code')) {
    throw new SignInError('Este enlace expiró o se abrió en otra pestaña. Vuelve a ingresar.')
  }
  const tokens = await token(p.iam, { grant_type: 'authorization_code', client_id: p.client, redirect_uri: p.redirect, code: params.get('code')!, code_verifier: p.verifier })
  if (!tokens.id_token || decode(tokens.id_token).nonce !== p.nonce) throw new SignInError('La respuesta no corresponde a esta solicitud. Vuelve a ingresar.')
  saveTokens(tokens)
  return safeBack(p.back)
}

let refreshing: Promise<Tokens | null> | null = null
export function refresh(cfg: PublicConfig): Promise<Tokens | null> {
  const current = readTokens()
  if (!current?.refresh_token) return Promise.resolve(null)
  const generation = sessionGeneration
  refreshing ??= token(trim(cfg.iam_url), { grant_type: 'refresh_token', client_id: cfg.client_id, refresh_token: current.refresh_token })
    .then(next => {
      // Sign-out (or another rotation) won the race: never resurrect the session.
      if (generation !== sessionGeneration || readTokens()?.refresh_token !== current.refresh_token) return null
      const merged = { ...next, id_token: next.id_token ?? current.id_token, refresh_token: next.refresh_token ?? current.refresh_token }
      saveTokens(merged)
      return merged
    })
    .catch(() => { if (generation === sessionGeneration) clearTokens(); return null })
    .finally(() => { refreshing = null })
  return refreshing
}

export async function accessToken(cfg: PublicConfig) {
  const tokens = readTokens()
  if (!tokens) return null
  return tokens.expires_at - Date.now() > 30_000 ? tokens.access_token : ((await refresh(cfg))?.access_token ?? null)
}

export interface UserInfo { name?: string; email?: string }
export async function userInfo(cfg: PublicConfig): Promise<UserInfo | null> {
  const access = await accessToken(cfg)
  if (!access) return null
  try {
    const res = await fetch(`${trim(cfg.iam_url)}/oauth/userinfo`, { credentials: 'omit', headers: { Authorization: `Bearer ${access}` } })
    if (!res.ok) return null
    const body = await res.json()
    return { name: typeof body.name === 'string' ? body.name : undefined, email: typeof body.email === 'string' ? body.email : undefined }
  } catch {
    return null
  }
}

export function signOut(cfg: PublicConfig) {
  const tokens = readTokens()
  endSession()
  const q = new URLSearchParams({ client_id: cfg.client_id, post_logout_redirect_uri: `${location.origin}/` })
  if (tokens?.id_token) q.set('id_token_hint', tokens.id_token)
  location.assign(`${trim(cfg.iam_url)}/oauth/end_session?${q}`)
}
