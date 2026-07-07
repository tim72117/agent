// Preview generators mirroring backend/internal/codegen/{llmschema,typescript}.go
// and the YAML shape toolschema.LoadFile expects. These are read-only
// previews for the developer to copy into backend/tools/<app>.yaml — this
// package does not write to the backend.

import { stringify as yamlStringify } from 'yaml'
import type { App, ParameterSchema, Tool } from './schema'

// --- YAML (toolschema.LoadFile input shape) --------------------------------

export function toYAML(app: App): string {
  // yaml.stringify keeps key order as inserted; build plain objects in the
  // same field order toolschema.Tool declares so generated files read the
  // same way as the hand-written examples in backend/tools/.
  const plain = {
    appId: app.appId,
    tools: app.tools.map(toolToPlain),
  }
  return yamlStringify(plain, { indent: 2 })
}

function toolToPlain(t: Tool) {
  const out: Record<string, unknown> = {
    name: t.name,
    description: t.description,
    parameters: schemaToPlain(t.parameters),
  }
  if (t.returns) out.returns = schemaToPlain(t.returns)
  return out
}

function schemaToPlain(s: ParameterSchema): Record<string, unknown> {
  const out: Record<string, unknown> = { type: s.type }
  if (s.description) out.description = s.description
  if (s.properties && Object.keys(s.properties).length > 0) {
    out.properties = Object.fromEntries(
      Object.entries(s.properties).map(([k, v]) => [k, schemaToPlain(v)]),
    )
  }
  if (s.items) out.items = schemaToPlain(s.items)
  if (s.required && s.required.length > 0) out.required = s.required
  if (s.enum && s.enum.length > 0) out.enum = s.enum
  return out
}

// --- LLM tool JSON (codegen.ToLLMTools output shape) ------------------------

export interface LLMTool {
  name: string
  description: string
  input_schema: ParameterSchema
}

export function toLLMTools(app: App): LLMTool[] {
  return app.tools.map((t) => ({
    name: t.name,
    description: t.description,
    input_schema: t.parameters,
  }))
}

export function toLLMToolsJSON(app: App): string {
  return JSON.stringify(toLLMTools(app), null, 2)
}

// --- TypeScript (codegen.TypeScript output, ported line-for-line) ----------

export function toTypeScript(app: App): string {
  const lines: string[] = []
  lines.push(`// Preview generated from the tool-editor. DO NOT EDIT.`)
  lines.push('')

  const names = app.tools.map((t) => t.name)
  if (names.length > 0) {
    lines.push(`export type ToolName = ${names.map((n) => JSON.stringify(n)).join(' | ')};`)
  } else {
    lines.push('export type ToolName = never;')
  }
  lines.push('')

  for (const t of app.tools) {
    const argsType = pascalCase(t.name) + 'Args'
    lines.push(writeInterface(argsType, t.parameters))
    lines.push('')
    if (t.returns) {
      const retType = pascalCase(t.name) + 'Result'
      lines.push(writeInterface(retType, t.returns))
      lines.push('')
    }
  }

  lines.push('export interface ToolHandlers {')
  for (const t of app.tools) {
    const argsType = pascalCase(t.name) + 'Args'
    const retType = t.returns ? pascalCase(t.name) + 'Result' : 'void | Record<string, unknown>'
    if (t.description) {
      lines.push(`  /** ${t.description.replace(/\n/g, ' ')} */`)
    }
    lines.push(`  ${t.name}(args: ${argsType}): Promise<${retType}> | ${retType};`)
  }
  lines.push('}')

  return lines.join('\n') + '\n'
}

function writeInterface(typeName: string, s: ParameterSchema): string {
  if (s.type !== 'object') {
    return `// error: top-level schema for ${typeName} must be type=object, got ${JSON.stringify(s.type)}`
  }

  const lines: string[] = [`export interface ${typeName} {`]
  const propNames = Object.keys(s.properties ?? {}).sort()
  const required = new Set(s.required ?? [])

  for (const name of propNames) {
    const prop = s.properties![name]
    const optional = required.has(name) ? '' : '?'
    if (prop.description) {
      lines.push(`  /** ${prop.description.replace(/\n/g, ' ')} */`)
    }
    lines.push(`  ${name}${optional}: ${tsType(prop)};`)
  }

  lines.push('}')
  return lines.join('\n')
}

function tsType(s: ParameterSchema): string {
  if (s.enum && s.enum.length > 0) {
    return s.enum.map((v) => JSON.stringify(v)).join(' | ')
  }

  switch (s.type) {
    case 'string':
      return 'string'
    case 'number':
    case 'integer':
      return 'number'
    case 'boolean':
      return 'boolean'
    case 'array':
      return s.items ? `${tsType(s.items)}[]` : 'unknown[]'
    case 'object': {
      const props = s.properties ?? {}
      const names = Object.keys(props).sort()
      if (names.length === 0) return 'Record<string, unknown>'
      const required = new Set(s.required ?? [])
      const parts = names.map((name) => {
        const optional = required.has(name) ? '' : '?'
        return `${name}${optional}: ${tsType(props[name])}; `
      })
      return `{ ${parts.join('')}}`
    }
    default:
      return 'unknown'
  }
}

function pascalCase(s: string): string {
  return s
    .split(/[_-]/)
    .filter(Boolean)
    .map((p) => p[0].toUpperCase() + p.slice(1))
    .join('')
}
