package ws

import (
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/tim72117/agent-tool-platform/internal/auth"
	"github.com/tim72117/agent-tool-platform/internal/inference"
	"github.com/tim72117/agent-tool-platform/internal/toolschema"
)

// Handler upgrades HTTP connections to WebSocket sessions for the Agent
// Bridge SDK. Unlike CORS-protected fetch/XHR, a WebSocket handshake is
// NOT gated by the browser: any page can attempt to open a connection to
// any origin. The server is the only line of defense, so AllowedOrigins is
// enforced here rather than left to browser behavior.
//
// Auth, if configured, is the other half of that defense: it decides which
// appId a connection is allowed to act as, rather than trusting whatever
// appId the client claims in its `hello` message (see session.go — the
// server-resolved appId from here always wins over that field).
type Handler struct {
	Apps           *toolschema.Registry
	Inference      inference.Service
	Log            *slog.Logger
	AllowedOrigins OriginChecker
	Auth           *auth.Store // nil disables auth: any appId is accepted, dev/mock mode only

	upgrader websocket.Upgrader
}

// OriginChecker decides whether a WebSocket handshake from the given Origin
// header should be accepted.
type OriginChecker func(origin string) bool

// AllowAllOrigins accepts every origin. Only appropriate for local
// development; production deployments should pass a real allowlist backed
// by each developer app's registered domains.
func AllowAllOrigins(string) bool { return true }

func NewHandler(apps *toolschema.Registry, infer inference.Service, log *slog.Logger, allowed OriginChecker, authStore *auth.Store) *Handler {
	h := &Handler{
		Apps:           apps,
		Inference:      infer,
		Log:            log,
		AllowedOrigins: allowed,
		Auth:           authStore,
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				// Non-browser clients (curl, server-to-server) send no
				// Origin header; allow them through to the app-level
				// appId check in the hello message instead.
				return true
			}
			return h.AllowedOrigins(origin)
		},
	}
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Browsers cannot attach custom headers to a WebSocket upgrade request,
	// so the API key travels as a query parameter instead of Authorization.
	// This is why TLS (wss://) is not optional for any deployment that
	// enables Auth — an unencrypted connection would put the key on the
	// wire, and often in server access logs, in plaintext.
	var appID string
	if h.Auth != nil {
		origin := r.Header.Get("Origin")
		token := r.URL.Query().Get("token")
		result, ok := h.Auth.Verify(token)
		if !ok {
			h.Log.Info("ws handshake rejected: invalid or missing token", "origin", origin)
			http.Error(w, "invalid or missing token", http.StatusUnauthorized)
			return
		}
		if _, known := h.Apps.Get(result.AppID); !known {
			h.Log.Warn("ws handshake rejected: token resolves to unknown appId", "appId", result.AppID)
			http.Error(w, "unknown app", http.StatusUnauthorized)
			return
		}
		// Per-app origin binding: this app's key only authenticates
		// connections presenting the exact Origin it was configured with
		// (admin's SetOrigin). No origin configured means every connection
		// for this app is rejected (fail-closed) rather than falling back
		// to the global AllowedOrigins check — a key stolen from one site
		// must not work when replayed from another just because that other
		// site happens to also be on the global allowlist.
		if result.AllowedOrigin == "" {
			h.Log.Warn("ws handshake rejected: app has no allowed origin configured", "appId", result.AppID)
			http.Error(w, "app is not configured to accept connections from any site yet", http.StatusForbidden)
			return
		}
		if origin != result.AllowedOrigin {
			h.Log.Info("ws handshake rejected: origin does not match app's configured origin",
				"appId", result.AppID, "origin", origin, "allowedOrigin", result.AllowedOrigin)
			http.Error(w, "origin not allowed for this app", http.StatusForbidden)
			return
		}
		appID = result.AppID
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.Log.Info("ws upgrade rejected", "err", err, "origin", r.Header.Get("Origin"))
		return
	}
	NewSession(r.Context(), conn, h.Apps, h.Inference, h.Log, appID)
}
