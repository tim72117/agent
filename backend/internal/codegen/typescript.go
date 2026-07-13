package codegen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tim72117/agent-tool-platform/internal/toolschema"
)

// TypeScript renders a .ts source file for one app: a `ToolHandlers`
// interface (one method per tool, argument/return types generated from the
// schema) plus a `ToolName` union type. Developers implement this interface
// and pass it to the Agent Bridge SDK, so calls arriving over the WebSocket
// are type-checked against their own tool definitions.
func TypeScript(app *toolschema.App) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "// Code generated from tools/%s.yaml by onagent codegen. DO NOT EDIT.\n\n", app.AppID)

	names := make([]string, 0, len(app.Tools))
	for _, t := range app.Tools {
		names = append(names, t.Name)
	}

	if len(names) > 0 {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		fmt.Fprintf(&b, "export type ToolName = %s;\n\n", strings.Join(quoted, " | "))
	} else {
		b.WriteString("export type ToolName = never;\n\n")
	}

	for _, t := range app.Tools {
		argsType := pascalCase(t.Name) + "Args"
		if err := writeInterface(&b, argsType, &t.Parameters); err != nil {
			return "", fmt.Errorf("codegen: tool %q: %w", t.Name, err)
		}
		b.WriteString("\n")

		if t.Returns != nil {
			retType := pascalCase(t.Name) + "Result"
			if err := writeInterface(&b, retType, t.Returns); err != nil {
				return "", fmt.Errorf("codegen: tool %q returns: %w", t.Name, err)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("export interface ToolHandlers {\n")
	for _, t := range app.Tools {
		argsType := pascalCase(t.Name) + "Args"
		retType := "void | Record<string, unknown>"
		if t.Returns != nil {
			retType = pascalCase(t.Name) + "Result"
		}
		fmt.Fprintf(&b, "  /** %s */\n", strings.ReplaceAll(t.Description, "\n", " "))
		fmt.Fprintf(&b, "  %s(args: %s): Promise<%s> | %s;\n", t.Name, argsType, retType, retType)
	}
	b.WriteString("}\n")

	return b.String(), nil
}

func writeInterface(b *strings.Builder, typeName string, s *toolschema.ParameterSchema) error {
	if s.Type != "object" {
		return fmt.Errorf("top-level schema for %s must be type=object, got %q", typeName, s.Type)
	}

	fmt.Fprintf(b, "export interface %s {\n", typeName)

	propNames := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)

	required := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		required[r] = true
	}

	for _, name := range propNames {
		prop := s.Properties[name]
		optional := ""
		if !required[name] {
			optional = "?"
		}
		if prop.Description != "" {
			fmt.Fprintf(b, "  /** %s */\n", strings.ReplaceAll(prop.Description, "\n", " "))
		}
		fmt.Fprintf(b, "  %s%s: %s;\n", name, optional, tsType(prop))
	}

	b.WriteString("}\n")
	return nil
}

func tsType(s *toolschema.ParameterSchema) string {
	if len(s.Enum) > 0 {
		quoted := make([]string, len(s.Enum))
		for i, v := range s.Enum {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		return strings.Join(quoted, " | ")
	}

	switch s.Type {
	case "string":
		return "string"
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		if s.Items != nil {
			return tsType(s.Items) + "[]"
		}
		return "unknown[]"
	case "object":
		if len(s.Properties) == 0 {
			return "Record<string, unknown>"
		}
		var b strings.Builder
		b.WriteString("{ ")
		names := make([]string, 0, len(s.Properties))
		for name := range s.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		required := make(map[string]bool, len(s.Required))
		for _, r := range s.Required {
			required[r] = true
		}
		for _, name := range names {
			optional := ""
			if !required[name] {
				optional = "?"
			}
			fmt.Fprintf(&b, "%s%s: %s; ", name, optional, tsType(s.Properties[name]))
		}
		b.WriteString("}")
		return b.String()
	default:
		return "unknown"
	}
}

func pascalCase(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}
