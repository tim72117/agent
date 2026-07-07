package inference

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/tim72117/agent-tool-platform/internal/toolschema"
	"github.com/tim72117/want/pkg/agentreg"
	"github.com/tim72117/want/types"
)

// platformAgentRole is the want agent role registered for this platform.
// No backend/.agents/<role>.md file exists, so AgentLoader always falls
// back to this Go-defined built-in (see want internal/loader.go: disk
// definitions only take priority when the file is actually present).
const platformAgentRole = "platform-tools"

// forwardedCall is one tool invocation the want LLM decided to make against
// a platform-defined (front-end-executed) tool during a single Complete()
// run.
type forwardedCall struct {
	ToolName string
	Args     json.RawMessage
}

// callSink collects forwardedCalls for the one in-flight Complete() call.
// WantService.Complete holds callSinkMu for its entire duration (want's
// orchestrator processes one submission at a time regardless), so a single
// package-level slice is safe: reset at the start of each call, drained at
// the end.
var (
	callSinkMu sync.Mutex
	callSink   []forwardedCall
)

func resetCallSink() {
	callSinkMu.Lock()
	callSink = nil
	callSinkMu.Unlock()
}

func drainCallSink() []forwardedCall {
	callSinkMu.Lock()
	defer callSinkMu.Unlock()
	out := callSink
	callSink = nil
	return out
}

func addToCallSink(name string, args json.RawMessage) {
	callSinkMu.Lock()
	callSink = append(callSink, forwardedCall{ToolName: name, Args: args})
	callSinkMu.Unlock()
}

// RegisterPlatformTools makes every tool declared across all loaded apps
// selectable by the want LLM, and registers a "platform-tools" want agent
// role whose tool whitelist contains exactly those names — deliberately
// excluding want's own built-in tools (Bash, Browser, Edit, ...), which
// would otherwise execute for real on the backend instead of being handed
// to the front-end to run in the DOM.
//
// Must be called once at startup, before the first WantService.Complete
// call, since want's tool registry (types.GlobalRegistry) and agent loader
// are process-global.
//
// Tool names are only guaranteed unique within a single app's YAML file
// (see toolschema.LoadFile); if two apps declare the same tool name, the
// second registration below wins silently. Fine for today's small tool
// count, but worth a cross-app uniqueness check if the tool surface grows.
func RegisterPlatformTools(apps map[string]*toolschema.App) {
	var toolNames []string
	for _, app := range apps {
		for _, t := range app.Tools {
			toolNames = append(toolNames, t.Name)
			registerForwardingTool(t)
		}
	}

	agentreg.Register(agentreg.DefaultLoader(), platformAgentRole, &agentreg.AgentDefinition{
		Role:  platformAgentRole,
		Tools: toolNames,
		WhenToUse: "Selects and fills arguments for tools that a connected " +
			"web page has declared; it never executes them itself.",
		Thought: "You are a tool-selection assistant embedded in a web page. " +
			"The user is talking to the page, not to you directly. When their " +
			"message calls for an action the page can perform, call the single " +
			"matching tool with well-formed arguments; the page executes it, " +
			"not you. If nothing needs doing, just reply in plain text. Never " +
			"ask the user to wait or claim you performed an action yourself — " +
			"the tool call itself is the action.",
	})
}

// registerForwardingTool registers one platform tool into want's global
// registry. Its Call does not perform the action — it records the
// tool name/args into callSink for WantService.Complete to translate into a
// Result.ToolCalls entry, which the WS session then sends to the browser to
// actually execute.
func registerForwardingTool(t toolschema.Tool) {
	decl := types.ToolDeclaration{
		Name:        t.Name,
		Description: t.Description,
		Type:        "sync",
		Parameters:  parameterSchemaToWant(t.Parameters),
	}
	types.RegisterTool(decl, func() types.ToolInterface {
		return &forwardingTool{name: t.Name}
	})
}

type forwardingTool struct {
	types.BaseToolConfig
	name string
}

func (f *forwardingTool) ValidateInput(types.ToolArguments, types.ToolContext) error { return nil }

func (f *forwardingTool) Call(args types.ToolArguments, ctx types.ToolContext) ([]types.ResultContentBlock, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal args for %s: %w", f.name, err)
	}
	addToCallSink(f.name, raw)

	msg := fmt.Sprintf("Forwarded %q to the page to execute.", f.name)
	ctx.EmitToolResult(map[string]interface{}{"message": msg})
	return []types.ResultContentBlock{types.TextBlock(msg)}, nil
}

func (f *forwardingTool) RenderToolUse(args types.ToolArguments) string {
	return fmt.Sprintf("Calling %s", f.name)
}

func (f *forwardingTool) RenderToolUseError(err error) string {
	return fmt.Sprintf("Failed to call %s: %v", f.name, err)
}

func (f *forwardingTool) RenderToolResult(data map[string]interface{}) string {
	if msg, ok := data["message"].(string); ok {
		return msg
	}
	return "Forwarded to page"
}

// parameterSchemaToWant converts our JSON-Schema subset into the
// map[string]interface{} shape want's ToolDeclaration.Parameters expects
// (mirrors the OpenAI/Anthropic tool schema convention want's providers
// speak — see shuttle's wanttools for the same hand-built shape).
func parameterSchemaToWant(p toolschema.ParameterSchema) map[string]interface{} {
	out := map[string]interface{}{
		"type": p.Type,
	}
	if p.Description != "" {
		out["description"] = p.Description
	}
	if len(p.Properties) > 0 {
		props := make(map[string]interface{}, len(p.Properties))
		for name, sub := range p.Properties {
			if sub == nil {
				continue
			}
			props[name] = parameterSchemaToWant(*sub)
		}
		out["properties"] = props
	}
	if p.Items != nil {
		out["items"] = parameterSchemaToWant(*p.Items)
	}
	if len(p.Required) > 0 {
		out["required"] = p.Required
	}
	if len(p.Enum) > 0 {
		out["enum"] = p.Enum
	}
	return out
}
