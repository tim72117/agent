// Command server runs the onagent backend: it loads developer
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
	"runtime/debug"
	"strings"

	"github.com/joho/godotenv"
	"github.com/tim72117/onagent/internal/adminauth"
	"github.com/tim72117/onagent/internal/adminconsole"
	"github.com/tim72117/onagent/internal/auth"
	"github.com/tim72117/onagent/internal/cliauth"
	"github.com/tim72117/onagent/internal/codegen"
	"github.com/tim72117/onagent/internal/console"
	"github.com/tim72117/onagent/internal/db"
	"github.com/tim72117/onagent/internal/inference"
	"github.com/tim72117/onagent/internal/quota"
	"github.com/tim72117/onagent/internal/session"
	"github.com/tim72117/onagent/internal/toolschema"
	"github.com/tim72117/onagent/internal/usertoken"
	"github.com/tim72117/onagent/internal/ws"
)

const usage = `Usage: server [-h|--help]

Runs the onagent backend: loads developer tool definitions, exposes codegen
endpoints (LLM tool schema + generated TypeScript), and serves the WebSocket
endpoint the Agent Bridge SDK connects to.

Configured entirely via environment variables (optionally loaded from a
.env file in the working directory):

  ADDR                    Listen address (default ":8080")
  DATABASE_URL            Postgres DSN
                          (default "postgres://platform:platform@localhost:5434/platform?sslmode=disable")
  APP_ENV                 Set to "production" to turn insecure-default
                          warnings (missing ALLOWED_ORIGINS/CONSOLE_ORIGIN,
                          COOKIE_SECURE=false) into startup failures.
  ALLOWED_ORIGINS         Comma-separated developer app origins allowed to
                          open /ws and hit credentialed /console, /auth
                          endpoints. Required when APP_ENV=production;
                          unset accepts any origin (dev mode only).
  COOKIE_SECURE           "true" to send session cookies with Secure
                          (required when APP_ENV=production). Default
                          "false" (plain HTTP, dev only).
  CONSOLE_ORIGIN          Comma-separated origins for the developer console
                          frontend (Playground WebSocket + CORS). Required
                          when APP_ENV=production; defaults to
                          "http://localhost:5173" outside production.
  ADMIN_ORIGIN            Comma-separated origins for the admin SPA CORS.
                          Default "http://localhost:5174".
  ADMIN_BOOTSTRAP_EMAIL   Email for the first admin account, created once
                          on startup if no admin exists yet.
  ADMIN_BOOTSTRAP_PASSWORD
                          Password for the bootstrapped admin account.
  SETTINGS_FILE           Path to the AI provider settings JSON
                          (default "configs/settings.json").
  AI_PROVIDER             Overrides the provider from SETTINGS_FILE.
  AI_MODEL                Overrides the model from SETTINGS_FILE.
  OLLAMA_URL              Ollama base URL (default "http://localhost:11434").
  VLLM_BASE_URL           vLLM base URL.
  GOOGLE_API_KEY          Overrides the Google API key from SETTINGS_FILE.
  WANT_WORKSPACE          Workspace path for the want registry (default "").
`

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "-h" || arg == "--help" {
			os.Stdout.WriteString(usage)
			return
		}
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Best-effort: a missing .env is normal in production, where real
	// environment variables are set directly and nothing here should block
	// startup over it.
	if err := godotenv.Load(); err != nil {
		log.Debug("no .env file loaded", "err", err)
	}

	// APP_ENV=production turns the insecure-default warnings below into
	// startup failures. Unset (or any other value) keeps today's dev-mode
	// behavior of warning and continuing, since local dev and CI never set
	// this. Cloud Run must set APP_ENV=production so a forgotten
	// ALLOWED_ORIGINS/COOKIE_SECURE fails loudly at deploy time instead of
	// silently running open in front of real users.
	isProd := os.Getenv("APP_ENV") == "production"

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

	// Governs both the WebSocket handshake (ws.Handler) and credentialed
	// CORS for /console, /auth (withCORS) — one setting, since both are
	// really the same question: which front-end origins does this backend
	// trust with a real session/token, not just "which origins can read
	// public codegen artifacts" (unrestricted regardless, see withCORS).
	originAllowlist := parseOrigins(envOr("ALLOWED_ORIGINS", ""))
	originChecker := ws.AllowAllOrigins
	if len(originAllowlist) > 0 {
		originChecker = allowlistChecker(originAllowlist)
		log.Info("origin allowlist enabled", "origins", originAllowlist)
	} else if isProd {
		log.Error("APP_ENV=production but ALLOWED_ORIGINS is not set; refusing to start open to any origin")
		os.Exit(1)
	} else {
		log.Warn("no ALLOWED_ORIGINS set; accepting WebSocket handshakes AND credentialed /console, /auth CORS requests from any origin (dev mode only — set this before any real deployment)")
	}

	authStore := auth.New(conn)

	// cookieSecure=false only makes sense for http://localhost dev, where
	// the browser would otherwise refuse to store a Secure cookie at all.
	// Any real deployment must set COOKIE_SECURE=true (or just deploy
	// behind HTTPS and flip the default) — an insecure session cookie sent
	// over plain HTTP is as bad as sending the password on every request.
	cookieSecure := envOr("COOKIE_SECURE", "false") == "true"
	if !cookieSecure {
		if isProd {
			log.Error("APP_ENV=production but COOKIE_SECURE is not \"true\"; refusing to start with session cookies sent over plain HTTP")
			os.Exit(1)
		}
		log.Warn("COOKIE_SECURE not set to \"true\"; session cookie will be sent over plain HTTP (dev mode only)")
	}
	sessionStore := session.New(conn, cookieSecure)
	tokenStore := usertoken.New(conn)
	cliAuthStore := cliauth.New(conn)

	// Monthly prompt quota, enforced at the WebSocket handshake and per
	// prompt (see internal/quota, ws.Handler, ws.Session). Backed by the
	// same DB as everything else here; this server always has one, so quota
	// is always on. (The nil-Service "disabled" mode exists for callers with
	// no database — e.g. tests — not for this path.)
	quotaSvc := quota.New(conn)

	// Admin back-office identity (internal/adminauth), a system deliberately
	// separate from the developer accounts above: its own tables, its own
	// cookie. cookieSecure is set further down for the developer session; the
	// admin store shares the same HTTPS/plain-HTTP decision, so read it here
	// via the same env the developer session uses below.
	adminSecure := envOr("COOKIE_SECURE", "false") == "true"
	adminAuthStore := adminauth.New(conn, adminSecure)

	// Seed the first admin from the environment — the ONLY way an admin comes
	// into being (there is no admin-signup endpoint), so the trust root is
	// whoever controls the deployment's env, never an API caller. No-op when
	// the vars are unset or the admin already exists. A genuine bootstrap
	// failure (a DB write error) is still fatal; but simply having no admin
	// configured is NOT — it only means the admin back-office has no login
	// yet, which must not take down the whole service (developer console,
	// quota, the public API) with it. So that case warns loudly and keeps
	// serving; set ADMIN_BOOTSTRAP_EMAIL/PASSWORD and redeploy to seed one.
	if created, err := adminAuthStore.Bootstrap(os.Getenv("ADMIN_BOOTSTRAP_EMAIL"), os.Getenv("ADMIN_BOOTSTRAP_PASSWORD")); err != nil {
		log.Error("admin bootstrap failed", "err", err)
		os.Exit(1)
	} else if created {
		log.Info("bootstrapped first admin from ADMIN_BOOTSTRAP_EMAIL")
	}
	if adminAuthStore.Count() == 0 {
		log.Warn("no admin account exists and ADMIN_BOOTSTRAP_EMAIL/PASSWORD were not set; the admin back-office (/admin) has no way to log in until you set them and redeploy. The rest of the service is unaffected.")
	}

	// wsAuth == nil is what tells ws.Handler to skip verification entirely
	// (see Handler.ServeHTTP) — appropriate only when literally no app can
	// have a key yet. Since the console API is always on now (no ADMIN_TOKEN
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

	// Separate from ALLOWED_ORIGINS above: that setting is about developer
	// apps' own sites talking to /ws. This is the console frontend itself
	// (a single app this project ships, not a developer's), needed only so
	// internal/console/playground.go can accept its cross-origin WebSocket
	// handshake — see Handler.ConsoleOrigins.
	//
	// The fallback to localhost:5173 (the console's standard dev port) only
	// applies outside production, so a fresh checkout works with zero
	// config. In production, CONSOLE_ORIGIN must be set explicitly — an
	// unset var used to silently default to localhost:5173 even in prod
	// (parseOrigins(envOr(..., "http://localhost:5173")) is never empty, so
	// the len==0 guard below could never fire), which meant a forgotten
	// CONSOLE_ORIGIN failed *quietly*: the server started fine but every
	// Playground WebSocket and credentialed CORS check trusted only
	// localhost, never the real deployed console origin.
	consoleOriginRaw := os.Getenv("CONSOLE_ORIGIN")
	if consoleOriginRaw == "" && !isProd {
		consoleOriginRaw = "http://localhost:5173"
	}
	consoleOrigins := parseOrigins(consoleOriginRaw)
	if isProd && len(consoleOrigins) == 0 {
		log.Error("APP_ENV=production but CONSOLE_ORIGIN is not set; refusing to start with the Playground WebSocket unreachable")
		os.Exit(1)
	}

	// Where the admin SPA (apps/admin) is served from, for the credentialed
	// CORS on /admin/api/*. Defaults to a local dev port distinct from the
	// console's (5173) so both dev servers can run at once. In production the
	// admin SPA is served same-origin from /admin, so a cross-origin allow is
	// only strictly needed in dev — but requiring it in prod would break any
	// deployment that does serve the admin console from a separate domain, so
	// it's left optional and simply merged into the credentialed-CORS allow
	// set below.
	adminOrigins := parseOrigins(envOr("ADMIN_ORIGIN", "http://localhost:5174"))

	// Credentialed CORS (see withCORS) must allow the developer app origins,
	// the console origin, AND the admin origin, since /console, /auth, and
	// /admin all ride on cookies. WebSocket handshake origin enforcement
	// stays on originChecker alone (developer apps only) — the console/admin
	// UIs never open the developer /ws.
	credentialedOrigins := anyOf(originChecker, allowlistChecker(consoleOrigins), allowlistChecker(adminOrigins))

	inferSvc := newInferenceService(log, apps.All())
	wsHandler := ws.NewHandler(apps, inferSvc, log, originChecker, wsAuth, quotaSvc)
	consoleHandler := console.NewHandler(apps, authStore, sessionStore, tokenStore, cliAuthStore, inferSvc, quotaSvc, consoleOrigins)

	mux := http.NewServeMux()
	mux.Handle("/ws", wsHandler)
	mux.HandleFunc("/apps/{appId}/tools.json", handleToolSchema(apps))
	mux.HandleFunc("/apps/{appId}/tools.ts", handleToolTypeScript(apps))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	consoleHandler.Register(mux)

	// Admin back-office API under /admin/api/* — a separate handler and
	// identity system from the developer console above (internal/adminauth).
	adminHandler := adminconsole.NewHandler(adminAuthStore, quotaSvc)
	adminHandler.Register(mux)

	// Static frontend hosting (apps/landing at "/", apps/console at "/app"
	// with SPA fallback) — registered last, after every API route above,
	// purely for readability ("APIs first, static hosting is the
	// fallback"); Go's http.ServeMux dispatches by pattern specificity, not
	// registration order, so this ordering doesn't change behavior. See
	// cmd/server/web.go for the embed.FS setup and why the embedded trees
	// are placeholders in an ordinary local build.
	mountStatic(mux, log)

	addr := envOr("ADDR", ":8080")
	log.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, recoverMiddleware(withCORS(mux, credentialedOrigins), log)); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// recoverMiddleware turns a panic in any handler into a 500 response instead
