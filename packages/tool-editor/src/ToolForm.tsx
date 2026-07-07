import type { Tool } from './schema'
import { emptyObjectSchema } from './schema'
import { SchemaEditor } from './SchemaEditor'
import type { ValidationIssue } from './validate'

export function ToolForm({
  tool,
  issues,
  onChange,
  onRemove,
}: {
  tool: Tool
  issues: ValidationIssue[]
  onChange: (next: Tool) => void
  onRemove: () => void
}) {
  return (
    <div className="tool-form">
      <div className="tool-form-header">
        <div className="tool-name-field">
          <label className="micro-label" htmlFor="tool-name">
            Name
          </label>
          <input
            id="tool-name"
            className="tool-name-input"
            placeholder="tool_name"
            value={tool.name}
            onChange={(e) => onChange({ ...tool, name: e.target.value })}
          />
        </div>
        <button type="button" className="text-btn danger" onClick={onRemove}>
          Delete tool
        </button>
      </div>

      {issues.length > 0 && (
        <ul className="issue-list">
          {issues.map((issue, i) => (
            <li key={i}>{issue.message}</li>
          ))}
        </ul>
      )}

      <label className="field">
        <span className="micro-label">Description</span>
        <textarea
          rows={2}
          placeholder="What does this tool do, and when should the model call it?"
          value={tool.description}
          onChange={(e) => onChange({ ...tool, description: e.target.value })}
        />
      </label>

      <div className="field">
        <span className="micro-label">Parameters</span>
        <SchemaEditor
          schema={tool.parameters}
          onChange={(next) => onChange({ ...tool, parameters: next })}
        />
      </div>

      <div className="field">
        <label className="checkbox-row">
          <input
            type="checkbox"
            checked={!!tool.returns}
            onChange={(e) =>
              onChange({ ...tool, returns: e.target.checked ? emptyObjectSchema() : undefined })
            }
          />
          Declare a returns shape (for TypeScript codegen)
        </label>
        {tool.returns && (
          <SchemaEditor
            schema={tool.returns}
            onChange={(next) => onChange({ ...tool, returns: next })}
          />
        )}
      </div>
    </div>
  )
}
