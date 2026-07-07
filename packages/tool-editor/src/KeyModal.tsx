import { useState } from 'react'
import type { IssuedKey } from './api'

// KeyModal shows a freshly-issued API key. The backend stores only the
// key's hash, so this modal is the single opportunity to copy the plaintext
// — closing it is irreversible (though issuing again replaces the key).
export function KeyModal({ issued, onClose }: { issued: IssuedKey; onClose: () => void }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    await navigator.clipboard.writeText(issued.apiKey)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="API key issued">
      <div className="modal">
        <h2 className="modal-title">API key for {issued.appId}</h2>
        <p className="modal-copy">
          Copy it now — the backend keeps only a hash, so this key is <strong>shown once</strong>{' '}
          and can't be recovered. Issuing a new key later replaces this one.
        </p>
        <div className="key-row">
          <code className="key-value">{issued.apiKey}</code>
          <button type="button" className="text-btn" onClick={copy}>
            {copied ? 'Copied' : 'Copy'}
          </button>
        </div>
        <p className="modal-hint">
          The site passes it to the SDK:{' '}
          <code>new AgentBridge({'{'} appId: "{issued.appId}", apiKey: "…" {'}'})</code>
        </p>
        <div className="modal-actions">
          <button type="button" className="primary" onClick={onClose}>
            I've saved it
          </button>
        </div>
      </div>
    </div>
  )
}
