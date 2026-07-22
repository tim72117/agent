package quota

import (
	"testing"
	"time"
)

// mustParse parses an RFC3339 timestamp or fails the test.
func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestCurrentPeriodStart(t *testing.T) {
	cases := []struct {
		name    string
		started string // subscription anchor
		now     string
		want    string // expected current-period start
	}{
		{
			name:    "mid-month anchor, now past the anchor day",
			started: "2026-01-15T00:00:00Z",
			now:     "2026-03-20T09:00:00Z",
			want:    "2026-03-15T00:00:00Z", // this month's 15th already passed
		},
		{
			name:    "mid-month anchor, now before the anchor day",
			started: "2026-01-15T00:00:00Z",
			now:     "2026-03-10T09:00:00Z",
			want:    "2026-02-15T00:00:00Z", // this month's 15th not yet reached → last month's
		},
		{
			name:    "now exactly on the boundary instant counts as the new period",
			started: "2026-01-15T08:30:00Z",
			now:     "2026-03-15T08:30:00Z",
			want:    "2026-03-15T08:30:00Z", // boundary is inclusive (>= in the query)
		},
		{
			name:    "one nanosecond before the boundary is still the previous period",
			started: "2026-01-15T08:30:00Z",
			now:     "2026-03-15T08:29:59Z",
			want:    "2026-02-15T08:30:00Z",
		},
		{
			name:    "31st anchor clamps to February",
			started: "2026-01-31T00:00:00Z",
			now:     "2026-02-15T00:00:00Z",
			want:    "2026-01-31T00:00:00Z", // Feb has no 31st; this month's boundary clamps to Feb 28 (future), so previous is Jan 31
		},
		{
			name:    "31st anchor, now late February, boundary clamps to Feb 28",
			started: "2026-01-31T00:00:00Z",
			now:     "2026-02-28T12:00:00Z",
			want:    "2026-02-28T00:00:00Z", // Feb boundary clamped to the 28th, now is past it
		},
		{
			name:    "31st anchor in a 30-day month clamps to the 30th",
			started: "2026-01-31T00:00:00Z",
			now:     "2026-04-30T06:00:00Z",
			want:    "2026-04-30T00:00:00Z", // April has 30 days
		},
		{
			name:    "leap-year February 29 exists",
			started: "2024-01-31T00:00:00Z",
			now:     "2024-02-29T06:00:00Z",
			want:    "2024-02-29T00:00:00Z", // 2024 is a leap year; clamp to the 29th
		},
		{
			name:    "crossing the year boundary backwards",
			started: "2025-06-15T00:00:00Z",
			now:     "2026-01-10T00:00:00Z",
			want:    "2025-12-15T00:00:00Z", // Jan 15 not yet reached → December's 15th, previous year
		},
		{
			name:    "1st-of-month anchor, mid-month now",
			started: "2026-01-01T00:00:00Z",
			now:     "2026-05-17T23:00:00Z",
			want:    "2026-05-01T00:00:00Z",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := currentPeriodStart(mustParse(t, tc.started), mustParse(t, tc.now))
			want := mustParse(t, tc.want)
			if !got.Equal(want) {
				t.Errorf("currentPeriodStart(%s, %s) = %s, want %s",
					tc.started, tc.now, got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// The period start must never be in the future and never more than roughly
// a month in the past, for any anchor/now combination — a cheap invariant
// that would catch an off-by-one in the month-stepping logic.
func TestCurrentPeriodStartInvariants(t *testing.T) {
	anchors := []string{
		"2026-01-01T00:00:00Z", "2026-01-15T12:00:00Z", "2026-01-28T00:00:00Z",
		"2026-01-29T00:00:00Z", "2026-01-30T00:00:00Z", "2026-01-31T00:00:00Z",
	}
	base := mustParse(t, "2026-03-15T00:00:00Z")
	for _, a := range anchors {
		anchor := mustParse(t, a)
		for day := 0; day < 400; day++ {
			now := base.AddDate(0, 0, day)
			start := currentPeriodStart(anchor, now)
			if start.After(now) {
				t.Fatalf("anchor %s, now %s: period start %s is in the future",
					a, now.Format(time.RFC3339), start.Format(time.RFC3339))
			}
			// The previous boundary can be at most 31 days before now.
			if now.Sub(start) > 32*24*time.Hour {
				t.Fatalf("anchor %s, now %s: period start %s is more than 32 days back",
					a, now.Format(time.RFC3339), start.Format(time.RFC3339))
			}
		}
	}
}

func TestPlanFor(t *testing.T) {
	// The defined free tier resolves to itself.
	if got := PlanFor(TierFree); got.Tier != TierFree {
		t.Errorf("PlanFor(TierFree).Tier = %q, want %q", got.Tier, TierFree)
	}
	// An undefined tier (a paid tier since removed, or a typo from a manual
	// UPDATE) must fall back to the free plan — fail safe, not error or
	// unlimited.
	if got := PlanFor(Tier("nonexistent-tier")); got.Tier != TierFree {
		t.Errorf("PlanFor(unknown).Tier = %q, want fallback %q", got.Tier, TierFree)
	}
	// Empty tier (e.g. a row that somehow stored "") also falls back.
	if got := PlanFor(Tier("")); got.Tier != TierFree {
		t.Errorf("PlanFor(\"\").Tier = %q, want fallback %q", got.Tier, TierFree)
	}
}

// TestStandingForPeriodEnd exercises nextPeriodBoundary, the periodEnd
// derivation StandingFor uses, without needing a database —
// periodStart/periodEnd here are pure functions of (startedAt, now).
func TestStandingForPeriodEnd(t *testing.T) {
	cases := []struct {
		name      string
		started   string
		now       string
		wantStart string
		wantEnd   string
	}{
		{
			name:      "mid-month anchor",
			started:   "2026-01-15T00:00:00Z",
			now:       "2026-03-20T09:00:00Z",
			wantStart: "2026-03-15T00:00:00Z",
			wantEnd:   "2026-04-15T00:00:00Z",
		},
		{
			name:      "31st anchor rolls current period into a 28-day February",
			started:   "2026-01-31T00:00:00Z",
			now:       "2026-02-15T00:00:00Z",
			wantStart: "2026-01-31T00:00:00Z",
			wantEnd:   "2026-02-28T00:00:00Z",
		},
		{
			name:      "31st anchor, period already in clamped February, end is March 31",
			started:   "2026-01-31T00:00:00Z",
			now:       "2026-02-28T12:00:00Z",
			wantStart: "2026-02-28T00:00:00Z",
			wantEnd:   "2026-03-31T00:00:00Z",
		},
		{
			name:      "1st-of-month anchor",
			started:   "2026-01-01T00:00:00Z",
			now:       "2026-05-17T23:00:00Z",
			wantStart: "2026-05-01T00:00:00Z",
			wantEnd:   "2026-06-01T00:00:00Z",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := mustParse(t, tc.started)
			now := mustParse(t, tc.now)
			periodStart := currentPeriodStart(started, now)
			periodEnd := nextPeriodBoundary(started, periodStart)

			if want := mustParse(t, tc.wantStart); !periodStart.Equal(want) {
				t.Errorf("periodStart = %s, want %s", periodStart.Format(time.RFC3339), want.Format(time.RFC3339))
			}
			if want := mustParse(t, tc.wantEnd); !periodEnd.Equal(want) {
				t.Errorf("periodEnd = %s, want %s", periodEnd.Format(time.RFC3339), want.Format(time.RFC3339))
			}
			if !periodEnd.After(periodStart) {
				t.Errorf("periodEnd %s must be after periodStart %s", periodEnd, periodStart)
			}
		})
	}
}

func TestOwnerStandingLimit(t *testing.T) {
	planFree := PlanFor(TierFree).MonthlyPrompts

	t.Run("no override uses the tier plan", func(t *testing.T) {
		r := ownerStandingRow{tier: TierFree, quotaOverride: nil}
		if got := r.limit(); got != planFree {
			t.Errorf("limit() = %d, want plan value %d", got, planFree)
		}
	})

	t.Run("override wins over the plan", func(t *testing.T) {
		override := planFree + 999
		r := ownerStandingRow{tier: TierFree, quotaOverride: &override}
		if got := r.limit(); got != override {
			t.Errorf("limit() = %d, want override %d", got, override)
		}
	})

	t.Run("unknown tier with no override falls back to free plan value", func(t *testing.T) {
		r := ownerStandingRow{tier: Tier("ghost"), quotaOverride: nil}
		if got := r.limit(); got != planFree {
			t.Errorf("limit() = %d, want free-plan fallback %d", got, planFree)
		}
	})

	t.Run("override of zero is respected (not treated as unset)", func(t *testing.T) {
		zero := 0
		r := ownerStandingRow{tier: TierFree, quotaOverride: &zero}
		if got := r.limit(); got != 0 {
			t.Errorf("limit() = %d, want 0 (explicit override)", got)
		}
	})
}
