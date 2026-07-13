// Static frontend hosting: embeds the built assets of the two Vite
// front-ends (apps/landing, apps/console) into the server binary and
// serves them.
//
// # Why embed.FS pointed at cmd/server/web/*
//
// apps/landing and apps/console are independent Vite projects, each built
// with its own `npm run build` into its own dist/ — there's no single
// build step that produces both together, and go:embed can only embed
// paths that exist on disk *at `go build` time*, relative to this package.
//
// So the two directories below (cmd/server/web/landing, cmd/server/web/console)
// are the hand-off point: the Dockerfile is expected to run
//
//	cd apps/landing && npm run build
//	cd apps/console && npm run build
//	cp -r apps/landing/dist/. backend/cmd/server/web/landing/
//	cp -r apps/console/dist/. backend/cmd/server/web/console/
//
// (or equivalent) *before* `go build ./cmd/server`, so that by the time
// //go:embed below runs, these directories contain the real built sites.
//
// # Local development
//
// Neither directory is populated by anything in this repo's normal dev
// loop — only checked-in placeholder index.html files live there (see the
// comment atop each), because go:embed fails the build outright on a
// completely empty directory. That means the "/" and "/app" routes on a
// locally-run `go run ./cmd/server` will only ever serve those
// placeholders, never real content. For real frontend development, run
// each app's own Vite dev server instead:
//
//	cd apps/landing && npm run dev
//	cd apps/console && npm run dev
//
// main() logs at startup whether each embedded tree looks like a real
// build or just the placeholder, so this isn't a silent trap.
package main

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
)

// landingFS and consoleFS hold whatever was on disk under cmd/server/web/*
// at compile time — either a real Vite build (production/Docker) or just
// the placeholder index.html (local `go build`/`go run`). The "all:"
// prefix is required so files/dirs starting with "." (e.g. Vite's
// occasional dotfiles) aren't silently skipped by the default embed
// pattern.
//
//go:embed all:web/landing
var landingFS embed.FS

//go:embed all:web/console
var consoleFS embed.FS

// placeholderMarker is a byte sequence unique to the checked-in placeholder
// files (see cmd/server/web/{landing,console}/index.html) and never present
// in a real Vite build's index.html. Used only to decide what to log at
// startup — never affects request handling.
const placeholderMarker = "Placeholder only"

// isPlaceholder reports whether sub (rooted at "landing" or "console"
// inside embedFS) looks like the checked-in placeholder rather than a real
// build: true if index.html is missing, unreadable, or contains the
// placeholder marker.
func isPlaceholder(sub fs.FS) bool {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return true
	}
	return strings.Contains(string(data), placeholderMarker)
}

// mountStatic wires the landing site at "/" and the console SPA at "/app"
// onto mux. Registered after every API route in main() (mux.Handle order
// doesn't matter for Go's ServeMux — it's pattern-specificity based, not
// registration-order based — but keeping it last here mirrors "APIs are
// primary, static hosting is the fallback" for readability).
//
// Route shapes deliberately avoid colliding with existing API prefixes
// (/ws, /apps/{appId}/..., /console/*, /auth/*, /healthz):
//   - "/" is registered as a plain http.FileServerFS subtree handler
//     (Go 1.22+ ServeMux: a pattern ending in "/" is a subtree match) so it
//     serves the landing page's index.html at the root *and* its asset
//     files (e.g. "/assets/x.js"). This is safe alongside the API routes
//     above because Go's ServeMux dispatches by longest-match specificity,
//     not registration order: a request for "/apps/foo/tools.json" always
//     matches the more specific "/apps/{appId}/tools.json" pattern over
//     the "/" catch-all, and likewise for "/console/*", "/auth/*", "/ws",
//     "/healthz". None of landing's real asset paths collide with those
//     prefixes (landing's build output only ever emits paths like
//     "/", "/assets/...", "/favicon.ico", etc).
//   - "/app" and "/app/" are both handled explicitly, and everything else
//     lives under the "/app/" subtree pattern — none of that overlaps any
//     existing route since nothing else starts with "/app".
func mountStatic(mux *http.ServeMux, log *slog.Logger) {
	landingRoot, err := fs.Sub(landingFS, "web/landing")
	if err != nil {
		// Can't happen: "web/landing" is a literal go:embed'd path.
		log.Error("static: bad landing embed root", "err", err)
		landingRoot = landingFS
	}
	consoleRoot, err := fs.Sub(consoleFS, "web/console")
	if err != nil {
		// Can't happen: "web/console" is a literal go:embed'd path.
		log.Error("static: bad console embed root", "err", err)
		consoleRoot = consoleFS
	}

	landingReal := !isPlaceholder(landingRoot)
	consoleReal := !isPlaceholder(consoleRoot)
	log.Info("embedded frontend static assets",
		"landing", embedStatus(landingReal),
		"console", embedStatus(consoleReal),
	)

	mountLanding(mux, landingRoot)
	mountConsole(mux, consoleRoot, consoleReal)
}

func embedStatus(real bool) string {
	if real {
		return "real build"
	}
	return "placeholder only (see cmd/server/web.go)"
}

// mountLanding serves apps/landing's build as plain static files at "/".
// No SPA fallback: landing has no client-side router, it's one index.html
// plus its asset files, so a request for a path that isn't an actual file
// should 404 like any normal static host, not be rewritten to index.html.
func mountLanding(mux *http.ServeMux, root fs.FS) {
	fileServer := http.FileServerFS(root)
	mux.Handle("/", fileServer)
}

// mountConsole serves apps/console's build at the "/app" prefix, with SPA
// fallback: any /app/* request that doesn't resolve to a real static file
// gets /app's index.html instead, so the console's own client-side
// pathname check (apps/console/src/main.tsx) can pick the right screen
// (e.g. /app/cli-auth) — see that file's comment for the routing contract
// this depends on.
func mountConsole(mux *http.ServeMux, root fs.FS, consoleReal bool) {
	fileServer := http.StripPrefix("/app", http.FileServerFS(root))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !consoleReal {
			// No real build embedded (local `go build`/`go run` without the
			// Docker copy step) — serving the placeholder as if it were a
			// working SPA would be misleading for every sub-path, and the
			// placeholder file itself explains what's going on, so route
			// everything to it via the file server rather than 404ing paths
			// like /app/cli-auth that would work fine in production.
			fileServer.ServeHTTP(w, r)
			return
		}

		upath := strings.TrimPrefix(r.URL.Path, "/app")
		upath = strings.TrimPrefix(upath, "/")
		if upath != "" {
			if f, err := root.Open(upath); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// Not a real static asset under the console build: SPA fallback to
		// /app's index.html so the client-side router in main.tsx decides
		// what to render (e.g. /app/cli-auth). Serve index.html directly
		// rather than rewriting the request path, since the actual URL the
		// browser requested (e.g. /app/cli-auth) must stay intact for
		// main.tsx's window.location.pathname check to work.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFileFS(w, r, root, "index.html")
	})

	mux.Handle("/app", handler)
	mux.Handle("/app/", handler)
}
