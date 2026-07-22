// Package console exposes the API the console front-end (or any other
// client) uses for developers to register/log in and manage their own
// apps: creating them, editing tool definitions and the agent Thought, and
// issuing/revoking API keys. This is not an administrator-only surface —
// every registered user gets one, scoped to the apps they created.
//
// Every route (other than /auth/register and /auth/login themselves)
// requires either a valid session cookie (internal/session, for the
// browser console) or a bearer token (internal/usertoken, for CLI/script
// access) — see withAuth. Every app-scoped operation additionally checks
// that the calling user owns the app before touching it; this ownership
// check is the actual multi-tenant boundary, applied identically
// regardless of which of the two auth methods resolved the caller. There
// is no super-admin override: a user can only ever see and modify apps
// they created.
// (An earlier version of this package used one shared ADMIN_TOKEN with no
// per-app ownership at all, and was named "admin" — misleading, since it's
// every developer's own workspace, not an operator-only console.)
package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tim72117/onagent/internal/auth"
	"github.com/tim72117/onagent/internal/cliauth"
	"github.com/tim72117/onagent/internal/inference"
	"github.com/tim72117/onagent/internal/quota"
	"github.com/tim72117/onagent/internal/session"
	"github.com/tim72117/onagent/internal/toolschema"
	"github.com/tim72117/onagent/internal/usertoken"
)

// Handler serves the /console/* and /auth/* APIs.
type Handler struct {
	Apps      *toolschema.Registry
	Auth      *auth.Store
	Session   *session.Store
	Tokens    *usertoken.Store
	CliAuth   *cliauth.Store
	Inference inference.Service // used only by playground.go's test-prompt endpoint
	Quota     *quota.Service    // nil disables enforcement; playground prompts count against the owner's quota like real traffic
	// ConsoleOrigins is the set of origins the console front-end itself is
	// served from (e.g. http://localhost:5173 in dev). Used only by
	// playground.go to accept the Playground WebSocket's cross-origin
	// handshake — the console (this API's own frontend) and this backend
	// almost never share a host:port, even in dev, so gorilla/websocket's
	// same-origin default rejects every real Playground connection unless
	// these are explicitly trusted. See playground.go's playgroundUpgrader.
	ConsoleOrigins []string
}

func NewHandler(apps *toolschema.Registry, authStore *auth.Store, sessionStore *session.Store, tokenStore *usertoken.Store, cliAuthStore *cliauth.Store, inferSvc inference.Service, quotaSvc *quota.Service, consoleOrigins []string) *Handler {
	return &Handler{Apps: apps, Auth: authStore, Session: sessionStore, Tokens: tokenStore, CliAuth: cliAuthStore, Inference: inferSvc, Quota: quotaSvc, ConsoleOrigins: consoleOrigins}
}

// syncWantRole re-registers appID's want agent role (tool whitelist +
// Thought) so an edit takes effect on the very next prompt, without a
// restart. Called after every successful write that changes what an app's
// agent should see/say (create, save tools, set thought) — see
// inference.RegisterAppRole's doc comment for what happens if this is
// skipped. A no-op-safe best-effort: if the Registry's cache hasn't
// reflected the write yet (shouldn't happen — Registry.Save/Create both
// Reload before returning), this silently does nothing rather than
// panicking, since a stale want role is a correctness bug to fix, not a
// reason to fail the HTTP request that already succeeded.
func (h *Handler) syncWantRole(appID string) {
	if app, ok := h.Apps.Get(appID); ok {
		inference.RegisterAppRole(app)
	}
}

