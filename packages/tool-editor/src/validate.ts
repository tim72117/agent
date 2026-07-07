// Mirrors the validation in backend/internal/toolschema/loader.go's
// LoadFile, so a tool that passes here won't fail to load once pasted into
// backend/tools/*.yaml — this is meant to catch mistakes before the round
// trip through a real backend restart, not to be a superset or subset of
// the Go validation.

import type { App } from './schema'
import { TOOL_NAME_RE } from './schema'

export interface ValidationIssue {
  toolIndex: number | null // null = app-level issue (e.g. missing appId)
  message: string
}

export function validateApp(app: App): ValidationIssue[] {
  const issues: ValidationIssue[] = []

  if (!app.appId.trim()) {
    issues.push({ toolIndex: null, message: 'appId is required' })
  }

  const seen = new Set<string>()
  app.tools.forEach((tool, i) => {
    if (!TOOL_NAME_RE.test(tool.name)) {
      issues.push({
        toolIndex: i,
        message: `tool[${i}] has invalid name ${JSON.stringify(tool.name)} (must match ${TOOL_NAME_RE.source})`,
      })
    } else if (seen.has(tool.name)) {
      issues.push({ toolIndex: i, message: `duplicate tool name ${JSON.stringify(tool.name)}` })
    } else {
      seen.add(tool.name)
    }

    if (!tool.description.trim()) {
      issues.push({ toolIndex: i, message: `tool ${JSON.stringify(tool.name)} is missing a description` })
    }
    if (!tool.parameters.type) {
      issues.push({ toolIndex: i, message: `tool ${JSON.stringify(tool.name)} is missing parameters.type` })
    }
  })

  return issues
}
