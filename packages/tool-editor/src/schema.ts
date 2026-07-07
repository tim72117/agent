// Mirrors backend/internal/toolschema/schema.go field-for-field, so the
// YAML this editor produces round-trips through toolschema.LoadFile without
// translation. Keep the two in sync by hand — there is no shared source of
// truth between the Go and TS type definitions.

export type ParamType = 'string' | 'number' | 'integer' | 'boolean' | 'array' | 'object'

export interface ParameterSchema {
  type: ParamType
  description?: string
  properties?: Record<string, ParameterSchema>
  items?: ParameterSchema
  required?: string[]
  enum?: string[]
}

export interface Tool {
  name: string
  description: string
  parameters: ParameterSchema
  returns?: ParameterSchema
}

export interface App {
  appId: string
  tools: Tool[]
}

// Same regexp as toolschema/loader.go's nameRE.
export const TOOL_NAME_RE = /^[a-zA-Z_][a-zA-Z0-9_]*$/

export function emptyObjectSchema(): ParameterSchema {
  return { type: 'object', properties: {}, required: [] }
}

export function emptyTool(): Tool {
  return {
    name: '',
    description: '',
    parameters: emptyObjectSchema(),
  }
}

