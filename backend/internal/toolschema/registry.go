package toolschema

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
)

// Registry is a thread-safe, database-backed holder for the set of
// registered apps. Introduced so the admin API (internal/admin) can
// create/update/delete an app's tools and have every consumer — the
// WebSocket handler, the codegen HTTP endpoints — see the change without a
// process restart. Originally backed by backend/tools/*.yaml on disk;
// now backed by Postgres (internal/db) so state survives across backend
// instances and isn't lost if the filesystem it ran on disappears.
//
// All reads/writes go through an in-memory cache refreshed by Reload,
// rather than hitting the database on every WebSocket hello — a session
// resolving its tool set shouldn't pay a query per connection.
type Registry struct {
	db   *sql.DB
	mu   sync.RWMutex
	apps map[string]*App
}

// NewRegistry loads every app from db once and returns a Registry serving
// that snapshot.
func NewRegistry(db *sql.DB) (*Registry, error) {
	r := &Registry{db: db}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Get returns the app for id, and whether it was found.
func (r *Registry) Get(id string) (*App, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	app, ok := r.apps[id]
	return app, ok
}

// All returns a snapshot copy of every loaded app, keyed by appId. Safe to
// range over without holding the Registry's lock — callers get their own
// map, not a reference into internal state.
func (r *Registry) All() map[string]*App {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*App, len(r.apps))
	for k, v := range r.apps {
		out[k] = v
	}
	return out
}

// Reload re-reads every app and its tools from the database and atomically
// swaps them into memory. On error, the previous in-memory set is left
// untouched.
func (r *Registry) Reload() error {
	apps, err := loadAllApps(r.db)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.apps = apps
	r.mu.Unlock()
	return nil
}

// Save validates app, upserts it and replaces its tool set in the
// database within a single transaction, and reloads the registry so the
// change is visible immediately. Returns the validate/write error without
// touching in-memory state if either fails.
func (r *Registry) Save(app *App) error {
	if err := app.Validate(); err != nil {
		return fmt.Errorf("toolschema: refusing to save invalid app: %w", err)
	}
	if err := saveApp(r.db, app); err != nil {
		return err
	}
	return r.Reload()
}

// Delete removes an app and its tools (ON DELETE CASCADE) from the
// database and reloads. Deleting an app that doesn't exist is not an
// error — deleting something already gone is the caller's desired end
// state either way.
func (r *Registry) Delete(appID string) error {
	if !ValidAppID(appID) {
		return fmt.Errorf("toolschema: invalid appId %q", appID)
	}
	if _, err := r.db.Exec(`DELETE FROM apps WHERE app_id = $1`, appID); err != nil {
		return fmt.Errorf("toolschema: delete app %s: %w", appID, err)
	}
	return r.Reload()
}

// Create inserts a brand-new app owned by ownerID with no tools yet, and
// reloads. Fails if appID already exists — unlike Save, which is
// upsert-and-replace-tools for editing an app the caller already knows
// exists, Create is specifically "this must be a new app," so the admin API
// can tell "created" apart from "already existed, tools replaced."
//
// ownerID isn't part of the App type (see toolschema/schema.go) because
// ownership is an admin-API-only concern — the WebSocket handler and public
// codegen endpoints that read through Registry.Get/All never need to know
// who owns what, only what an app's tools are.
func (r *Registry) Create(appID string, ownerID int64) error {
	if !ValidAppID(appID) {
		return fmt.Errorf("toolschema: invalid appId %q", appID)
	}
	if _, exists := r.Get(appID); exists {
		return fmt.Errorf("toolschema: appId %q already exists", appID)
	}
	if _, err := r.db.Exec(
		`INSERT INTO apps (app_id, owner_id) VALUES ($1, $2)`, appID, ownerID,
	); err != nil {
		return fmt.Errorf("toolschema: create app %s: %w", appID, err)
	}
	return r.Reload()
}

// OwnerOf returns the user id that owns appID, or ok=false if the app
// doesn't exist or (only possible for apps migrated before multi-user
// existed) has no owner recorded.
func (r *Registry) OwnerOf(appID string) (ownerID int64, ok bool) {
	var id sql.NullInt64
	err := r.db.QueryRow(`SELECT owner_id FROM apps WHERE app_id = $1`, appID).Scan(&id)
	if err != nil || !id.Valid {
		return 0, false
	}
	return id.Int64, true
}

