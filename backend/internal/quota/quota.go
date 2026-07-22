// Package quota enforces per-user monthly prompt allowances backed by the
// subscriptions and usage_events tables (see internal/db/schema.sql).
//
// The whole design is deliberately counter-free: usage is an append-only
// ledger (usage_events), "how much has this user used this period" is a
// COUNT(*) computed at read time, and the period boundary is DERIVED from
// each user's subscriptions.started_at anchor rather than reset by a
// scheduled job. That removes the reset-boundary race a mutable running
// counter would otherwise have to guard against — see
// docs/subscription-usage-quota-design.md sections 2 and 3.
//
// Attribution key is the app_id (already carried on every inference call as
// inference.Request.AppID); a user's usage is the sum across every app they
// own, joined through apps.owner_id. Enforcement runs at two points, both
// calling Check: the WebSocket handshake (ws.Handler) to turn away a
// connection whose owner is already over, and per prompt (ws.Session.
// handlePrompt) to stop a long-lived connection from overrunning.
package quota

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Service checks and records usage against the database. A nil *Service is
// a valid, fully-disabled quota system: every method is a no-op that allows
// everything. This mirrors ws.Handler.Auth being nil for local/dev/mock
// runs that have no database — quota enforcement is opt-in on having a real
// DB, and never gets in the way of a no-auth dev server.
type Service struct {
	db *sql.DB
}

// New returns a Service backed by db. Pass the same *sql.DB the other
// stores use. Callers that have no database should keep a nil *Service
// instead of constructing one, which disables enforcement entirely.
func New(db *sql.DB) *Service {
	return &Service{db: db}
}

// Decision is the result of a quota Check.
type Decision struct {
	// Allowed is false only when the owner is known and has met or exceeded
	// their allowance for the current period. It is true whenever quota is
	// disabled, the app has no resolvable owner, or the owner is under quota.
	Allowed bool
	// Used and Limit describe the owner's current-period standing when a
	// real check ran (both zero when quota is disabled). Limit is the
	// monthly allowance; Used is the count already recorded this period.
	Used  int
	Limit int
}

// allowed is the decision returned whenever no enforcement applies.
var allowed = Decision{Allowed: true}

// Check reports whether a new billable prompt is permitted for the owner of
// appID right now. A nil Service (disabled) always allows. An app with no
// owner on record (owner_id NULL, or the app is unknown) also always
// allows: quota is a property of a paying user, and an unowned app has no
// user to bill. Any database error is returned to the caller to decide
// fail-open vs. fail-closed at the call site (see ws.Handler and
// ws.Session, which log-and-allow so a transient DB blip never wrongly
// blocks a legitimate user).
func (s *Service) Check(ctx context.Context, appID string) (Decision, error) {
	if s == nil {
		return allowed, nil
	}

	st, ok, err := s.ownerStanding(ctx, appID)
	if err != nil {
		return Decision{}, err
	}
	if !ok {
		// App unknown or unowned — nobody to charge.
		return allowed, nil
	}

	limit := st.limit()
	periodStart := currentPeriodStart(st.startedAt, time.Now())
	used, err := s.usageSince(ctx, st.ownerID, periodStart)
	if err != nil {
		return Decision{}, err
	}

	return Decision{
		Allowed: used < limit,
		Used:    used,
		Limit:   limit,
	}, nil
}

