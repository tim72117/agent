import type { Tool } from './schema'
import type { AppSummary, Quota } from './api'
import type { ValidationIssue } from './validate'

export function Sidebar({
  userEmail,
  quota,
  summaries,
  activeAppId,
  onSelectApp,
  onAddApp,
  tools,
  activeToolIndex,
  agentSelected,
  playgroundSelected,
  issuesByTool,
  onSelectTool,
  onSelectAgent,
  onSelectPlayground,
  onAddTool,
  onDeleteApp,
  onLogout,
}: {
  userEmail: string
  quota: Quota | null // null while loading, or if the fetch failed — rendered as nothing rather than a placeholder
  summaries: AppSummary[]
  activeAppId: string | null
  onSelectApp: (appId: string) => void
  onAddApp: () => void
  tools: Tool[] | null // null when no app is selected
  activeToolIndex: number | null
  agentSelected: boolean
  playgroundSelected: boolean
  issuesByTool: Map<number, ValidationIssue[]>
  onSelectTool: (index: number) => void
  onSelectAgent: () => void
  onSelectPlayground: () => void
  onAddTool: () => void
  onDeleteApp: () => void
  onLogout: () => void
}) {
  return (
    <nav className="sidebar">
      <div className="sidebar-brand">
        <span className="sidebar-mark" aria-hidden="true">
          ⌘
        </span>
        <span className="sidebar-brand-name">Console</span>
      </div>

      <div className="sidebar-section">
        <div className="sidebar-section-head">
          <span>Apps</span>
          <button type="button" className="sidebar-icon-btn" onClick={onAddApp} aria-label="New app">
            +
          </button>
        </div>
        <ul className="sidebar-list">
          {summaries.map((s) => (
            <li key={s.appId}>
              <button
                type="button"
                className={`sidebar-item${s.appId === activeAppId ? ' active' : ''}`}
                onClick={() => onSelectApp(s.appId)}
              >
                <span className="sidebar-item-label">{s.appId}</span>
                {s.hasKey && !s.allowedOrigin && (
                  <span className="status-dot error" title="Key issued but no origin set — all connections blocked" />
                )}
                {s.hasKey && s.allowedOrigin && (
                  <span className="status-dot ok" title={`Accepting connections from ${s.allowedOrigin}`} />
                )}
              </button>
            </li>
          ))}
        </ul>
        {summaries.length === 0 && <p className="sidebar-empty">No apps yet</p>}
      </div>

      {tools !== null && (
        <div className="sidebar-section">
          <div className="sidebar-section-head">
            <span>Agent</span>
          </div>
          <ul className="sidebar-list">
            <li>
              <button
                type="button"
                className={`sidebar-item${agentSelected ? ' active' : ''}`}
                onClick={onSelectAgent}
              >
                <span className="sidebar-item-label">Thought</span>
              </button>
            </li>
            <li>
              <button
                type="button"
                className={`sidebar-item${playgroundSelected ? ' active' : ''}`}
                onClick={onSelectPlayground}
              >
                <span className="sidebar-item-label">Playground</span>
              </button>
            </li>
          </ul>
        </div>
      )}

      {tools !== null && (
        <div className="sidebar-section sidebar-section-grow">
          <div className="sidebar-section-head">
            <span>Tools</span>
            <button type="button" className="sidebar-icon-btn" onClick={onAddTool} aria-label="New tool">
              +
            </button>
          </div>
          {tools.length === 0 ? (
            <p className="sidebar-empty">No tools yet</p>
          ) : (
            <ul className="sidebar-list">
              {tools.map((tool, i) => {
                const issueCount = issuesByTool.get(i)?.length ?? 0
                return (
                  <li key={i}>
                    <button
                      type="button"
                      className={`sidebar-item sidebar-item-tool${i === activeToolIndex ? ' active' : ''}`}
                      onClick={() => onSelectTool(i)}
                    >
                      <span className="sidebar-item-label">
                        {tool.name || <em>unnamed_tool</em>}
                      </span>
                      {issueCount > 0 && (
                        <span className="status-dot error" title={`${issueCount} issue(s)`} />
                      )}
                    </button>
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      )}

      <div className="sidebar-footer">
        {activeAppId && (
          <button type="button" className="sidebar-text-btn danger" onClick={onDeleteApp}>
            Delete "{activeAppId}"
          </button>
        )}
        {quota && (
          <div className="sidebar-quota" title={`Resets ${new Date(quota.periodEnd).toLocaleDateString()}`}>
            <span className="sidebar-quota-plan">{quota.planName} plan</span>
            <span className="sidebar-quota-usage">
              {quota.used} / {quota.limit} requests used this month
            </span>
          </div>
        )}
        <div className="sidebar-account">
          <span className="sidebar-account-email">{userEmail}</span>
          <button type="button" className="sidebar-text-btn" onClick={onLogout}>
            Sign out
          </button>
        </div>
        <p className="sidebar-hint">
          Each app's API key is the credential its site passes to <code>AgentBridge</code>.
        </p>
      </div>
    </nav>
  )
}
