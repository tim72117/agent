// Command server runs the agent-tool-platform backend: it loads developer
// tool definitions, exposes codegen endpoints (LLM tool schema + generated
// TypeScript), and serves the WebSocket endpoint the Agent Bridge SDK
// connects to.
package main

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/tim72117/agent-tool-platform/internal/admin"
	"github.com/tim72117/agent-tool-platform/internal/auth"
	"github.com/tim72117/agent-tool-platform/internal/codegen"
	"github.com/tim72117/agent-tool-platform/internal/db"
	"github.com/tim72117/agent-tool-platform/internal/inference"
	"github.com/tim72117/agent-tool-platform/internal/session"
	"github.com/tim72117/agent-tool-platform/internal/toolschema"
	"github.com/tim72117/agent-tool-platform/internal/ws"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Best-effort: a missing .env is normal in production, where real
	// environment variables are set directly and nothing here should block
	// startup over it.
	if err := godotenv.Load(); err != nil {
		log.Debug("no .env file loaded", "err", err)
	}

	dsn := envOr("DATABASE_URL", "postgres://platform:platform@localhost:5434/platform?sslmode=disable")
	conn, err := db.Open(dsn)
	if err != nil {
		log.Error("failed to connect to database", "err", err)
		os.Exit(1)
	}
	defer conn.Close()

	apps, err := toolschema.NewRegistry(conn)
	if err != nil {
		log.Error("failed to load tool definitions", "err", err)
		os.Exit(1)
	}
	log.Info("loaded tool definitions from database", "apps", len(apps.All()))

	originAllowlist := parseOrigins(envOr("ALLOWED_ORIGINS", ""))
	originChecker := ws.AllowAllOrigins
	if len(originAllowlist) > 0 {
		originChecker = allowlistChecker(originAllowlist)
		log.Info("origin allowlist enabled", "origins", originAllowlist)
	} else {
		log.Warn("no ALLOWED_ORIGINS set; accepting WebSocket handshakes from any origin (dev mode only)")
	}

	authStore := auth.New(conn)

	// cookieSecure=false only makes sense for http://localhost dev, where
	// the browser would otherwise refuse to store a Secure cookie at all.
	// Any real deployment must set COOKIE_SECURE=true (or just deploy
	// behind HTTPS and flip the default) — an insecure session cookie sent
	// over plain HTTP is as bad as sending the password on every request.
	cookieSecure := envOr("COOKIE_SECURE", "false") == "true"
	if !cookieSecure {
		log.Warn("COOKIE_SECURE not set to \"true\"; session cookie will be sent over plain HTTP (dev mode only)")
	}
	sessionStore := session.New(conn, cookieSecure)

	// wsAuth == nil is what tells ws.Handler to skip verification entirely
	// (see Handler.ServeHTTP) — appropriate only when literally no app can
	// have a key yet. Since the admin API is always on now (no ADMIN_TOKEN
	// gate anymore — any registered user can create an app and issue it a
	// key at any moment), auth must always stay enforced once at least one
	// app exists. An empty Store just rejects every token until the first
	// Issue call, rather than skipping the check.
	wsAuth := authStore
	if authStore.Count() > 0 {
		log.Info("API key auth enabled", "keys", authStore.Count())
	} else {
		log.Info("API key auth enabled (no keys issued yet)")
	}

	inferSvc := newInferenceService(log, apps.All())
	wsHandler := ws.NewHandler(apps, inferSvc, log, originChecker, wsAuth)
	adminHandler := admin.NewHandler(apps, authStore, sessionStore)

	mux := http.NewServeMux()
	mux.Handle("/ws", wsHandler)
	mux.HandleFunc("/apps/{appId}/tools.json", handleToolSchema(apps))
	mux.HandleFunc("/apps/{appId}/tools.ts", handleToolTypeScript(apps))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	adminHandler.Register(mux)

	addr := envOr("ADDR", ":8080")
	log.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, withCORS(mux)); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func handleToolSchema(apps *toolschema.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app, ok := apps.Get(r.PathValue("appId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(codegen.ToLLMTools(app))
	}
}

func handleToolTypeScript(apps *toolschema.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app, ok := apps.Get(r.PathValue("appId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		src, err := codegen.TypeScript(app)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(src))
	}
}

// withCORS enables browser fetches to the HTTP endpoints above from any
// origin. The codegen endpoints (tools.json / tools.ts) stay permissive —
// they only return public artifacts, so "*" leaks nothing there.
//
// The /admin and /auth endpoints now run on session cookies (internal/session)
// instead of a bearer token, and cookies are ambient credentials a
// cross-site page can ride on — the classic case CORS exists to guard
// against. Browsers refuse to combine Access-Control-Allow-Origin: * with
// Access-Control-Allow-Credentials: true specifically to prevent that, so
// this echoes back the actual request Origin (and always sets
// Allow-Credentials) rather than using a wildcard. That's still an open
// allowlist — any origin gets echoed back — matching the tool-editor's
// current "runs wherever a developer points it" deployment model; a
// production deployment with a fixed dashboard origin should replace this
// with a real allowlist the way ws.Handler's AllowedOrigins already works.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseOrigins(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fileSettings mirrors the JSON shape of configs/settings.json. Loaded
// separately from want's own config.Settings because that type marks
// credential fields json:"-" (deliberately unreadable from a committable
// file); this project's settings.json is gitignored and keeps them alongside
// the rest of the config for local/dev convenience.
type fileSettings struct {
	OllamaURL    string `json:"ollama_url"`
	VLLMBaseURL  string `json:"vllm_base_url"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	GoogleAPIKey string `json:"google_api_key"`
}

func loadFileSettings(path string) fileSettings {
	var fs fileSettings
	data, err := os.ReadFile(path)
	if err != nil {
		return fs
	}
	if err := json.Unmarshal(data, &fs); err != nil {
		return fs
	}
	return fs
}

// newInferenceService selects the reasoning backend. Settings are read from
// configs/settings.json first, with environment variables overriding
// individual fields. AI_PROVIDER/provider unset (or "mock") keeps the
// existing MockService so local/demo setups without any LLM credentials
// keep working; any other value boots a want orchestrator scoped to exactly
// the tools declared by apps (see inference.RegisterPlatformTools).
func newInferenceService(log *slog.Logger, apps map[string]*toolschema.App) inference.Service {
	fs := loadFileSettings(envOr("SETTINGS_FILE", "configs/settings.json"))

	provider := envOr("AI_PROVIDER", fs.Provider)
	if provider == "" {
		provider = "mock"
	}
	if provider == "mock" {
		return inference.NewMock()
	}

	log.Info("using want orchestrator for inference", "provider", provider)
	inference.RegisterPlatformTools(apps)
	return inference.NewWant(inference.WantSettings{
		Provider:        provider,
		Model:           envOr("AI_MODEL", fs.Model),
		OllamaURL:       envOr("OLLAMA_URL", cmp.Or(fs.OllamaURL, "http://localhost:11434")),
		VLLMBaseURL:     envOr("VLLM_BASE_URL", fs.VLLMBaseURL),
		GoogleAPIKey:    envOr("GOOGLE_API_KEY", fs.GoogleAPIKey),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		Workspace:       envOr("WANT_WORKSPACE", ""),
	})
}

func allowlistChecker(allowed []string) ws.OriginChecker {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[o] = true
	}
	return func(origin string) bool {
		return set[origin]
	}
}