// Record appends one usage event for appID, keyed by eventID for
// idempotency (a retried request carrying the same RequestID is not counted
// twice — the ON CONFLICT below makes the insert a no-op the second time).
// A nil Service (disabled) is a no-op. Callers should record only after the
// billable work actually succeeded (ws.Session.handlePrompt records after
// inference.Complete returns without error).
func (s *Service) Record(ctx context.Context, appID, eventID string) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_events (app_id, event_id, kind)
		VALUES ($1, $2, 'prompt')
		ON CONFLICT (app_id, event_id) DO NOTHING`,
		appID, eventID)
	if err != nil {
		return fmt.Errorf("quota: record usage event: %w", err)
	}
	return nil
}

// Standing is one owner's current plan + usage snapshot, returned by
// StandingFor for the developer-facing self-service quota endpoint
// (internal/console's GET /console/quota). Field naming mirrors
// UserSummary (admin.go) — Tier/PlanName/Limit/Used name the same facts the
// admin back-office already exposes for a user, so the two surfaces agree
// on vocabulary even though this one is scoped to "me" rather than an
// admin-chosen userId.
type Standing struct {
	Tier        Tier
	PlanName    string
	Limit       int
	Used        int
	PeriodStart time.Time
	PeriodEnd   time.Time
}

// StandingFor returns ownerID's current plan and usage-this-period, keyed by
// owner account rather than by app: quota.go's package doc and usageSince
// both establish that usage is attributed to the app's owner and summed
// across every app that owner has (a user can own multiple apps, and they
// all draw from one shared monthly allowance) — Check/Record already
// enforce at that scope via ownerStanding+usageSince, so this read-only
// method mirrors the same scope rather than reporting per-app. The query
// here is the single-user analog of ListUsers' row query (admin.go): same
// users/subscriptions join, narrowed to one id instead of every account.
//
// PeriodEnd is the next period's start (one billing cycle after
// PeriodStart), computed with the same monthBoundary primitive
// currentPeriodStart uses — exclusive, matching how usageSince's
// periodStart bound is inclusive.
func (s *Service) StandingFor(ctx context.Context, ownerID int64) (Standing, error) {
	if s == nil {
		return Standing{}, fmt.Errorf("quota: service is disabled")
	}

	var quotaOverride sql.NullInt64
	var tier string
	var startedAt time.Time
	row := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(sub.tier, $2),
		       sub.monthly_quota,
		       COALESCE(sub.started_at, now())
		  FROM users u
		  LEFT JOIN subscriptions sub ON sub.user_id = u.id
		 WHERE u.id = $1`,
		ownerID, string(DefaultTier))
	err := row.Scan(&tier, &quotaOverride, &startedAt)
	if err != nil {
		return Standing{}, fmt.Errorf("quota: resolve standing: %w", err)
	}

	st := ownerStandingRow{ownerID: ownerID, tier: Tier(tier), startedAt: startedAt}
	if quotaOverride.Valid {
		v := int(quotaOverride.Int64)
		st.quotaOverride = &v
	}

	now := time.Now()
	periodStart := currentPeriodStart(startedAt, now)
	used, err := s.usageSince(ctx, ownerID, periodStart)
	if err != nil {
		return Standing{}, err
	}
	periodEnd := nextPeriodBoundary(startedAt, periodStart)

	return Standing{
		Tier:        st.tier,
		PlanName:    PlanFor(st.tier).Name,
		Limit:       st.limit(),
		Used:        used,
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
	}, nil
}

// ownerStanding resolves appID to its owner and the billing facts needed to
// compute a limit, in one query. ok is false when the app is unknown or has
// no owner_id (an unowned app is not billable).
//
// The limit itself is NOT read from the row — it is derived by Check from
// the tier via PlanFor, so editing a plan applies to everyone on that tier
// immediately. The row supplies three things: the tier (defaulting to the
// free tier when there is no subscriptions row, via COALESCE, so a missing
// row behaves like an explicit free-tier row); the billing-cycle anchor
// (started_at); and an OPTIONAL per-user override (monthly_quota), which is
// NULL for everyone by default and, when set, wins over the plan's number —
// this is the manual "grant this one user more" lever, without which the
// plan value applies.
func (s *Service) ownerStanding(ctx context.Context, appID string) (st ownerStandingRow, ok bool, err error) {
	var quotaOverride sql.NullInt64
	var tier string
	row := s.db.QueryRowContext(ctx, `
		SELECT a.owner_id,
		       COALESCE(sub.tier, $2),
		       sub.monthly_quota,
		       COALESCE(sub.started_at, now())
		  FROM apps a
		  LEFT JOIN subscriptions sub ON sub.user_id = a.owner_id
		 WHERE a.app_id = $1
		   AND a.owner_id IS NOT NULL`,
		appID, string(DefaultTier))
	err = row.Scan(&st.ownerID, &tier, &quotaOverride, &st.startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ownerStandingRow{}, false, nil
	}
	if err != nil {
		return ownerStandingRow{}, false, fmt.Errorf("quota: resolve owner standing: %w", err)
	}
	st.tier = Tier(tier)
	if quotaOverride.Valid {
		v := int(quotaOverride.Int64)
		st.quotaOverride = &v
	}
	return st, true, nil
}

