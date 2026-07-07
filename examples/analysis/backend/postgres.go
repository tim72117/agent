package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/url"
	"regexp"

	_ "github.com/lib/pq"
)

// openPostgresDB opens a *sql.DB backed by Postgres that questions.go can
// use exactly as it uses the SQL Server *sql.DB: same @p1-style query text,
// same three queries (file_analysis, INFORMATION_SCHEMA.COLUMNS,
// file_ques_page), same result shape. questions.go is intentionally left
// untouched — this file adapts around it instead of the other way around.
//
// Two SQL Server-isms in questions.go don't have a Postgres equivalent and
// are rewritten transparently here:
//
//  1. Named parameters (@p1) -> positional ($1). Postgres' pq driver only
//     understands $N placeholders.
//  2. The cross-database column lookup
//     "analysis_data.INFORMATION_SCHEMA.COLUMNS WHERE TABLE_NAME = @p1"
//     is SQL Server syntax for reaching into another database. Postgres has
//     no equivalent (a database can only query its own catalog), so this
//     schema puts the equivalent lookup in a same-database view,
//     analysis_columns(table_name, column_name), and the driver rewrites
//     the query to select from it instead. See postgres_schema.sql for the
//     table/view definitions this expects.
func openPostgresDB(dsn string) (*sql.DB, error) {
	connector, err := newPGRewriteConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

// buildPostgresDSN builds a postgres:// connection string from the same env
// var names main.go already reads for SQL Server (DB_HOST/DB_PORT/
// DB_DATABASE/DB_USERNAME/DB_PASSWORD), so switching DB_DRIVER=postgres
// doesn't require renaming anything in .env.
func buildPostgresDSN(host, port, database, username, password string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   host + ":" + port,
		Path:   "/" + database,
	}
	q := url.Values{}
	q.Add("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

var namedParamRE = regexp.MustCompile(`@p(\d+)`)

// rewriteQuery translates the two SQL Server-isms questions.go emits into
// their Postgres equivalents. Any query without either pattern passes
// through unchanged.
func rewriteQuery(q string) string {
	q = crossDBColumnsRE.ReplaceAllString(q, "analysis_columns")
	q = namedParamRE.ReplaceAllStringFunc(q, func(m string) string {
		n := namedParamRE.FindStringSubmatch(m)[1]
		return "$" + n
	})
	return q
}

var crossDBColumnsRE = regexp.MustCompile(`analysis_data\.INFORMATION_SCHEMA\.COLUMNS`)

// --- driver.Connector / driver.Driver wiring -------------------------------
//
// database/sql calls Connector.Connect() to get a driver.Conn per pooled
// connection, then routes every Prepare/Query/Exec through that Conn. We
// wrap lib/pq's Conn so PrepareContext rewrites query text before handing it
// to pq — that's the one place all of questions.go's db.QueryRow/db.Query
// calls funnel through.

type pgRewriteConnector struct {
	dsn    string
	driver driver.Driver
}

func newPGRewriteConnector(dsn string) (driver.Connector, error) {
	// pq registers itself under "postgres"; sql.Open would look it up by
	// name, but we go through driver.Driver directly since we need to wrap
	// the connections it produces.
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	drv := db.Driver()
	_ = db.Close() // only needed to obtain the registered driver.Driver
	return &pgRewriteConnector{dsn: dsn, driver: drv}, nil
}

func (c *pgRewriteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &pgRewriteConn{Conn: conn}, nil
}

func (c *pgRewriteConnector) Driver() driver.Driver { return c.driver }

type pgRewriteConn struct {
	driver.Conn
}

func (c *pgRewriteConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(rewriteQuery(query))
}

func (c *pgRewriteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, rewriteQuery(query))
	}
	return c.Prepare(query)
}

// QueryContext/ExecContext let *sql.DB skip Prepare for one-shot
// db.Query/db.QueryRow calls (which is what questions.go uses); without
// these, the sql package would still work by falling back to
// PrepareContext+Query, but implementing them directly avoids an extra
// round trip and keeps the rewrite applied uniformly regardless of which
// path the sql package picks.
func (c *pgRewriteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, rewriteQuery(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *pgRewriteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, rewriteQuery(query), args)
	}
	return nil, driver.ErrSkip
}

