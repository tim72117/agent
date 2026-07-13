-- Schema for the onagent backend's own state: registered apps,
-- their tool definitions, and API key hashes. Replaces the earlier
-- filesystem-backed design (backend/tools/*.yaml + backend/apps/*.json) —
-- see internal/toolschema and internal/auth, which now read/write through
-- this database instead of the filesystem.
--
-- Applied once at startup by db.Open (idempotent: every statement is
-- CREATE ... IF NOT EXISTS), not by a separate migration tool — the schema
-- is small and stable enough that a hand-rolled migration runner would be
-- more machinery than the problem warrants.

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL, -- bcrypt
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Case-insensitive uniqueness: "Dev@Example.com" and "dev@example.com" must
-- collide at signup, not create two accounts that can never both log in
-- with the "same" address a human would type.
CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_idx ON users (lower(email));

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY, -- opaque random token; also the cookie value
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions (user_id);

-- Long-lived bearer tokens for CLI/API access, distinct from the
-- browser-only session cookie above: a user can hold several (one per
-- machine/CI context), each independently named and revocable, unlike a
-- session which is single-use-until-logout. See internal/usertoken.
CREATE TABLE IF NOT EXISTS user_tokens (
    id           BIGSERIAL PRIMARY KEY, -- what Revoke/List address a token by; token_hash itself is never exposed to a caller
    token_hash   TEXT NOT NULL,         -- sha256 hex of the plaintext token
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,         -- human label, e.g. "laptop" — helps tell tokens apart when revoking
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ            -- updated on each successful Verify; NULL until first use
);

CREATE INDEX IF NOT EXISTS user_tokens_user_id_idx ON user_tokens (user_id);

-- Same rationale as apps_api_key_hash_idx: token_hash must stay globally
-- unique so two tokens can never collide onto the same user by accident,
-- but it's an indexed lookup column (Verify's WHERE clause), not the
-- primary key a caller addresses a token by.
CREATE UNIQUE INDEX IF NOT EXISTS user_tokens_token_hash_idx ON user_tokens (token_hash);

-- Backs the browser-redirect CLI login flow (atp login --web): a
-- short-lived, single-use, opaque handoff between the CLI's local
-- callback server and the console page the user approves in. The id
-- itself (32 random bytes) is the only thing that ever appears in the
-- browser's URL — the real redirect_uri never does, which is what makes a
-- malicious link unable to redirect a freshly minted token anywhere: an
-- attacker has no way to get their own redirect_uri associated with a
-- session id, since that association only happens server-side, in
-- response to the CLI's own POST /console/cli-auth/start call.
CREATE TABLE IF NOT EXISTS cli_auth_sessions (
    id           TEXT PRIMARY KEY,       -- opaque, what appears in URLs
    redirect_uri TEXT NOT NULL,          -- validated (loopback-only) at creation; never re-derived from anything client-supplied afterward
    name         TEXT NOT NULL,          -- token label, e.g. the CLI's hostname
    token        TEXT,                   -- the plaintext usertoken, set once approved; cleared the instant /exchange returns it (single collection)
    approved     BOOLEAN NOT NULL DEFAULT false,
    expires_at   TIMESTAMPTZ NOT NULL,   -- ~10 minutes from creation
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS apps (
    app_id         TEXT PRIMARY KEY,
    owner_id       BIGINT REFERENCES users (id) ON DELETE CASCADE, -- NULL only for apps migrated before multi-user existed
    api_key_hash   TEXT,              -- sha256 hex, NULL until a key is issued
    allowed_origin TEXT,              -- exact Origin header a connection must present; NULL = no site configured yet, so every WS handshake for this app is rejected (fail-closed) — see ws.Handler.ServeHTTP
    thought        TEXT,              -- per-app want agent system prompt; NULL = use the platform default (want_tools.go's defaultThought)
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ALTER ... ADD COLUMN IF NOT EXISTS so this stays idempotent for databases
-- created before these columns existed (CREATE TABLE IF NOT EXISTS above is
-- a no-op against an existing table and would silently skip new columns
-- otherwise).
ALTER TABLE apps ADD COLUMN IF NOT EXISTS allowed_origin TEXT;
ALTER TABLE apps ADD COLUMN IF NOT EXISTS owner_id BIGINT REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE apps ADD COLUMN IF NOT EXISTS thought TEXT;

CREATE INDEX IF NOT EXISTS apps_owner_id_idx ON apps (owner_id);

-- One active key per app in this design (Issue overwrites), but a key must
-- still be globally unique — two apps can never end up authenticating as
-- each other because of a hash collision or a bug that copies a row.
CREATE UNIQUE INDEX IF NOT EXISTS apps_api_key_hash_idx
    ON apps (api_key_hash) WHERE api_key_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS tools (
    app_id      TEXT NOT NULL REFERENCES apps (app_id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL,
    parameters  JSONB NOT NULL, -- toolschema.ParameterSchema, serialized
    returns     JSONB,          -- toolschema.ParameterSchema, serialized; NULL if undeclared
    position    INTEGER NOT NULL, -- preserves declaration order within an app
    PRIMARY KEY (app_id, name)
);