// Register mounts the auth and console routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/register", h.register)
	mux.HandleFunc("POST /auth/login", h.login)
	mux.HandleFunc("POST /auth/logout", h.logout)
	mux.HandleFunc("GET /auth/me", h.withAuth(h.me))

	mux.HandleFunc("GET /console/quota", h.withAuth(h.getQuota))

	mux.HandleFunc("GET /console/apps", h.withAuth(h.listApps))
	mux.HandleFunc("POST /console/apps", h.withAuth(h.createApp))
	mux.HandleFunc("GET /console/apps/{appId}", h.withOwnedApp(h.getApp))
	mux.HandleFunc("PUT /console/apps/{appId}/tools", h.withOwnedApp(h.saveTools))
	mux.HandleFunc("PUT /console/apps/{appId}/origin", h.withOwnedApp(h.setOrigin))
	mux.HandleFunc("PUT /console/apps/{appId}/thought", h.withOwnedApp(h.setThought))
	mux.HandleFunc("DELETE /console/apps/{appId}", h.withOwnedApp(h.deleteApp))
	mux.HandleFunc("POST /console/apps/{appId}/key", h.withOwnedApp(h.issueKey))
	mux.HandleFunc("DELETE /console/apps/{appId}/key", h.withOwnedApp(h.revokeKey))
	mux.HandleFunc("GET /console/apps/{appId}/playground", h.withOwnedApp(h.playgroundWS))

	// issueToken and approveCliAuth are withCookieAuth, not withAuth: both
	// mint a new bearer token, and if a bearer token itself could
	// authorize minting more of them, one leaked token would let an
	// attacker mint unlimited replacements — revoking the token that
	// leaked wouldn't cut off access, the attacker just switches to one
	// minted before the victim noticed. Requiring the browser session
	// (which a CLI never holds beyond the moment it trades it for a
	// token) breaks that chain. Listing/revoking stay on withAuth since
	// neither compounds access — revoking is self-limiting no matter
	// which credential requested it.
	mux.HandleFunc("POST /console/tokens", h.withCookieAuth(h.issueToken))
	mux.HandleFunc("GET /console/tokens", h.withAuth(h.listTokens))
	mux.HandleFunc("DELETE /console/tokens/{tokenId}", h.withAuth(h.revokeToken))

	// start and exchange are unauthenticated by design — see
	// internal/cliauth's package doc for why the session id itself (32
	// random bytes, single-use) is the right credential for each: Start
	// happens before the CLI has any credential at all, and Exchange's id
	// only ever works once, right after a legitimate approval, for
	// whoever holds the id the CLI itself generated the URL from.
	mux.HandleFunc("POST /console/cli-auth/start", h.startCliAuth)
	mux.HandleFunc("GET /console/cli-auth/{id}", h.getCliAuth)
	mux.HandleFunc("POST /console/cli-auth/{id}/approve", h.withCookieAuth(h.approveCliAuth))
	mux.HandleFunc("POST /console/cli-auth/{id}/exchange", h.exchangeCliAuth)
}

// withAuth resolves the caller's identity — a session cookie first (the
// browser console's path), falling back to a bearer token
// (internal/usertoken, the CLI's path) — and rejects the request if
// neither resolves. Handlers downstream see a single *session.User either
// way; they never need to know which method authenticated the caller.
func (h *Handler) withAuth(next func(http.ResponseWriter, *http.Request, *session.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user, ok := h.Session.Verify(r); ok {
			next(w, r, user)
			return
		}
		if u, ok := h.Tokens.Verify(r); ok {
			next(w, r, &session.User{ID: u.ID, Email: u.Email})
			return
		}
		http.Error(w, "not authenticated", http.StatusUnauthorized)
	}
}

// withCookieAuth is withAuth restricted to the session cookie only, no
// bearer-token fallback — see the Register call sites for why this
// matters specifically for token-minting routes.
func (h *Handler) withCookieAuth(next func(http.ResponseWriter, *http.Request, *session.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := h.Session.Verify(r)
		if !ok {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}
		next(w, r, user)
	}
}

