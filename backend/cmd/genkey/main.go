// Command genkey issues a new API key for an app from the command line: it
// prints the plaintext key once (nothing else ever will) and stores its
// hash in the apps table, via auth.Store.Issue — the same path the admin
// API (internal/admin) uses, so a key issued here and one issued through
// the dashboard are indistinguishable. The app must already exist (create
// it first via the admin API or the tool-editor UI).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tim72117/agent-tool-platform/internal/auth"
	"github.com/tim72117/agent-tool-platform/internal/db"
)

func main() {
	dsn := flag.String("db", envOr("DATABASE_URL", "postgres://platform:platform@localhost:5434/platform?sslmode=disable"), "database connection string")
	flag.Parse()

	appID := flag.Arg(0)
	if appID == "" {
		fmt.Fprintln(os.Stderr, "usage: genkey [-db <dsn>] <appId>")
		os.Exit(1)
	}

	conn, err := db.Open(*dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "genkey:", err)
		os.Exit(1)
	}
	defer conn.Close()

	key, err := auth.New(conn).Issue(appID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "genkey:", err)
		os.Exit(1)
	}

	fmt.Printf("Issued key for %q\n\n", appID)
	fmt.Printf("API key (shown once — save it now):\n\n  %s\n\n", key)
	fmt.Printf("Give this to the developer to configure AgentBridge({ apiKey: %q, ... }).\n", key)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
