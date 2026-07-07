# Dynamic credential issuance for embedded SDKs

This platform's `AgentBridge` SDK currently reads its API key from a
build-time environment variable (`VITE_AGENT_API_KEY` in
`examples/analysis/frontend/.env`) baked into the bundle. That key is
long-lived: it stays valid until an admin explicitly rotates it via
`POST /admin/apps/{appId}/key`, and every page load reuses the same value.

Several widely-used SDKs solve the "give a browser page a credential"
problem differently — by having the page fetch a short-lived, purpose-scoped
credential from its own backend at load/session time, rather than shipping
one long-lived secret in the build. Notes below on how each does it, for
comparison against this platform's current approach.

## Stripe.js

`Stripe(publishableKey)` takes a key that's designed to be public — it's
safe to hard-code because it can only *create* charges/intents, not read
existing ones. But the actual payment flow doesn't stop there: the
merchant's backend creates a `PaymentIntent`/`SetupIntent` server-side and
returns its `client_secret` to the page for that specific checkout session
only. The publishable key is static; the thing that actually authorizes one
transaction is minted per-request and single-use.

## Twilio Voice/Video SDK

The SDK never receives a long-lived account credential at all. The page
calls the developer's own backend endpoint, which mints a short-lived
**Access Token** — a JWT encoding the specific grants (voice, video, an
identity) and an expiry — using Twilio's account SID/auth token that never
leaves the server. The SDK is initialized with that JWT. This is the
closest precedent to "backend issues, frontend fetches, SDK consumes."

## Firebase / Supabase

The client-side `apiKey` is hard-coded and meant to be public — it only
identifies *which* project, carrying no privilege by itself. Actual access
control happens through backend-issued, short-lived **session tokens**
(Firebase Auth ID tokens, Supabase JWTs) combined with server-enforced rules
(Firestore security rules, Postgres Row-Level Security). The static key and
the dynamic credential are two different things doing two different jobs.

## Sentry / analytics SDKs (DSN pattern)

The DSN is typically hard-coded because its blast radius is small — it can
only *write* events, not read anything back. Products with a more sensitive
write surface (or that need per-user attribution/rate limits) move to
backend-issued, revocable tokens instead of a shared DSN.

## Where this platform sits today, and the natural next step

The current design (static key, `.env`, manual rotation via admin API)
matches the "public key, low blast radius" end of this spectrum reasonably
well for a first cut: a leaked key is scoped to one app's tools and one
allowed origin (see `internal/auth`), not to the whole platform.

The pattern these examples converge on, if tightening further is ever
warranted, is a small session-token endpoint: the page calls something like
`POST /session-token` on its *own* backend (which holds the real API key
server-side and never exposes it) at page-load time, gets back a
short-lived, single-purpose token, and passes that to `AgentBridge` instead.
The long-lived credential then never reaches the browser at all — only a
token that expires and can't be replayed indefinitely if intercepted.