// withOwnedApp is withAuth plus an ownership check on the {appId} path
// value: the request is rejected before the handler runs at all if the
// session's user doesn't own that app. Handlers behind this are guaranteed
// both an authenticated user and confirmed ownership.
//
// A nonexistent appId and an appId owned by someone else both produce 404,
// not 403 — a 403 would confirm to a prober "this app exists, you just
// can't touch it," leaking which app ids are taken.
func (h *Handler) withOwnedApp(next func(http.ResponseWriter, *http.Request, *session.User)) http.HandlerFunc {
	return h.withAuth(func(w http.ResponseWriter, r *http.Request, user *session.User) {
		appID := r.PathValue("appId")
		ownerID, ok := h.Apps.OwnerOf(appID)
		if !ok || ownerID != user.ID {
			http.Error(w, "unknown appId", http.StatusNotFound)
			return
		}
		next(w, r, user)
	})
}

// --- auth ----------------------------------------------------------------

type authRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	Email string `json:"email"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	user, err := h.Session.Register(req.Email, req.Password)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, session.ErrEmailTaken) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	if _, err := h.Session.CreateSession(w, user.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, authResponse{Email: user.Email})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	user, err := h.Session.Login(req.Email, req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	if _, err := h.Session.CreateSession(w, user.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, authResponse{Email: user.Email})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	h.Session.Logout(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request, user *session.User) {
	writeJSON(w, http.StatusOK, authResponse{Email: user.Email})
}

// --- quota -----------------------------------------------------------------

// quotaResponse is the caller's own plan + usage-this-period standing.
// Field names mirror quota.UserSummary (the admin back-office's per-user
// shape returned by GET /admin/api/users) — tier/limit/used name the same
// facts there and here, so the two surfaces don't invent different
// vocabulary for the same numbers. periodStart/periodEnd are RFC 3339
// (encoding/json's default time.Time marshaling) since nothing else in this
// package needs a different format for a timestamp.
type quotaResponse struct {
	Tier        string    `json:"tier"`
	PlanName    string    `json:"planName"`
	Limit       int       `json:"limit"`
	Used        int       `json:"used"`
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
}

// getQuota reports the calling developer's own plan and current-period
// usage. Scoped to the account (user), not to a single app: quota is billed
// per owner across every app they have, matching how quota.Check/Record
// already enforce it — see quota.Service.StandingFor's doc comment. No app
// ownership check applies here (unlike the /console/apps/{appId}/* routes)
// since there is no {appId} in this path at all.
func (h *Handler) getQuota(w http.ResponseWriter, r *http.Request, user *session.User) {
	st, err := h.Quota.StandingFor(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, quotaResponse{
		Tier:        string(st.Tier),
		PlanName:    st.PlanName,
		Limit:       st.Limit,
		Used:        st.Used,
		PeriodStart: st.PeriodStart,
		PeriodEnd:   st.PeriodEnd,
	})
}

// --- apps ------------------------------------------------------------------

// appSummary is what listApps returns per app: enough for a dashboard list
// view without shipping every tool's full schema.
type appSummary struct {
	AppID         string `json:"appId"`
	ToolCount     int    `json:"toolCount"`
	HasKey        bool   `json:"hasKey"`
	AllowedOrigin string `json:"allowedOrigin"` // "" means unset (fail-closed — see ws.Handler.ServeHTTP)
	Thought       string `json:"thought"`       // "" means the platform default applies (agent_roles.go's defaultThought)
}

func (h *Handler) listApps(w http.ResponseWriter, r *http.Request, user *session.User) {
	ids, err := h.Apps.OwnedBy(user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := make([]appSummary, 0, len(ids))
	for _, id := range ids {
		app, ok := h.Apps.Get(id)
		if !ok {
			continue // owner_id row exists but Registry cache hasn't caught up; skip rather than fake zero tools
		}
		out = append(out, appSummary{
			AppID:         id,
			ToolCount:     len(app.Tools),
			HasKey:        h.Auth.HasKey(id),
			AllowedOrigin: h.Auth.OriginFor(id),
			Thought:       app.Thought,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// getApp returns the full App definition (every tool with its complete
// parameter and returns schema). The editor loads this for editing — the
// public /apps/{appId}/tools.json can't serve that purpose because its
// LLM-schema shape drops the returns declaration.
func (h *Handler) getApp(w http.ResponseWriter, r *http.Request, user *session.User) {
	app, _ := h.Apps.Get(r.PathValue("appId")) // ownership + existence already checked by withOwnedApp
	writeJSON(w, http.StatusOK, app)
}

type createAppRequest struct {
	AppID string `json:"appId"`
}

func (h *Handler) createApp(w http.ResponseWriter, r *http.Request, user *session.User) {
	var req createAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if err := h.Apps.Create(req.AppID, user.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.syncWantRole(req.AppID)
	writeJSON(w, http.StatusCreated, appSummary{AppID: req.AppID, ToolCount: 0, HasKey: false})
}

type setOriginRequest struct {
	// Origin is the exact value the site's Origin header must present, e.g.
	// "https://demo.example.com" (no path, no trailing slash — that's what
	// browsers actually send). Empty string clears it, returning the app to
	// fail-closed (no connections accepted) until set again.
	Origin string `json:"origin"`
}

func (h *Handler) setOrigin(w http.ResponseWriter, r *http.Request, user *session.User) {
	appID := r.PathValue("appId")

	var req setOriginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if err := h.Auth.SetOrigin(appID, req.Origin); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	app, _ := h.Apps.Get(appID)
	writeJSON(w, http.StatusOK, appSummary{
		AppID:         appID,
		ToolCount:     len(app.Tools),
		HasKey:        h.Auth.HasKey(appID),
		AllowedOrigin: req.Origin,
		Thought:       app.Thought,
	})
}

type setThoughtRequest struct {
	// Thought is the app's custom want agent system prompt. Empty string
	// clears it, returning the app to the platform default.
	Thought string `json:"thought"`
}

func (h *Handler) setThought(w http.ResponseWriter, r *http.Request, user *session.User) {
	appID := r.PathValue("appId")

	var req setThoughtRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if err := h.Apps.SetThought(appID, req.Thought); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.syncWantRole(appID)
	app, _ := h.Apps.Get(appID)
	writeJSON(w, http.StatusOK, appSummary{
		AppID:         appID,
		ToolCount:     len(app.Tools),
		HasKey:        h.Auth.HasKey(appID),
		AllowedOrigin: h.Auth.OriginFor(appID),
		Thought:       req.Thought,
	})
}

func (h *Handler) saveTools(w http.ResponseWriter, r *http.Request, user *session.User) {
	appID := r.PathValue("appId")

	var tools []toolschema.Tool
	if err := json.NewDecoder(r.Body).Decode(&tools); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	app := &toolschema.App{AppID: appID, Tools: tools}
	if err := h.Apps.Save(app); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.syncWantRole(appID)
	saved, _ := h.Apps.Get(appID) // Save's own Reload already refreshed this; existing thought is untouched by saveApp (see registry.go)
	writeJSON(w, http.StatusOK, appSummary{
		AppID:         appID,
		ToolCount:     len(tools),
		HasKey:        h.Auth.HasKey(appID),
		AllowedOrigin: h.Auth.OriginFor(appID),
		Thought:       saved.Thought,
	})
}

func (h *Handler) deleteApp(w http.ResponseWriter, r *http.Request, user *session.User) {
	appID := r.PathValue("appId")
	if err := h.Apps.Delete(appID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Auth.Revoke(appID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type issueKeyResponse struct {
	AppID  string `json:"appId"`
	ApiKey string `json:"apiKey"` // plaintext — shown exactly once, never retrievable again
}

func (h *Handler) issueKey(w http.ResponseWriter, r *http.Request, user *session.User) {
	appID := r.PathValue("appId")
	key, err := h.Auth.Issue(appID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, issueKeyResponse{AppID: appID, ApiKey: key})
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request, user *session.User) {
	appID := r.PathValue("appId")
	if err := h.Auth.Revoke(appID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- user tokens (CLI/script auth) ------------------------------------------

type issueTokenRequest struct {
	// Name is a human label distinguishing this token from a user's other
	// ones, e.g. "laptop" or "ci" — shown back in listTokens so a user can
	// tell which one to revoke without having kept the plaintext.
	Name string `json:"name"`
}

type issueTokenResponse struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"` // plaintext — shown exactly once, never retrievable again
}

