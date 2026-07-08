// Package toolschema defines the developer-facing tool definition format
// and loads it from YAML files under backend/tools/.
//
// A Tool describes one capability the front-end exposes to the LLM: its
// name, a JSON-Schema-style parameter definition (for the inference
// service), and metadata used to generate a matching TypeScript handler
// stub for the Agent Bridge SDK.
package toolschema

// Tool is one developer-defined capability exposed to the LLM.
type Tool struct {
	// Name is the tool's identifier as seen by the LLM. Must be unique
	// within an app and match ^[a-zA-Z_][a-zA-Z0-9_]*$.
	Name string `yaml:"name" json:"name"`

	// Description explains to the LLM when/why to call this tool.
	Description string `yaml:"description" json:"description"`

	// Parameters is a JSON-Schema "object" definition of the tool's
	// arguments, in the same shape OpenAI/Anthropic tool calling expects.
	Parameters ParameterSchema `yaml:"parameters" json:"parameters"`

	// Returns optionally documents the shape of the tool_result payload
	// the front-end sends back after executing this tool. It is not sent
	// to the LLM as part of the tool schema, but is used for TS codegen.
	Returns *ParameterSchema `yaml:"returns,omitempty" json:"returns,omitempty"`
}

// ParameterSchema is a (deliberately small) subset of JSON Schema, enough to
// describe LLM tool parameters and generate matching TypeScript types.
type ParameterSchema struct {
	Type        string                      `yaml:"type" json:"type"`
	Description string                      `yaml:"description,omitempty" json:"description,omitempty"`
	Properties  map[string]*ParameterSchema `yaml:"properties,omitempty" json:"properties,omitempty"`
	Items       *ParameterSchema            `yaml:"items,omitempty" json:"items,omitempty"`
	Required    []string                    `yaml:"required,omitempty" json:"required,omitempty"`
	Enum        []string                    `yaml:"enum,omitempty" json:"enum,omitempty"`
}

// App groups the tools that belong to one developer application (one
// AppID), loaded from a single YAML file.
type App struct {
	AppID string `yaml:"appId" json:"appId"`
	Tools []Tool `yaml:"tools" json:"tools"`

	// Thought is this app's custom instructions for the want agent that
	// selects its tools — tone, domain knowledge, or app-specific rules a
	// developer wants the LLM to follow beyond "call the matching tool."
	// Empty means the platform default applies (see
	// internal/inference/want_tools.go's defaultThought). Not part of the
	// LLM tool schema itself (codegen.ToLLMTools doesn't touch it) — it
	// only affects the want agent role's system prompt.
	Thought string `yaml:"thought,omitempty" json:"thought,omitempty"`
}
