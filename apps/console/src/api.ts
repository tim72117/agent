// Client for the backend's /console and /auth APIs (backend/internal/console).
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
  /** Custom want agent system prompt for this app. "" means the platform
   * default applies. */
  thought: string
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

// Mirrors backend/internal/console.quotaResponse, itself named to match the
// admin back-office's per-user shape (quota.UserSummary) — tier/limit/used
// mean the same thing on both surfaces, just scoped to "me" here instead of
// an admin-chosen userId.
export interface Quota {
  tier: string
  planName: string
  limit: number
  used: number
  periodStart: string // RFC 3339
  periodEnd: string // RFC 3339
}

// Exported so Playground.tsx can derive the playground WebSocket's URL
// from the same source of truth rather than duplicating the env var lookup
// (that's also why this falls back to window.location.origin rather than
// a relative "" — Playground.tsx does BASE.replace(/^http/, 'ws') to build
// an absolute ws(s):// URL, which needs a real origin to replace from).
// When VITE_CONSOLE_API_URL isn't set at build time (the Docker/production
// build has no env file providing it — only .env.local does, for local dev
// against a separately-running backend on a different port), this resolves
// against whatever origin actually served the page. Console is embedded
// same-origin in production (backend/cmd/server/web.go), so that's correct
// there; a hardcoded localhost fallback baked into the production bundle
// was also triggering the browser's Local Network Access permission
// prompt, since a public page reaching for localhost looks identical to
// an attack from the browser's perspective.
export const BASE: string = import.meta.env.VITE_CONSOLE_API_URL ?? window.location.origin

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

  getQuota: (): Promise<Quota> => request('GET', '/console/quota').then((r) => r.json()),

  listApps: (): Promise<AppSummary[]> => request('GET', '/console/apps').then((r) => r.json()),

  getApp: (appId: string): Promise<App> => request('GET', `/console/apps/${id(appId)}`).then((r) => r.json()),

  createApp: (appId: string): Promise<AppSummary> =>
    request('POST', '/console/apps', { appId }).then((r) => r.json()),

  saveTools: (appId: string, tools: Tool[]): Promise<AppSummary> =>
    request('PUT', `/console/apps/${id(appId)}/tools`, tools).then((r) => r.json()),

  setOrigin: (appId: string, origin: string): Promise<AppSummary> =>
    request('PUT', `/console/apps/${id(appId)}/origin`, { origin }).then((r) => r.json()),

  setThought: (appId: string, thought: string): Promise<AppSummary> =>
    request('PUT', `/console/apps/${id(appId)}/thought`, { thought }).then((r) => r.json()),

  deleteApp: (appId: string): Promise<void> =>
    request('DELETE', `/console/apps/${id(appId)}`).then(() => undefined),

  issueKey: (appId: string): Promise<IssuedKey> =>
    request('POST', `/console/apps/${id(appId)}/key`).then((r) => r.json()),

  revokeKey: (appId: string): Promise<void> =>
    request('DELETE', `/console/apps/${id(appId)}/key`).then(() => undefined),

  // The CLI's own -console page only ever carries an opaque session id
  // (see backend/internal/cliauth) — these two calls resolve it to a
  // display name and, on approval, to where the browser should go next.
  // There's no client-side redirect validation here anymore: the actual
  // destination was registered server-side when the CLI called
  // POST /console/cli-auth/start, before this page ever existed.
  getCliAuthName: (sessionId: string): Promise<{ name: string }> =>
    request('GET', `/console/cli-auth/${id(sessionId)}`).then((r) => r.json()),

  approveCliAuth: (sessionId: string): Promise<{ redirectUri: string }> =>
    request('POST', `/console/cli-auth/${id(sessionId)}/approve`).then((r) => r.json()),
}