// of taking down the whole process — without it, one request hitting an
// unanticipated nil/index/type-assertion edge case kills every other user's
// in-flight connection too, since nothing else in this codebase calls
// recover(). Only covers the request goroutine http.Server itself spawns per
// connection; handlers that spin off their own long-lived goroutine (e.g.
// ws.Session's read loop, playground's prompt loop) need their own recover
// at the top of that goroutine — this can't reach into those.
func recoverMiddleware(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic recovered", "err", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
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

// withCORS enables browser fetches to the HTTP endpoints above. Policy
// differs by path, because the security requirement differs:
//
//   - /apps/{appId}/tools.json|.ts (codegen) return public, non-credentialed
//     artifacts — "*" leaks nothing there, so any origin is fine.
//
//   - /console/* and /auth/* run on session cookies (internal/session) or a
//     bearer token minted through them, and cookies are ambient credentials
//     a cross-site page can ride on — the classic case CORS exists to guard
//     against. This used to reflect back *any* request Origin (with
//     Access-Control-Allow-Credentials: true), which let any website that
//     got a logged-in user to visit it read authenticated responses from
//     their browser — including a freshly minted bearer token from
//     POST /console/tokens or /console/cli-auth/approve. allowedOrigins
//     (the same allowlist ws.Handler already enforces for WebSocket
//     handshakes — one ALLOWED_ORIGINS setting governs both) is now
//     required to get Access-Control-Allow-Origin at all here; an origin
//     not on the list gets no such header, so the browser's own
//     same-origin policy blocks the page from ever reading the response,
//     regardless of what the server computed.
func withCORS(next http.Handler, allowedOrigins ws.OriginChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// /admin/api/* is credentialed too (admin session cookie), so it
		// must go through the allowlisted, credentials-true branch — never
		// the "*" branch, which browsers refuse to combine with credentials.
		credentialed := strings.HasPrefix(r.URL.Path, "/console/") ||
			strings.HasPrefix(r.URL.Path, "/auth/") ||
			strings.HasPrefix(r.URL.Path, "/admin/")

		switch {
		case credentialed:
			if origin != "" && allowedOrigins(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}
		default:
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

// anyOf combines origin checkers with OR: an origin is allowed if any of
// them allows it. Used to build the credentialed-CORS allow set from the
// developer app allowlist plus the console and admin origins.
func anyOf(checkers ...ws.OriginChecker) ws.OriginChecker {
	return func(origin string) bool {
		for _, c := range checkers {
			if c != nil && c(origin) {
				return true
			}
		}
		return false
	}
}
