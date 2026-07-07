-- Schema for the agent-tool-platform backend's own state: registered apps,
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

CREATE TABLE IF NOT EXISTS apps (
    app_id         TEXT PRIMARY KEY,
    owner_id       BIGINT REFERENCES users (id) ON DELETE CASCADE, -- NULL only for apps migrated before multi-user existed
    api_key_hash   TEXT,              -- sha256 hex, NULL until a key is issued
    allowed_origin TEXT,              -- exact Origin header a connection must present; NULL = no site configured yet, so every WS handshake for this app is rejected (fail-closed) — see ws.Handler.ServeHTTP
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ALTER ... ADD COLUMN IF NOT EXISTS so this stays idempotent for databases
-- created before these columns existed (CREATE TABLE IF NOT EXISTS above is
-- a no-op against an existing table and would silently skip new columns
-- otherwise).
ALTER TABLE apps ADD COLUMN IF NOT EXISTS allowed_origin TEXT;
ALTER TABLE apps ADD COLUMN IF NOT EXISTS owner_id BIGINT REFERENCES users (id) ON DELETE CASCADE;

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
