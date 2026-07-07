// Client for the backend's /admin and /auth APIs (backend/internal/admin).
// Auth is a session cookie (backend/internal/session), not a bearer token —
// every call sends credentials: 'include' so the browser attaches it, and
// the backend's CORS layer (main.go's withCORS) echoes back the request
// Origin (required for a browser to accept a credentialed cross-origin
// response — see that function's comment for why "*" doesn't work here).
// A 401 means the session is missing/expired; callers should drop back to
// the login screen rather than retry.

import type { App, Tool } from './schema'

export interface AppSummary {
  appId: string
  toolCount: number
  hasKey: boolean
  /** Exact Origin header this app's connections must present. "" means
   * unset — the backend rejects every WebSocket connection for this app
   * until it's set (fail-closed; see backend's ws.Handler.ServeHTTP). */
  allowedOrigin: string
}

export interface IssuedKey {
  appId: string
  /** Plaintext key — the backend stores only its hash, so this is the one
   * and only chance to read it. */
  apiKey: string
}

export interface CurrentUser {
  email: string
}

const BASE: string = import.meta.env.VITE_ADMIN_API_URL ?? 'http://localhost:8080'

export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request(method: string, path: string, body?: unknown): Promise<Response> {
  let res: Response
  try {
    res = await fetch(`${BASE}${path}`, {
      method,
      credentials: 'include',
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
  } catch {
    throw new ApiError(0, `Cannot reach the backend at ${BASE}. Is it running?`)
  }
  if (!res.ok) {
    const text = (await res.text()).trim()
    throw new ApiError(res.status, text || res.statusText)
  }
  return res
}

const id = encodeURIComponent

export const api = {
  register: (email: string, password: string): Promise<CurrentUser> =>
    request('POST', '/auth/register', { email, password }).then((r) => r.json()),

  login: (email: string, password: string): Promise<CurrentUser> =>
    request('POST', '/auth/login', { email, password }).then((r) => r.json()),

  logout: (): Promise<void> => request('POST', '/auth/logout').then(() => undefined),

  me: (): Promise<CurrentUser> => request('GET', '/auth/me').then((r) => r.json()),

  listApps: (): Promise<AppSummary[]> => request('GET', '/admin/apps').then((r) => r.json()),

  getApp: (appId: string): Promise<App> => request('GET', `/admin/apps/${id(appId)}`).then((r) => r.json()),

  createApp: (appId: string): Promise<AppSummary> =>
    request('POST', '/admin/apps', { appId }).then((r) => r.json()),

  saveTools: (appId: string, tools: Tool[]): Promise<AppSummary> =>
    request('PUT', `/admin/apps/${id(appId)}/tools`, tools).then((r) => r.json()),

  setOrigin: (appId: string, origin: string): Promise<AppSummary> =>
    request('PUT', `/admin/apps/${id(appId)}/origin`, { origin }).then((r) => r.json()),

  deleteApp: (appId: string): Promise<void> =>
    request('DELETE', `/admin/apps/${id(appId)}`).then(() => undefined),

  issueKey: (appId: string): Promise<IssuedKey> =>
    request('POST', `/admin/apps/${id(appId)}/key`).then((r) => r.json()),

  revokeKey: (appId: string): Promise<void> =>
    request('DELETE', `/admin/apps/${id(appId)}/key`).then(() => undefined),
}
