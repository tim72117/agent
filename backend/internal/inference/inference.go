// Package inference defines the boundary between this platform and the
// actual LLM inference/reasoning service. A real implementation will be
// plugged in later; for now MockService lets the rest of the system
// (WebSocket hub, SDK, demo app) be built and tested end-to-end.
package inference

import (
	"context"
	"encoding/json"

	"github.com/tim72117/agent-tool-platform/internal/codegen"
)

// ToolCall is one tool invocation the inference service wants the front-end
// to execute.
type ToolCall struct {
	ToolName string
	Args     json.RawMessage
}

// Result is what the inference service produces in response to a prompt:
// zero or more tool calls to run on the front-end, plus an optional
// natural-language message to show the user.
type Result struct {
	ToolCalls        []ToolCall
	AssistantMessage string
}

// Request bundles everything the inference service needs to reason about
// one prompt: the user's text, arbitrary front-end context/state, and the
// tool set available for this session's app.
type Request struct {
	Prompt  string
	Context json.RawMessage
	Tools   []codegen.LLMTool

	// SessionID identifies the end-user connection this prompt belongs to
	// (the WebSocket session id). Implementations use it to isolate
	// conversation state between users: two prompts share LLM conversation
	// history if and only if they carry the same SessionID. Empty means "no
	// isolation requested" (single-caller/dev use).
	SessionID string
}

// Service is the boundary this platform depends on. Swap MockService for a
// real client (HTTP/gRPC to the actual inference backend) without touching
// the WebSocket hub or SDK.
type Service interface {
	Complete(ctx context.Context, req Request) (*Result, error)
}
