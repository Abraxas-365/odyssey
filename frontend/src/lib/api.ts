import { accessToken, loadConfig, refresh } from './session'

// docs/api.md §3: clients branch on `type` (closed set) and `code`; `message`
// is for humans and is never parsed.
export type ErrorType = 'VALIDATION' | 'AUTHORIZATION' | 'NOT_FOUND' | 'CONFLICT' | 'BUSINESS' | 'INTERNAL' | 'EXTERNAL' | 'NETWORK'

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly type: ErrorType = 'INTERNAL',
    readonly code = '',
    readonly details: Record<string, unknown> = {},
    readonly requestId?: string,
  ) {
    super(message)
  }
  get field(): string | undefined {
    return typeof this.details.field === 'string' ? this.details.field : undefined
  }
}

export function params(values: Record<string, string | number | boolean | undefined | null>) {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(values)) if (v !== undefined && v !== null && v !== '') q.set(k, String(v))
  return q.size ? `?${q}` : ''
}

interface RequestOptions { method?: string; body?: unknown; signal?: AbortSignal }

async function request(path: string, options: RequestOptions = {}): Promise<Response> {
  const cfg = await loadConfig()
  const body = options.body === undefined ? undefined : JSON.stringify(options.body)
  let token = await accessToken(cfg)
  if (!token) {
    window.dispatchEvent(new Event('session-expired'))
    throw new ApiError('Tu sesión expiró. Vuelve a ingresar.', 401, 'AUTHORIZATION', 'UNAUTHORIZED')
  }
  const send = () =>
    fetch(`/api/v1${path}`, {
      method: options.method ?? 'GET',
      body,
      signal: options.signal,
      credentials: 'omit',
      headers: { Authorization: `Bearer ${token}`, Accept: 'application/json', ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
    })
  let res: Response
  try {
    res = await send()
    // 401 is rejected before any handler runs, so one retry with a fresh token
    // is safe even for writes. Never retry other statuses or network errors.
    if (res.status === 401) {
      token = (await refresh(cfg))?.access_token ?? null
      if (token) res = await send()
    }
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e
    throw new ApiError('No hay conexión con Odyssey. Revisa tu red e intenta de nuevo.', 0, 'NETWORK')
  }
  if (res.status === 401) window.dispatchEvent(new Event('session-expired'))
  if (!res.ok) {
    const data = await res.json().catch(() => null)
    const e = data?.error
    throw new ApiError(e?.message ?? 'No se pudo completar la operación.', res.status, e?.type ?? 'INTERNAL', e?.code ?? '', e?.details ?? {}, e?.request_id)
  }
  return res
}

export async function api<T>(path: string, options?: RequestOptions): Promise<T> {
  const res = await request(path, options)
  return res.status === 204 ? (undefined as T) : (res.json() as Promise<T>)
}

// Plain-Spanish messages per error code (UX v3 §4: consequences, not codes).
const messages: Record<string, string> = {
  PROJECT_ARCHIVED: 'Esta app está archivada. Restáurala antes de hacer cambios.',
  FORBIDDEN: 'No tienes permiso para hacer esto. Si lo necesitas, pídeselo a TI.',
  UNAUTHORIZED: 'Tu sesión expiró. Vuelve a ingresar.',
  PAYLOAD_TOO_LARGE: 'Lo que enviaste es demasiado grande.',
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (messages[error.code]) return messages[error.code]
    switch (error.type) {
      case 'NOT_FOUND': return 'No encontramos lo que buscas. Puede que se haya borrado o que no tengas acceso.'
      case 'CONFLICT': return error.field === 'slug' ? 'Ya existe una app con esa dirección. Elige otra.' : 'Alguien cambió esto al mismo tiempo. Recarga e intenta de nuevo.'
      case 'INTERNAL': return 'Algo falló de nuestro lado. Intenta de nuevo; si sigue pasando, avísale a TI.'
      case 'EXTERNAL': return 'Un servicio del que dependemos no responde. Intenta de nuevo en unos minutos.'
      default: return error.message
    }
  }
  return error instanceof Error ? error.message : 'Ocurrió un error. Intenta de nuevo.'
}
