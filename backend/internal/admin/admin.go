// Package admin exposes the management API the tool-editor front-end (or
// any other admin client) uses to register/log in, create apps, edit their
// tool definitions, and issue/revoke API keys.
//
// Every route (other than /auth/register and /auth/login themselves)
// requires a valid session cookie (internal/session), and every app-scoped
// operation checks that the calling user owns the app before touching it —
// this is the actual multi-tenant boundary. There is no super-admin
// override: a user can only ever see and modify apps they created.
// (An earlier version of this package used one shared ADMIN_TOKEN with no
// per-app ownership at all; ownership checking is what replaced it.)
package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tim72117/agent-tool-platform/internal/auth"
	"github.com/tim72117/agent-tool-platform/internal/session"
	"github.com/tim72117/agent-tool-platform/internal/toolschema"
)

// Handler serves the /admin/* and /auth/* APIs.
type Handler struct {
	Apps    *toolschema.Registry
	Auth    *auth.Store
	Session *session.Store
}

func NewHandler(apps *toolschema.Registry, authStore *auth.Store, sessionStore *session.Store) *Handler {
	return &Handler{Apps: apps, Auth: authStore, Session: sessionStore}
}

// Register mounts the auth and admin routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/register", h.register)
	mux.HandleFunc("POST /auth/login", h.login)
	mux.HandleFunc("POST /auth/logout", h.logout)
	mux.HandleFunc("GET /auth/me", h.withAuth(h.me))

	mux.HandleFunc("GET /admin/apps", h.withAuth(h.listApps))
	mux.HandleFunc("POST /admin/apps", h.withAuth(h.createApp))
	mux.HandleFunc("GET /admin/apps/{appId}", h.withOwnedApp(h.getApp))
	mux.HandleFunc("PUT /admin/apps/{appId}/tools", h.withOwnedApp(h.saveTools))
	mux.HandleFunc("PUT /admin/apps/{appId}/origin", h.withOwnedApp(h.setOrigin))
	mux.HandleFunc("DELETE /admin/apps/{appId}", h.withOwnedApp(h.deleteApp))
	mux.HandleFunc("POST /admin/apps/{appId}/key", h.withOwnedApp(h.issueKey))
	mux.HandleFunc("DELETE /admin/apps/{appId}/key", h.withOwnedApp(h.revokeKey))
}

// withAuth resolves the session cookie and rejects the request if it's
// missing/expired. Handlers that need the user read it back via
// h.userFrom(r) — Go's stdlib http.HandlerFunc has no room for an extra
// return value, so it rides in via request context like any other
// middleware-injected value.
func (h *Handler) withAuth(next func(http.ResponseWriter, *http.Request, *session.User)) http.HandlerFunc {
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

// --- apps ------------------------------------------------------------------

// appSummary is what listApps returns per app: enough for a dashboard list
// view without shipping every tool's full schema.
type appSummary struct {
	AppID         string `json:"appId"`
	ToolCount     int    `json:"toolCount"`
	HasKey        bool   `json:"hasKey"`
	AllowedOrigin string `json:"allowedOrigin"` // "" means unset (fail-closed — see ws.Handler.ServeHTTP)
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
	writeJSON(w, http.StatusOK, appSummary{
		AppID:         appID,
		ToolCount:     len(tools),
		HasKey:        h.Auth.HasKey(appID),
		AllowedOrigin: h.Auth.OriginFor(appID),
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

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
