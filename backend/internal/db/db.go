// Package db owns the Postgres connection and schema for this backend's
// own state (apps, tools, API key hashes). internal/toolschema and
// internal/auth are the only other packages that touch it — everything
// else keeps talking to those packages' existing Go types, not to SQL.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "github.com/lib/pq"
)

//go:embed schema.sql
var schemaSQL string

// Open connects to Postgres at dsn and applies schema.sql. Safe to call on
// every startup: every statement in schema.sql is idempotent (CREATE ... IF
// NOT EXISTS), so this never fails or duplicates state on a database that
// already has the schema from a previous run.
func Open(dsn string) (*sql.DB, error) {
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	if _, err := conn.Exec(schemaSQL); err != nil {
		conn.Close()
		return nil, fmt.Errorf("db: apply schema: %w", err)
	}
	return conn, nil
}
