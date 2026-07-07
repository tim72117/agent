import { useState } from 'react'
import type { App } from './schema'
import { toLLMToolsJSON, toTypeScript, toYAML } from './codegen'

type Tab = 'yaml' | 'json' | 'ts'

const TABS: { id: Tab; label: string; hint: string }[] = [
  { id: 'yaml', label: 'YAML', hint: 'what Save writes to backend/tools/<appId>.yaml' },
  { id: 'json', label: 'LLM tool JSON', hint: 'shape returned by GET /apps/{appId}/tools.json' },
  { id: 'ts', label: 'TypeScript', hint: 'shape returned by GET /apps/{appId}/tools.ts' },
]

export function PreviewPanel({ app }: { app: App }) {
  const [tab, setTab] = useState<Tab>('yaml')
  const [copied, setCopied] = useState(false)

  const content = tab === 'yaml' ? toYAML(app) : tab === 'json' ? toLLMToolsJSON(app) : toTypeScript(app)

  async function copy() {
    await navigator.clipboard.writeText(content)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div className="preview-panel">
      <div className="preview-tabs">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            className={tab === t.id ? 'active' : undefined}
            onClick={() => setTab(t.id)}
          >
            {t.label}
          </button>
        ))}
        <button type="button" className="copy-btn" onClick={copy}>
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <div className="preview-hint">{TABS.find((t) => t.id === tab)!.hint}</div>
      <pre className="preview-code">
        <code>{content}</code>
      </pre>
    </div>
  )
}
