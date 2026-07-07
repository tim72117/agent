// Package session implements email/password accounts and database-backed
// login sessions for the admin API (internal/admin), replacing the earlier
// single shared ADMIN_TOKEN. Every app now belongs to exactly one user
// (apps.owner_id); internal/admin is responsible for checking that
// ownership on every request, this package only answers "who is making
// this request, if anyone."
package session

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// CookieName is the cookie the admin API's session id travels in. httpOnly
// so page JavaScript can't read it (XSS can't exfiltrate the session even
// if it can run arbitrary code), Secure in production (see Store.Secure).
const CookieName = "atp_session"

const sessionTTL = 30 * 24 * time.Hour

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// ErrInvalidCredentials covers both "no such user" and "wrong password"
// without distinguishing them in the response — telling a caller which one
// failed lets them enumerate registered emails.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrEmailTaken is returned by Register when the email is already in use
// (case-insensitively — see the schema's users_email_lower_idx).
var ErrEmailTaken = errors.New("an account with this email already exists")

// User is the caller-facing shape of an authenticated account. Never
// includes the password hash.
type User struct {
	ID    int64
	Email string
}

// Store implements registration, login, and session verification against
// the users/sessions tables (internal/db/schema.sql).
type Store struct {
	db *sql.DB
	// Secure controls the cookie's Secure attribute. true in any real
	// deployment (HTTPS-only cookie); false only for http://localhost dev,
	// where the browser would otherwise silently refuse to store it.
	Secure bool
}

func New(db *sql.DB, secure bool) *Store {
	return &Store{db: db, Secure: secure}
}

// Register creates a new account. Fails with ErrEmailTaken if the email
// (case-insensitively) is already registered, or a validation error for a
// malformed email / too-short password.
func (s *Store) Register(email, password string) (*User, error) {
	email = strings.TrimSpace(email)
	if !emailRE.MatchString(email) {
		return nil, fmt.Errorf("invalid email address")
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("session: hash password: %w", err)
	}

	var id int64
	err = s.db.QueryRow(
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		email, string(hash),
	).Scan(&id)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" { // unique_violation
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("session: insert user: %w", err)
	}

	return &User{ID: id, Email: email}, nil
}

// Login verifies email/password and returns the matching user.
// ErrInvalidCredentials covers both a nonexistent email and a wrong
// password — see that error's doc comment for why.
func (s *Store) Login(email, password string) (*User, error) {
	var (
		id   int64
		hash string
	)
	err := s.db.QueryRow(
		`SELECT id, password_hash FROM users WHERE lower(email) = lower($1)`, email,
	).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("session: query user: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrInvalidCredentials
	}

	return &User{ID: id, Email: email}, nil
}

// CreateSession mints a new session for userID and sets its cookie on w.
// Returns the raw session id (rarely needed by callers beyond tests — the
// cookie is what actually carries it to the browser).
func (s *Store) CreateSession(w http.ResponseWriter, userID int64) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", fmt.Errorf("session: generate id: %w", err)
	}
	expiresAt := time.Now().Add(sessionTTL)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`,
		id, userID, expiresAt,
	); err != nil {
		return "", fmt.Errorf("session: insert: %w", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: sameSite(s.Secure),
	})

	return id, nil
}

// Verify resolves the session cookie on r, if any, to its user. ok is false
// for a missing cookie, an unknown/expired session, or a user that no
// longer exists (shouldn't happen given ON DELETE CASCADE, but Verify
// doesn't assume it).
func (s *Store) Verify(r *http.Request) (user *User, ok bool) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return nil, false
	}

	var (
		id    int64
		email string
	)
	err = s.db.QueryRow(`
		SELECT users.id, users.email
		  FROM sessions
		  JOIN users ON users.id = sessions.user_id
		 WHERE sessions.id = $1 AND sessions.expires_at > now()`,
		cookie.Value,
	).Scan(&id, &email)
	if err != nil {
		return nil, false
	}

	return &User{ID: id, Email: email}, true
}

// Logout deletes the session named by r's cookie (if any) and clears the
// cookie on w. Not an error if there was no session — logging out twice is
// a no-op, not a failure.
func (s *Store) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(CookieName); err == nil {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id = $1`, cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: sameSite(s.Secure),
	})
}

// sameSite picks the cookie's SameSite attribute to match what the browser
// will actually accept. SameSite=None requires Secure — browsers silently
// drop a None cookie set without it (a real bug this project hit: the
// cookie from CreateSession was never stored in http://localhost dev,
// so every request looked unauthenticated one round trip later even though
// login itself reported success). Secure=true deployments use None because
// the tool-editor origin and the backend origin differ (e.g. a dashboard
// domain calling an api. subdomain) and the fetch is cross-site from the
// cookie's perspective. Secure=false (plain-HTTP local dev) falls back to
// Lax, which browsers do send on the same-site fetches this SPA makes to
// localhost even across different ports.
func sameSite(secure bool) http.SameSite {
	if secure {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

func randomID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