// ownerStandingRow is the raw billing facts for an app's owner (see
// ownerStanding). limit derivation happens in Check, not here.
type ownerStandingRow struct {
	ownerID       int64
	tier          Tier
	quotaOverride *int // nil unless a per-user override is set on the row
	startedAt     time.Time
}

// limit returns the effective monthly prompt allowance: the per-user
// override if one is set, otherwise the tier's plan value. Centralizing
// this here keeps "override beats plan" in one place.
func (r ownerStandingRow) limit() int {
	if r.quotaOverride != nil {
		return *r.quotaOverride
	}
	return PlanFor(r.tier).MonthlyPrompts
}

// usageSince counts billable events across all of ownerID's apps since
// periodStart. This is the O(n)-over-the-ledger query the
// usage_events(app_id, created_at) index exists to keep fast.
func (s *Service) usageSince(ctx context.Context, ownerID int64, periodStart time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM usage_events ue
		  JOIN apps a ON a.app_id = ue.app_id
		 WHERE a.owner_id = $1
		   AND ue.created_at >= $2`,
		ownerID, periodStart).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("quota: count usage: %w", err)
	}
	return n, nil
}

// currentPeriodStart returns the start of the billing period containing now,
// anchored to started_at's day-of-month — mirroring Stripe's
// billing_cycle_anchor. It is the most recent month-boundary at or before
// now: e.g. anchored to the 15th, on the 20th the period started this
// month's 15th; on the 10th it started last month's 15th.
//
// Month-end anchors are clamped to the target month's last day, matching
// Stripe's stated behavior ("a billing cycle anchor of January 31 bills
// February 28/29, then March 31..."): an anchor on the 31st yields Feb 28,
// Apr 30, etc., never rolling over into the following month. All arithmetic
// is done in started_at's own location so the boundary lands at the
// intended local wall-clock instant, not shifted by a UTC/zone mismatch.
func currentPeriodStart(startedAt, now time.Time) time.Time {
	loc := startedAt.Location()
	now = now.In(loc)

	// A period boundary is the anchor day-of-month at the anchor
	// time-of-day. Start from "this month's boundary" and step back a month
	// if it hasn't arrived yet.
	anchorDay := startedAt.Day()

	boundary := monthBoundary(now.Year(), now.Month(), anchorDay, startedAt, loc)
	if !boundary.After(now) {
		return boundary
	}
	// This month's boundary is still in the future — the current period
	// began at last month's boundary.
	prevYear, prevMonth := now.Year(), now.Month()-1
	if prevMonth < time.January {
		prevMonth = time.December
		prevYear--
	}
	return monthBoundary(prevYear, prevMonth, anchorDay, startedAt, loc)
}

// nextPeriodBoundary returns the boundary one billing cycle after
// periodStart (itself assumed to already be a boundary returned by
// currentPeriodStart, anchored to startedAt) — i.e. the current period's
// end / the next period's start. It steps periodStart's (year, month)
// forward by one and re-applies monthBoundary's same clamp-to-last-day
// rule, so a 31st anchor rolling out of a clamped short month still lands
// on the correct following boundary (e.g. an anchor of the 31st, with the
// current period start clamped to Feb 28, ends at Mar 31, not Mar 28).
func nextPeriodBoundary(startedAt, periodStart time.Time) time.Time {
	loc := startedAt.Location()
	periodStart = periodStart.In(loc)
	year, month := periodStart.Year(), periodStart.Month()+1
	if month > time.December {
		month = time.January
		year++
	}
	return monthBoundary(year, month, startedAt.Day(), startedAt, loc)
}

// monthBoundary builds the period-boundary instant in (year, month) for the
// given anchor day, clamping the day to that month's last day so a 31st
// anchor never overflows a shorter month. Hour/min/sec/nsec come from the
// anchor so the boundary reproduces the exact time-of-day the subscription
// started.
func monthBoundary(year int, month time.Month, anchorDay int, anchor time.Time, loc *time.Location) time.Time {
	day := anchorDay
	if last := daysInMonth(year, month); day > last {
		day = last
	}
	return time.Date(year, month, day,
		anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), loc)
}

// daysInMonth returns the number of days in the given month, leap years
// included. Trick: day 0 of the next month is the last day of this one.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
