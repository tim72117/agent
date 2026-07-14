import { useCallback, useEffect, useMemo, useState } from 'react'
import type { App as AppSchema, Tool } from './schema'
import { DEFAULT_THOUGHT, emptyTool } from './schema'
import { api, ApiError } from './api'
import type { AppSummary, CurrentUser, IssuedKey } from './api'
import { Login } from './Login'
import { KeyModal } from './KeyModal'
import { AddAppModal } from './AddAppModal'
import { Sidebar } from './Sidebar'
import { ToolForm } from './ToolForm'
import { ThoughtEditor } from './ThoughtEditor'
import { Playground } from './Playground'
import { PreviewPanel } from './PreviewPanel'
import { validateApp } from './validate'
import { useToast } from './Toast'

type AuthState = 'checking' | 'anonymous' | 'authenticated'

export default function App() {
  // The session lives in an httpOnly cookie the backend sets — JS can't
  // read it directly, so on mount we ask the backend who (if anyone) it
  // belongs to instead of trusting any client-side flag.
  const [authState, setAuthState] = useState<AuthState>('checking')
  const [user, setUser] = useState<CurrentUser | null>(null)
  const [loginError, setLoginError] = useState<string | null>(null)
  const [summaries, setSummaries] = useState<AppSummary[] | null>(null)

  // draft is the full definition of the app being edited; edits stay local
  // until Save PUTs them to the backend, so half-finished schema changes
  // never go live on keystroke.
  const [draft, setDraft] = useState<AppSchema | null>(null)
  const [dirty, setDirty] = useState(false)
  const [activeToolIndex, setActiveToolIndex] = useState<number | null>(null)
  const [agentSelected, setAgentSelected] = useState(false)
  const [playgroundSelected, setPlaygroundSelected] = useState(false)
  const [issuedKey, setIssuedKey] = useState<IssuedKey | null>(null)
  const [showAddApp, setShowAddApp] = useState(false)
  const [busy, setBusy] = useState(false)
  // Origin edits save immediately on submit (unlike tool edits, which batch
  // into draft/dirty until Save) — it's a single field with its own PUT
  // endpoint, and there's no half-finished intermediate state worth
  // protecting against an accidental navigate-away.
  const [originDraft, setOriginDraft] = useState('')
  const [originBusy, setOriginBusy] = useState(false)
  // Thought edits follow the same immediate-save pattern as origin.
  const [thoughtDraft, setThoughtDraft] = useState('')
  const [thoughtBusy, setThoughtBusy] = useState(false)

  const logout = useCallback((message: string | null) => {
    setUser(null)
    setAuthState('anonymous')
    setSummaries(null)
    setDraft(null)
    setDirty(false)
    setActiveToolIndex(null)
    setAgentSelected(false)
    setPlaygroundSelected(false)
    setLoginError(message)
  }, [])

  const { showToast } = useToast()

  // Any API failure funnels through here: auth problems end the session,
  // everything else surfaces as a dismissible toast rather than a
  // blocking native alert() (which stalls the whole tab and can't be
  // styled or auto-dismissed).
  const reportError = useCallback(
    (err: unknown) => {
      if (err instanceof ApiError && err.status === 401) {
        logout('Your session expired. Sign in again.')
        return
      }
      showToast(err instanceof Error ? err.message : String(err), 'error')
    },
    [logout, showToast],
  )

  const refreshSummaries = useCallback(async () => {
    const list = await api.listApps()
    list.sort((a, b) => a.appId.localeCompare(b.appId))
    setSummaries(list)
  }, [])

  // Check for an existing session once on load.
  useEffect(() => {
    api
      .me()
      .then((u) => {
        setUser(u)
        setAuthState('authenticated')
      })
      .catch(() => setAuthState('anonymous'))
  }, [])

  useEffect(() => {
    if (authState !== 'authenticated') return
    refreshSummaries().catch((err) => {
      if (err instanceof ApiError && err.status === 401) {
        logout('Your session expired. Sign in again.')
      } else {
        reportError(err)
      }
    })
  }, [authState, refreshSummaries, logout, reportError])

  // Unsaved edits only live in this tab; warn before the browser discards them.
  useEffect(() => {
    if (!dirty) return
    const handler = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [dirty])

  const issues = useMemo(() => (draft ? validateApp(draft) : []), [draft])
  const issuesByTool = useMemo(() => {
    const m = new Map<number, typeof issues>()
    for (const issue of issues) {
      if (issue.toolIndex === null) continue
      if (!m.has(issue.toolIndex)) m.set(issue.toolIndex, [])
      m.get(issue.toolIndex)!.push(issue)
    }
    return m
  }, [issues])

  const activeSummary = summaries?.find((s) => s.appId === draft?.appId) ?? null

  // Keep the origin/thought inputs in sync with the server's value whenever
  // the selected app changes (including right after a save, via
  // refreshSummaries) — but not on every keystroke, since that would fight
  // the user typing.
  useEffect(() => {
    setOriginDraft(activeSummary?.allowedOrigin ?? '')
  }, [activeSummary?.appId, activeSummary?.allowedOrigin])

  useEffect(() => {
    setThoughtDraft(activeSummary?.thought ?? '')
  }, [activeSummary?.appId, activeSummary?.thought])

  function confirmDiscard(): boolean {
    return !dirty || confirm('Discard unsaved changes to this app?')
  }

  async function selectApp(appId: string) {
    if (!confirmDiscard()) return
    try {
      const app = await api.getApp(appId)
      setDraft({ appId: app.appId, tools: app.tools ?? [] })
      setDirty(false)
      setActiveToolIndex(null)
      setAgentSelected(false)
      setPlaygroundSelected(false)
    } catch (err) {
      reportError(err)
    }
  }

  function addApp() {
    if (!confirmDiscard()) return
    setShowAddApp(true)
  }

  async function createApp(appId: string) {
    try {
      await api.createApp(appId)
      await refreshSummaries()
      const app = await api.getApp(appId)
      setDraft({ appId: app.appId, tools: app.tools ?? [] })
      setDirty(false)
      setActiveToolIndex(null)
      setAgentSelected(false)
      setPlaygroundSelected(false)
      setShowAddApp(false)
    } catch (err) {
      reportError(err)
    }
  }

  async function deleteApp() {
    if (!draft) return
    if (!confirm(`Delete app "${draft.appId}" and its tools? Its API key is revoked too.`)) return
    try {
      await api.deleteApp(draft.appId)
      await refreshSummaries()
      setDraft(null)
      setDirty(false)
      setActiveToolIndex(null)
      setAgentSelected(false)
      setPlaygroundSelected(false)
    } catch (err) {
      reportError(err)
    }
  }

  async function saveDraft() {
    if (!draft) return
    setBusy(true)
    try {
      await api.saveTools(draft.appId, draft.tools)
      setDirty(false)
      await refreshSummaries()
    } catch (err) {
      reportError(err)
    } finally {
      setBusy(false)
    }
  }

  async function issueKey() {
    if (!draft) return
    if (
      activeSummary?.hasKey &&
      !confirm('This app already has a key. Issuing a new one revokes the old key immediately. Continue?')
    ) {
      return
    }
    try {
      const issued = await api.issueKey(draft.appId)
      setIssuedKey(issued)
      await refreshSummaries()
    } catch (err) {
      reportError(err)
    }
  }

  async function saveOrigin(e: React.FormEvent) {
    e.preventDefault()
    if (!draft) return
    setOriginBusy(true)
    try {
      await api.setOrigin(draft.appId, originDraft.trim())
      await refreshSummaries()
    } catch (err) {
      reportError(err)
    } finally {
      setOriginBusy(false)
    }
  }

  async function saveThought(e: React.FormEvent) {
    e.preventDefault()
    if (!draft) return
    setThoughtBusy(true)
    try {
      await api.setThought(draft.appId, thoughtDraft.trim())
      await refreshSummaries()
    } catch (err) {
      reportError(err)
    } finally {
      setThoughtBusy(false)
    }
  }

  async function revokeKey() {
    if (!draft) return
    if (!confirm(`Revoke the API key for "${draft.appId}"? Connected sites stop working immediately.`)) return
    try {
      await api.revokeKey(draft.appId)
      await refreshSummaries()
    } catch (err) {
      reportError(err)
    }
  }

  function updateDraft(next: AppSchema) {
    setDraft(next)
    setDirty(true)
  }

  function addTool() {
    if (!draft) return
    updateDraft({ ...draft, tools: [...draft.tools, emptyTool()] })
    setActiveToolIndex(draft.tools.length)
    setAgentSelected(false)
    setPlaygroundSelected(false)
  }

  function updateTool(index: number, next: Tool) {
    if (!draft) return
    const tools = draft.tools.slice()
    tools[index] = next
    updateDraft({ ...draft, tools })
  }

  function removeTool(index: number) {
    if (!draft) return
    updateDraft({ ...draft, tools: draft.tools.filter((_, i) => i !== index) })
    setActiveToolIndex(null)
  }

  function selectTool(index: number) {
    setActiveToolIndex(index)
    setAgentSelected(false)
    setPlaygroundSelected(false)
  }

  function selectAgent() {
    setActiveToolIndex(null)
    setAgentSelected(true)
    setPlaygroundSelected(false)
  }

  function selectPlayground() {
    setActiveToolIndex(null)
    setAgentSelected(false)
    setPlaygroundSelected(true)
  }

  async function doLogout() {
    try {
      await api.logout()
    } catch {
      // Cookie may already be gone server-side; clear local state regardless.
    }
    logout(null)
  }

  if (authState === 'checking') {
    return <div className="connecting">Loading…</div>
  }

  if (authState === 'anonymous' || !user) {
    return (
      <Login
        initialError={loginError}
        onSuccess={(u) => {
          setLoginError(null)
          setUser(u)
          setAuthState('authenticated')
        }}
      />
    )
  }

  if (!summaries) {
    return <div className="connecting">Connecting…</div>
  }

  const selectedTool = draft && activeToolIndex !== null ? draft.tools[activeToolIndex] : null
  const appLevelIssues = issues.filter((i) => i.toolIndex === null)
  const canSave = dirty && issues.length === 0 && !busy

  return (
    <div className="shell">
      <Sidebar
        userEmail={user.email}
        summaries={summaries}
        activeAppId={draft?.appId ?? null}
        onSelectApp={selectApp}
        onAddApp={addApp}
        tools={draft?.tools ?? null}
        activeToolIndex={activeToolIndex}
        agentSelected={agentSelected}
        playgroundSelected={playgroundSelected}
        issuesByTool={issuesByTool}
        onSelectTool={selectTool}
        onSelectAgent={selectAgent}
        onSelectPlayground={selectPlayground}
        onAddTool={addTool}
        onDeleteApp={deleteApp}
        onLogout={doLogout}
      />

      <main className="workspace">
        {draft ? (
          <>
            <header className="workspace-header">
              <div className="workspace-heading">
                <h1 className="appid-heading">{draft.appId}</h1>
                <span className="workspace-sub">
                  {draft.tools.length} {draft.tools.length === 1 ? 'tool' : 'tools'}
                </span>
                {activeSummary?.hasKey && <span className="badge">key issued</span>}
                {dirty && <span className="badge badge-dirty">unsaved</span>}
                <div className="workspace-actions">
                  <button type="button" className="text-btn" onClick={issueKey}>
                    {activeSummary?.hasKey ? 'Rotate key' : 'Issue key'}
                  </button>
                  {activeSummary?.hasKey && (
                    <button type="button" className="text-btn danger" onClick={revokeKey}>
                      Revoke key
                    </button>
                  )}
                  <button type="button" className="primary" onClick={saveDraft} disabled={!canSave}>
                    {busy ? 'Saving…' : 'Save'}
                  </button>
                </div>
              </div>
              <form className="origin-row" onSubmit={saveOrigin}>
                <span className="micro-label origin-label">Allowed origin</span>
                <input
                  className="origin-input"
                  placeholder="https://your-site.example.com"
                  value={originDraft}
                  onChange={(e) => setOriginDraft(e.target.value)}
                />
                <button
                  type="submit"
                  className="text-btn"
                  disabled={originBusy || originDraft.trim() === (activeSummary?.allowedOrigin ?? '')}
                >
                  {originBusy ? 'Saving…' : 'Save origin'}
                </button>
                {!activeSummary?.allowedOrigin && (
                  <span className="origin-warning">
                    No origin set — every connection for this app is blocked until one is saved.
                  </span>
                )}
              </form>

              {appLevelIssues.length > 0 && (
                <ul className="issue-list issue-list-inline">
                  {appLevelIssues.map((issue, i) => (
                    <li key={i}>{issue.message}</li>
                  ))}
                </ul>
              )}
            </header>

            {playgroundSelected ? (
              <div className="workspace-body workspace-body-single">
                <section className="editor-pane editor-pane-wide">
                  <Playground appId={draft.appId} />
                </section>
              </div>
            ) : (
              <div className="workspace-body">
                <section className="editor-pane">
                  {agentSelected ? (
                    <ThoughtEditor
                      value={thoughtDraft}
                      defaultPreview={DEFAULT_THOUGHT}
                      busy={thoughtBusy}
                      dirty={thoughtDraft.trim() !== (activeSummary?.thought ?? '')}
                      onChange={setThoughtDraft}
                      onSave={saveThought}
                    />
                  ) : selectedTool ? (
                    <ToolForm
                      key={activeToolIndex}
                      tool={selectedTool}
                      issues={issuesByTool.get(activeToolIndex!) ?? []}
                      onChange={(next) => updateTool(activeToolIndex!, next)}
                      onRemove={() => removeTool(activeToolIndex!)}
                    />
                  ) : (
                    <div className="empty-state">
                      <p className="empty-state-title">No tool selected</p>
                      <p className="empty-state-body">
                        Choose a tool from the sidebar, or add a new one to define its parameters.
                      </p>
                      <button type="button" className="primary" onClick={addTool}>
                        + New tool
                      </button>
                    </div>
                  )}
                </section>

                <section className="preview-pane">
                  <PreviewPanel app={draft} />
                </section>
              </div>
            )}
          </>
        ) : (
          <div className="empty-state workspace-empty">
            <p className="empty-state-title">No app selected</p>
            <p className="empty-state-body">
              Pick an app from the sidebar to edit its tools, or create a new one.
            </p>
            <button type="button" className="primary" onClick={addApp}>
              + New app
            </button>
          </div>
        )}
      </main>

      {issuedKey && <KeyModal issued={issuedKey} onClose={() => setIssuedKey(null)} />}
      {showAddApp && <AddAppModal onSubmit={createApp} onClose={() => setShowAddApp(false)} />}
    </div>
  )
}