func (h *Handler) issueToken(w http.ResponseWriter, r *http.Request, user *session.User) {
	var req issueTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	id, token, err := h.Tokens.Issue(user.ID, req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusCreated, issueTokenResponse{ID: id, Name: req.Name, Token: token})
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request, user *session.User) {
	tokens, err := h.Tokens.List(user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request, user *session.User) {
	tokenID, err := strconv.ParseInt(r.PathValue("tokenId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid tokenId", http.StatusBadRequest)
		return
	}
	if err := h.Tokens.Revoke(user.ID, tokenID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- CLI browser login (onagent login --web) -----------------------------------
//
// Four routes implement the handoff described in internal/cliauth's
// package doc: the CLI registers its (validated, loopback-only)
// redirect_uri out of band via start, before it has any credential at
// all; the browser only ever carries the resulting opaque id; approve
// mints the actual token server-side once the user consents; and the
// CLI's own local callback server collects it via exchange, once, right
// after the browser redirects back with that id.

type startCliAuthRequest struct {
	RedirectURI string `json:"redirectUri"`
	Name        string `json:"name"`
}

type startCliAuthResponse struct {
	ID string `json:"id"`
}

func (h *Handler) startCliAuth(w http.ResponseWriter, r *http.Request) {
	var req startCliAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	id, err := h.CliAuth.Start(req.RedirectURI, req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, startCliAuthResponse{ID: id})
}

type getCliAuthResponse struct {
	// Name is the only thing this endpoint reveals about a session —
	// enough for CliAuthPage to render "the {name} CLI wants to sign in"
	// without needing redirect_uri (or anything else sensitive) in the
	// page's own URL or any response a page script can read.
	Name string `json:"name"`
}

func (h *Handler) getCliAuth(w http.ResponseWriter, r *http.Request) {
	name, ok := h.CliAuth.NameFor(r.PathValue("id"))
	if !ok {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, getCliAuthResponse{Name: name})
}

type approveCliAuthResponse struct {
	// RedirectURI is where the front-end sends the browser next (with
	// ?code={id} appended) — looked up server-side from what start
	// registered, never re-derived from the page's own URL.
	RedirectURI string `json:"redirectUri"`
}

func (h *Handler) approveCliAuth(w http.ResponseWriter, r *http.Request, user *session.User) {
	id := r.PathValue("id")

	name, ok := h.CliAuth.NameFor(id)
	if !ok {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}

	_, token, err := h.Tokens.Issue(user.ID, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	redirectURI, ok := h.CliAuth.Approve(id, token)
	if !ok {
		// The minted token above was never persisted anywhere or shown to
		// anyone — Approve failing just means it's discarded here, not a
		// leak. See Approve's doc comment for why double-approval is
		// rejected rather than re-collected.
		http.Error(w, "session already used or expired", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, approveCliAuthResponse{RedirectURI: redirectURI})
}

type exchangeCliAuthResponse struct {
	Token string `json:"token"` // plaintext — shown exactly once, never retrievable again
}

func (h *Handler) exchangeCliAuth(w http.ResponseWriter, r *http.Request) {
	token, ok := h.CliAuth.Exchange(r.PathValue("id"))
	if !ok {
		http.Error(w, "not approved yet, or already collected", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, exchangeCliAuthResponse{Token: token})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
