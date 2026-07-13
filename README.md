# onagent

A platform that lets developers describe front-end capabilities as LLM
tools, then generates both the LLM-facing tool schema and the matching
TypeScript front-end code. A browser SDK ("Agent Bridge") connects any
web page to a backend inference service over WebSocket: it pushes page
context/state, and dispatches `tool_call`s the inference service returns
back into the page (fill a form, navigate, highlight an element, etc.).

LLM reasoning/inference is provided by want (a private LLM orchestration
library)'s `orchestrator.Orchestrator`: `internal/inference.NewWant` wires
a provider (Anthropic, Ollama, vLLM, Google) into the `inference.Service`
boundary and does real tool-calling inference against the app's tool
schema. For local/demo setups without any LLM credentials,
`internal/inference.MockService` is a fallback that echoes a plausible
`tool_call` for whichever tool name appears in the prompt, so the rest of
the pipeline (tool loading, codegen, WebSocket protocol, SDK, demo app)
still works end-to-end without a real model. `cmd/server/main.go` picks
between them based on `AI_PROVIDER`/`configs/settings.json` (unset or
`mock` uses `MockService`; any other provider boots the want
orchestrator).

## Layout

```
backend/                     Go backend
  cmd/server/                 HTTP + WebSocket entrypoint
  internal/toolschema/        Developer tool-definition format (YAML) + loader
  internal/codegen/           tool defs -> LLM tool JSON, and -> TypeScript
  internal/protocol/          WebSocket message envelope types
  internal/ws/                WebSocket session/handler (Origin allowlist, dispatch)
  internal/inference/         Boundary to the inference service (want orchestrator; mock fallback for local dev)
  tools/                      Developer tool-definition YAML files (one per app)

packages/agent-bridge-sdk/   Browser SDK developers embed in their site
examples/react-demo/         Vite + React app demonstrating the SDK end-to-end
docs/security-and-transport.md   Cross-site transport design notes (GA-derived)
```

## How it fits together

1. A developer describes their app's tools in `backend/tools/<app>.yaml`
   (name, description, JSON-Schema-style parameters — same shape as
   OpenAI/Anthropic tool calling).
2. The backend loads all app definitions at startup and exposes, per app:
   - `GET /apps/{appId}/tools.json` — the LLM tool schema to hand to the
     inference service.
   - `GET /apps/{appId}/tools.ts` — generated TypeScript (`ToolHandlers`
     interface + arg/result types) the developer implements against.
3. The developer's page embeds `@onagent/agent-bridge-sdk`,
   constructs an `AgentBridge` with `appId` + a `tools` object implementing
   the generated `ToolHandlers` interface, and calls `sendContext()` /
   `prompt()`.
4. The backend's `/ws` endpoint receives `hello` (selects the app's tool
   set), `context`, and `prompt` messages; forwards prompts to
   `inference.Service` along with the app's tool schema; and relays any
   resulting `tool_call`s back to the browser, which dispatches them to
   the registered handler and returns a `tool_result`.

See `backend/internal/protocol/message.go` for the full message set.

## Quick start

Requires Go 1.22+ and Node 20+.

```bash
# Backend
cd backend
go run ./cmd/server                      # serves on :8080 by default
# Optional: ALLOWED_ORIGINS=https://your-site.example (CSV) restricts
# which page origins may open a WebSocket connection; unset = dev mode,
# any origin accepted (a warning is logged).

# SDK (build once so examples/react-demo can import it)
cd packages/agent-bridge-sdk
npm install
npm run build

# Demo app
cd examples/react-demo
npm install
echo "VITE_AGENT_WS_URL=ws://localhost:8080/ws" > .env.local
npm run dev
```

Open the demo app and type a prompt — with an `AI_PROVIDER` configured,
the want orchestrator reasons over the prompt and the app's tool schema
and returns real `tool_call`s; with no provider configured, the mock
inference service echoes back a matching `tool_call` for any tool name
mentioned in the prompt (e.g. "please fill_search_form for me"). Either
way, the SDK dispatches the resulting `tool_call` to the handler
registered in `examples/react-demo/src/App.tsx`.

## Adding a new tool

1. Add an entry under `tools:` in `backend/tools/<app>.yaml` (or a new file
   for a new `appId`).
2. Restart the backend; fetch `/apps/<appId>/tools.ts` and copy the
   generated `ToolHandlers` interface into your front-end project (or wire
   up a build step that fetches it automatically — not set up yet).
3. Implement the new method in the object passed as `tools` to
   `AgentBridge`.

## Status / what's not built yet

- Real inference is wired up via want's `orchestrator.Orchestrator` (see
  `internal/inference.NewWant`); `internal/inference.MockService` remains
  as an opt-in fallback for local dev without LLM credentials.
- No per-session auth (token issuance/verification) — currently identity
  is just whatever `appId` the client claims in `hello`. See
  `docs/security-and-transport.md` for what's needed before this is
  production-facing.
- No `beaconUrl` HTTP endpoint on the backend yet (the SDK supports
  calling one on page unload; nothing serves it server-side).
- No automated test suite yet.
