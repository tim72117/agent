// Package auth binds each WebSocket connection to a single, server-verified
// appId, closing the impersonation gap in the original design: previously a
// client's `hello` message could claim any appId, and the backend simply
// believed it (see protocol.HelloPayload.AppID). That let anyone who knew or
// guessed an appId open a session under it and spend that developer's
// inference quota.
//
// This is deliberately the minimal fix — one static API key per app, hashed
// at rest, checked at WebSocket handshake time — not a full account/session
// system. Originally backed by backend/apps/*.json on disk; now backed by
// Postgres (internal/db, the same `apps` table internal/toolschema.Registry
// uses for tool definitions) so key state survives across backend instances
// and both an app and its key live/die together in one place.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
)

// appIDRE mirrors toolschema's app-id pattern (kept local so auth stays
// dependency-free): appIDs are used as SQL values, and validating them here
// too means a malformed appId is rejected at the auth boundary even if some
// future caller skips toolschema's own check.
var appIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Store looks up the appId bound to an API key. Every method talks directly
// to Postgres — there's no in-memory cache to keep consistent, since key
// checks are infrequent (once per WebSocket handshake, not once per
// message) and a stale cache here is exactly the kind of bug that would let
// a revoked key keep working.
type Store struct {
	db *sql.DB
}

// New wraps db for key verification/issuance. Assumes the `apps` table
// already exists (internal/db.Open applies the schema before this is ever
// constructed).
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Count returns the number of apps with an active API key, for startup
// logging.
func (s *Store) Count() int {
	var n int
	// Errors here are startup-logging-only; a failed count shouldn't stop
	// the server, so it's swallowed to zero rather than propagated as
	// Count has no error return (kept for call-site simplicity elsewhere).
	_ = s.db.QueryRow(`SELECT count(*) FROM apps WHERE api_key_hash IS NOT NULL`).Scan(&n)
	return n
}

// VerifyResult is what a successful Verify reveals about the key's app —
// bundled together so ws.Handler makes exactly one query per handshake
// instead of Verify-then-OriginFor as two round trips.
type VerifyResult struct {
	AppID string
	// AllowedOrigin is the exact Origin header this app's connections must
	// present, or "" if the app has none configured yet. ws.Handler treats
	// "" as fail-closed — reject every handshake — rather than "no
	// restriction", since a freshly created app having no origin bound is
	// far more likely to mean "the developer hasn't set it up yet" than
	// "intentionally open to any site". See SetOrigin.
	AllowedOrigin string
}

// Verify checks a plaintext API key and returns the appId it's bound to,
// plus that app's origin restriction. The hash lookup is an indexed
// equality match (apps_api_key_hash_idx), not a linear scan — Postgres does
// the constant-time-irrelevant part (index lookup) while the hash itself
// already removes the need for subtle.ConstantTimeCompare that the old
// in-memory version used: hashing the input is what defeats timing attacks
// on the raw key, and hash equality is checked by an opaque B-tree
// comparison rather than user-controlled Go code.
func (s *Store) Verify(apiKey string) (result VerifyResult, ok bool) {
	if apiKey == "" {
		return VerifyResult{}, false
	}
	hash := HashKey(apiKey)

	var id string
	var origin sql.NullString
	err := s.db.QueryRow(
		`SELECT app_id, allowed_origin FROM apps WHERE api_key_hash = $1`, hash,
	).Scan(&id, &origin)
	if err != nil {
		return VerifyResult{}, false
	}
	return VerifyResult{AppID: id, AllowedOrigin: origin.String}, true
}

// HasKey reports whether appID currently has an active key, without
// revealing it.
func (s *Store) HasKey(appID string) bool {
	var exists bool
	_ = s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM apps WHERE app_id = $1 AND api_key_hash IS NOT NULL)`,
		appID,
	).Scan(&exists)
	return exists
}

// Issue generates a new random API key for appID, persists its hash
// (overwriting any previous key — an app has exactly one active key at a
// time in this minimal design, so issuing a new one implicitly revokes the
// old), and returns the plaintext key. The caller must show it to the
// developer immediately: it is not retrievable afterward, only its hash is
// ever stored. Fails if appID has no row in `apps` yet — an app must exist
// (toolschema.Registry.Save) before it can have a key.
func (s *Store) Issue(appID string) (plaintextKey string, err error) {
	if !appIDRE.MatchString(appID) {
		return "", fmt.Errorf("auth: invalid appId %q", appID)
	}
	key, err := randomKey()
	if err != nil {
		return "", fmt.Errorf("auth: generate key: %w", err)
	}
	hash := HashKey(key)

	result, err := s.db.Exec(`UPDATE apps SET api_key_hash = $1 WHERE app_id = $2`, hash, appID)
	if err != nil {
		return "", fmt.Errorf("auth: issue key for %s: %w", appID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("auth: issue key for %s: %w", appID, err)
	}
	if n == 0 {
		return "", fmt.Errorf("auth: no such app %q", appID)
	}

	return key, nil
}

// Revoke clears appID's key. Not an error if the app had no key, or
// doesn't exist — either way, the app now has no working key, which is the
// caller's desired end state.
func (s *Store) Revoke(appID string) error {
	if !appIDRE.MatchString(appID) {
		return fmt.Errorf("auth: invalid appId %q", appID)
	}
	if _, err := s.db.Exec(`UPDATE apps SET api_key_hash = NULL WHERE app_id = $1`, appID); err != nil {
		return fmt.Errorf("auth: revoke key for %s: %w", appID, err)
	}
	return nil
}

// SetOrigin sets or clears (origin == "") the exact Origin header appID's
// WebSocket connections must present. An app with no origin set rejects
// every connection regardless of API key — see VerifyResult.AllowedOrigin.
// Fails if appID has no row in `apps` yet, same as Issue.
func (s *Store) SetOrigin(appID, origin string) error {
	if !appIDRE.MatchString(appID) {
		return fmt.Errorf("auth: invalid appId %q", appID)
	}
	var val sql.NullString
	if origin != "" {
		val = sql.NullString{String: origin, Valid: true}
	}
	result, err := s.db.Exec(`UPDATE apps SET allowed_origin = $1 WHERE app_id = $2`, val, appID)
	if err != nil {
		return fmt.Errorf("auth: set origin for %s: %w", appID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: set origin for %s: %w", appID, err)
	}
	if n == 0 {
		return fmt.Errorf("auth: no such app %q", appID)
	}
	return nil
}

// OriginFor returns appID's configured allowed origin, or "" if unset.
func (s *Store) OriginFor(appID string) string {
	var origin sql.NullString
	_ = s.db.QueryRow(`SELECT allowed_origin FROM apps WHERE app_id = $1`, appID).Scan(&origin)
	return origin.String
}

// HashKey computes the sha256 hex digest of a plaintext key — what's
// actually stored, in apps.api_key_hash.
func HashKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

func randomKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
