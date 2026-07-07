package toolschema

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

var nameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// appIDRE constrains appIds to safe characters for use as a SQL identifier
// value and, in the migration path (cmd/migrate) and auth.Store.Issue, a
// filename stem. Path separators, "..", leading dots are all excluded so an
// admin API request can never point outside what's intended.
var appIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidAppID reports whether id is safe to use as an app identifier.
func ValidAppID(id string) bool { return appIDRE.MatchString(id) }

// LoadDir reads every *.yaml/*.yml file in dir as an App definition and
// returns them keyed by AppID. It fails fast on duplicate AppIDs, duplicate
// tool names within an app, or invalid tool names, since these would
// otherwise surface as confusing runtime errors during codegen or dispatch.
//
// Used only by cmd/migrate now to import the pre-database YAML files into
// Postgres; Registry (registry.go) is the live data path.
func LoadDir(dir string) (map[string]*App, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("toolschema: read dir %s: %w", dir, err)
	}

	apps := make(map[string]*App)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		app, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		if _, dup := apps[app.AppID]; dup {
			return nil, fmt.Errorf("toolschema: duplicate appId %q (file %s)", app.AppID, path)
		}
		apps[app.AppID] = app
	}
	return apps, nil
}

// LoadFile reads and validates a single App definition file.
func LoadFile(path string) (*App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("toolschema: read %s: %w", path, err)
	}

	var app App
	if err := yaml.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("toolschema: parse %s: %w", path, err)
	}

	if err := app.Validate(); err != nil {
		return nil, fmt.Errorf("toolschema: %s: %w", path, err)
	}

	return &app, nil
}

// Validate checks the same rules LoadFile enforces (unique/valid tool
// names, required description/parameters.type), independent of where the
// App came from. Used by LoadFile for YAML on disk, and by the admin API
// for an App decoded from a request body — both must reject the same
// malformed input before it ever reaches a file or the in-memory Registry.
func (a *App) Validate() error {
	if !ValidAppID(a.AppID) {
		return fmt.Errorf("invalid appId %q (must match %s)", a.AppID, appIDRE.String())
	}

	seen := make(map[string]bool, len(a.Tools))
	for i, t := range a.Tools {
		if !nameRE.MatchString(t.Name) {
			return fmt.Errorf("tool[%d] has invalid name %q (must match %s)", i, t.Name, nameRE.String())
		}
		if seen[t.Name] {
			return fmt.Errorf("duplicate tool name %q", t.Name)
		}
		seen[t.Name] = true
		if t.Description == "" {
			return fmt.Errorf("tool %q is missing a description", t.Name)
		}
		if t.Parameters.Type == "" {
			return fmt.Errorf("tool %q is missing parameters.type", t.Name)
		}
	}

	return nil
}

