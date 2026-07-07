import { useState } from 'react'
import { api, ApiError } from './api'
import type { CurrentUser } from './api'
import { offerToSavePassword } from './credentials'

type Mode = 'login' | 'register'

// Login gates the editor behind a real account (backend/internal/session):
// email + password, either signing in to an existing one or registering a
// new one. Success sets a session cookie the browser attaches automatically
// on subsequent requests — there's no token for this component to hand
// back, just the confirmed user.
export function Login({
  initialError,
  onSuccess,
}: {
  initialError: string | null
  onSuccess: (user: CurrentUser) => void
}) {
  const [mode, setMode] = useState<Mode>('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(initialError)
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!email.trim() || !password) return
    setBusy(true)
    setError(null)
    try {
      const user = mode === 'login' ? await api.login(email, password) : await api.register(email, password)
      // Fire-and-forget: onSuccess should proceed immediately either way,
      // this is a best-effort nudge to the browser's password manager.
      void offerToSavePassword(email, password)
      onSuccess(user)
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-screen">
      <form className="login-card" onSubmit={submit} id="login-form" name="login-form">
        <div className="sidebar-brand login-brand">
          <span className="sidebar-mark" aria-hidden="true">
            ⌘
          </span>
          <span className="sidebar-brand-name">Tool Editor</span>
        </div>
        <p className="login-copy">
          {mode === 'login'
            ? 'Sign in to manage your apps, tools, and API keys.'
            : 'Create an account to start defining apps and tools.'}
        </p>
        <label className="field">
          <span className="micro-label">Email</span>
          <input
            type="email"
            name="email"
            id="login-email"
            autoFocus
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@example.com"
          />
        </label>
        <label className="field">
          <span className="micro-label">Password</span>
          <input
            type="password"
            name="password"
            id="login-password"
            autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={mode === 'register' ? 'At least 8 characters' : '••••••••'}
          />
        </label>
        {error && <p className="login-error">{error}</p>}
        <button type="submit" className="primary login-submit" disabled={busy || !email.trim() || !password}>
          {busy ? 'Please wait…' : mode === 'login' ? 'Sign in' : 'Create account'}
        </button>
        <button
          type="button"
          className="login-switch"
          onClick={() => {
            setMode(mode === 'login' ? 'register' : 'login')
            setError(null)
          }}
        >
          {mode === 'login' ? "Don't have an account? Create one" : 'Already have an account? Sign in'}
        </button>
      </form>
    </div>
  )
}