// OwnedBy returns every appId owned by ownerID, for a user's app list.
func (r *Registry) OwnedBy(ownerID int64) ([]string, error) {
	rows, err := r.db.Query(`SELECT app_id FROM apps WHERE owner_id = $1 ORDER BY app_id`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("toolschema: query apps for owner %d: %w", ownerID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("toolschema: scan app_id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- database access ---------------------------------------------------

func loadAllApps(db *sql.DB) (map[string]*App, error) {
	appRows, err := db.Query(`SELECT app_id FROM apps ORDER BY app_id`)
	if err != nil {
		return nil, fmt.Errorf("toolschema: query apps: %w", err)
	}
	defer appRows.Close()

	apps := make(map[string]*App)
	var ids []string
	for appRows.Next() {
		var id string
		if err := appRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("toolschema: scan app_id: %w", err)
		}
		apps[id] = &App{AppID: id, Tools: []Tool{}}
		ids = append(ids, id)
	}
	if err := appRows.Err(); err != nil {
		return nil, fmt.Errorf("toolschema: iterate apps: %w", err)
	}

	toolRows, err := db.Query(`
		SELECT app_id, name, description, parameters, returns
		  FROM tools
		 ORDER BY app_id, position`)
	if err != nil {
		return nil, fmt.Errorf("toolschema: query tools: %w", err)
	}
	defer toolRows.Close()

	for toolRows.Next() {
		var (
			appID, name, description string
			parametersJSON           []byte
			returnsJSON              []byte // NULL-able
		)
		if err := toolRows.Scan(&appID, &name, &description, &parametersJSON, &returnsJSON); err != nil {
			return nil, fmt.Errorf("toolschema: scan tool: %w", err)
		}

		var params ParameterSchema
		if err := json.Unmarshal(parametersJSON, &params); err != nil {
			return nil, fmt.Errorf("toolschema: unmarshal parameters for %s.%s: %w", appID, name, err)
		}

		tool := Tool{Name: name, Description: description, Parameters: params}
		if returnsJSON != nil {
			var ret ParameterSchema
			if err := json.Unmarshal(returnsJSON, &ret); err != nil {
				return nil, fmt.Errorf("toolschema: unmarshal returns for %s.%s: %w", appID, name, err)
			}
			tool.Returns = &ret
		}

		app, ok := apps[appID]
		if !ok {
			// A tool row referencing an app_id not in `apps` would mean the
			// foreign key / cascade delete didn't do its job — a schema
			// invariant violation, not a normal runtime condition.
			return nil, fmt.Errorf("toolschema: tool %s.%s references missing app", appID, name)
		}
		app.Tools = append(app.Tools, tool)
	}
	if err := toolRows.Err(); err != nil {
		return nil, fmt.Errorf("toolschema: iterate tools: %w", err)
	}

	return apps, nil
}

// saveApp upserts the app row and replaces its entire tool set inside one
// transaction, so a Registry.Reload can never observe a half-written
// save (e.g. old tools deleted but new ones not yet inserted).
func saveApp(db *sql.DB, app *App) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("toolschema: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op if already committed

	if _, err := tx.Exec(
		`INSERT INTO apps (app_id) VALUES ($1) ON CONFLICT (app_id) DO NOTHING`,
		app.AppID,
	); err != nil {
		return fmt.Errorf("toolschema: upsert app %s: %w", app.AppID, err)
	}

	// Replace-all semantics: saveApp always receives the tool editor's full
	// intended tool list, so deleting the old set and inserting the new one
	// is simpler and less error-prone than diffing for adds/removes/renames.
	if _, err := tx.Exec(`DELETE FROM tools WHERE app_id = $1`, app.AppID); err != nil {
		return fmt.Errorf("toolschema: clear tools for %s: %w", app.AppID, err)
	}

	for i, t := range app.Tools {
		paramsJSON, err := json.Marshal(t.Parameters)
		if err != nil {
			return fmt.Errorf("toolschema: marshal parameters for %s: %w", t.Name, err)
		}
		var returnsJSON []byte
		if t.Returns != nil {
			returnsJSON, err = json.Marshal(t.Returns)
			if err != nil {
				return fmt.Errorf("toolschema: marshal returns for %s: %w", t.Name, err)
			}
		}
		if _, err := tx.Exec(
			`INSERT INTO tools (app_id, name, description, parameters, returns, position)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			app.AppID, t.Name, t.Description, paramsJSON, returnsJSON, i,
		); err != nil {
			return fmt.Errorf("toolschema: insert tool %s: %w", t.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("toolschema: commit: %w", err)
	}
	return nil
}
