package quota

import (
	"errors"
	"testing"
	"time"
)

func TestReserveRejectsOverTokenQuota(t *testing.T) {
	m := NewManager()
	if _, err := m.Reserve("key", "model", 10, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reserve("key", "model", 10, 5); !errors.Is(err, ErrTokenQuotaExceeded) {
		t.Fatalf("err = %v, want ErrTokenQuotaExceeded", err)
	}
	if _, err := m.Reserve("key", "other", 10, 5); err != nil {
		t.Fatalf("separate model scope rejected: %v", err)
	}
}

func TestWouldAcceptReportsAvailabilityWithoutMutatingUsage(t *testing.T) {
	now := time.Unix(1000, 0)
	m := NewManagerWithClock(func() time.Time { return now })
	spec := WindowSpec{Window: time.Minute}
	m.RecordUsageWithWindowSpec("key", "model", 10, spec, 4)
	scope := BuildScope("key", "model")
	usedBefore := m.used[scope]
	windowBefore := m.window[scope]

	if !m.WouldAccept("key", "model", 10, spec, 6) {
		t.Fatal("WouldAccept rejected request at quota boundary")
	}
	if m.WouldAccept("key", "model", 10, spec, 7) {
		t.Fatal("WouldAccept accepted request over quota")
	}
	if got := m.Remaining("key", "model", 10, spec); got != 6 {
		t.Fatalf("remaining = %d, want 6", got)
	}
	if got := m.used[scope]; got != usedBefore {
		t.Fatalf("used mutated from %d to %d", usedBefore, got)
	}
	if got := m.window[scope]; got != windowBefore {
		t.Fatalf("window mutated from %#v to %#v", windowBefore, got)
	}
}

func TestWouldAcceptTreatsNonPositiveLimitsAsUnlimited(t *testing.T) {
	m := NewManager()
	m.Seed("key", "model", 100)
	if !m.WouldAccept("key", "model", 0, WindowSpec{}, 1000000) {
		t.Fatal("zero limit should be unlimited")
	}
	if !m.WouldAccept("key", "model", -1, WindowSpec{}, 1000000) {
		t.Fatal("negative limit should be unlimited")
	}
}

func TestWouldAcceptObservesRollingWindowResetWithoutMutating(t *testing.T) {
	now := time.Unix(2000, 0)
	m := NewManagerWithClock(func() time.Time { return now })
	spec := WindowSpec{ResetMode: ResetModeRolling, Window: time.Minute}
	m.RecordUsageWithWindowSpec("key", "model", 10, spec, 8)
	scope := BuildScope("key", "model")
	windowBefore := m.window[scope]
	now = now.Add(time.Minute)

	if !m.WouldAccept("key", "model", 10, spec, 10) {
		t.Fatal("WouldAccept should see reset rolling window capacity")
	}
	if got := m.used[scope]; got != 8 {
		t.Fatalf("used mutated after peek = %d, want 8", got)
	}
	if got := m.window[scope]; got != windowBefore {
		t.Fatalf("window mutated from %#v to %#v", windowBefore, got)
	}
}

func TestWouldAcceptObservesCalendarResetWithoutMutating(t *testing.T) {
	now := time.Date(2026, time.June, 27, 23, 59, 0, 0, time.UTC)
	m := NewManagerWithClock(func() time.Time { return now })
	spec := WindowSpec{ResetMode: ResetModeDaily}
	m.RecordUsageWithWindowSpec("key", "model", 10, spec, 8)
	scope := BuildScope("key", "model")
	windowBefore := m.window[scope]
	now = time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)

	if !m.WouldAccept("key", "model", 10, spec, 10) {
		t.Fatal("WouldAccept should see reset daily quota capacity")
	}
	if got := m.used[scope]; got != 8 {
		t.Fatalf("used mutated after calendar peek = %d, want 8", got)
	}
	if got := m.window[scope]; got != windowBefore {
		t.Fatalf("window mutated from %#v to %#v", windowBefore, got)
	}
}

func TestRecordUsageRespectsWindowResetAndUnlimitedNoop(t *testing.T) {
	now := time.Unix(3000, 0)
	m := NewManagerWithClock(func() time.Time { return now })
	spec := WindowSpec{Window: time.Minute}
	m.RecordUsageWithWindowSpec("key", "model", 10, spec, 6)
	now = now.Add(time.Minute)
	m.RecordUsageWithWindowSpec("key", "model", 10, spec, 3)
	if got := m.Metrics().UsedByScope["key::model"]; got != 3 {
		t.Fatalf("used after rolling reset record = %d, want 3", got)
	}

	m.RecordUsage("key", "unlimited", 0, 7)
	if _, ok := m.Metrics().UsedByScope["key::unlimited"]; ok {
		t.Fatal("unlimited quota record should be a no-op")
	}
}

func TestSeedInitialUsage(t *testing.T) {
	m := NewManager()
	m.Seed("key", "model", 8)
	if got := m.Metrics().UsedByScope["key::model"]; got != 8 {
		t.Fatalf("used = %d", got)
	}
	if _, err := m.Reserve("key", "model", 10, 3); !errors.Is(err, ErrTokenQuotaExceeded) {
		t.Fatalf("err = %v, want ErrTokenQuotaExceeded", err)
	}
}

func TestReservationCommitReconcilesActualUsage(t *testing.T) {
	m := NewManager()
	reservation, err := m.Reserve("key", "model", 20, 4)
	if err != nil {
		t.Fatal(err)
	}
	reservation.Commit(9)
	metrics := m.Metrics()
	if got := metrics.UsedByScope["key::model"]; got != 9 {
		t.Fatalf("used = %d", got)
	}
	if _, err := m.Reserve("key", "model", 10, 2); !errors.Is(err, ErrTokenQuotaExceeded) {
		t.Fatalf("err = %v, want ErrTokenQuotaExceeded", err)
	}
}

func TestReservationReleaseRollsBackReservedTokens(t *testing.T) {
	m := NewManager()
	reservation, err := m.Reserve("key", "model", 10, 7)
	if err != nil {
		t.Fatal(err)
	}
	reservation.Release()
	if got := m.Metrics().UsedByScope["key::model"]; got != 0 {
		t.Fatalf("used = %d", got)
	}
	if _, err := m.Reserve("key", "model", 10, 10); err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
}

func TestTokenQuotaWindowResetsUsage(t *testing.T) {
	m := NewManager()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	window := time.Second
	if _, err := m.ReserveWithWindow("key", "model", 10, window, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveWithWindow("key", "model", 10, window, 5); !errors.Is(err, ErrTokenQuotaExceeded) {
		t.Fatalf("err = %v, want ErrTokenQuotaExceeded", err)
	}
	now = now.Add(window)
	if _, err := m.ReserveWithWindow("key", "model", 10, window, 5); err != nil {
		t.Fatalf("after reset: %v", err)
	}
	metrics := m.Metrics()
	if got := metrics.UsedByScope["key::model"]; got != 5 {
		t.Fatalf("used after reset = %d", got)
	}
	if resetAt := metrics.ResetAtByScope["key::model"]; !resetAt.Equal(now.Add(window)) {
		t.Fatalf("reset_at = %s, want %s", resetAt, now.Add(window))
	}
}

func TestTokenQuotaWindowKeepsSeededUsageUntilReset(t *testing.T) {
	m := NewManager()
	now := time.Unix(2000, 0)
	m.now = func() time.Time { return now }
	m.Seed("key", "model", 8)
	if _, err := m.ReserveWithWindow("key", "model", 10, time.Minute, 3); !errors.Is(err, ErrTokenQuotaExceeded) {
		t.Fatalf("err = %v, want ErrTokenQuotaExceeded", err)
	}
	now = now.Add(time.Minute)
	if _, err := m.ReserveWithWindow("key", "model", 10, time.Minute, 3); err != nil {
		t.Fatalf("after seeded window reset: %v", err)
	}
}

func TestMetricsAppliesWindowBeforeExportingUsage(t *testing.T) {
	m := NewManager()
	now := time.Unix(3000, 0)
	m.now = func() time.Time { return now }
	window := time.Second
	if _, err := m.ReserveWithWindow("key", "model", 10, window, 6); err != nil {
		t.Fatal(err)
	}

	now = now.Add(window)
	metrics := m.Metrics()

	if got := metrics.UsedByScope["key::model"]; got != 0 {
		t.Fatalf("used after snapshot reset = %d, want 0", got)
	}
	if resetAt := metrics.ResetAtByScope["key::model"]; !resetAt.Equal(now.Add(window)) {
		t.Fatalf("reset_at = %s, want %s", resetAt, now.Add(window))
	}
}

func TestNextResetAtComputesUTCBoundaries(t *testing.T) {
	tests := []struct {
		name string
		mode ResetMode
		now  time.Time
		want time.Time
	}{
		{
			name: "daily midday",
			mode: ResetModeDaily,
			now:  time.Date(2026, time.June, 27, 15, 4, 5, 0, time.UTC),
			want: time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "daily exact midnight",
			mode: ResetModeDaily,
			now:  time.Date(2026, time.June, 27, 0, 0, 0, 0, time.UTC),
			want: time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "daily converts non UTC time",
			mode: ResetModeDaily,
			now:  time.Date(2026, time.June, 27, 23, 30, 0, 0, time.FixedZone("UTC+02", 2*60*60)),
			want: time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "weekly Sunday to Monday",
			mode: ResetModeWeekly,
			now:  time.Date(2026, time.June, 28, 12, 0, 0, 0, time.UTC),
			want: time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "weekly exact Monday",
			mode: ResetModeWeekly,
			now:  time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC),
			want: time.Date(2026, time.July, 6, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "monthly mid month",
			mode: ResetModeMonthly,
			now:  time.Date(2026, time.January, 15, 8, 0, 0, 0, time.UTC),
			want: time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "monthly December to January",
			mode: ResetModeMonthly,
			now:  time.Date(2026, time.December, 31, 23, 59, 0, 0, time.UTC),
			want: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "monthly non leap February",
			mode: ResetModeMonthly,
			now:  time.Date(2025, time.February, 28, 12, 0, 0, 0, time.UTC),
			want: time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "monthly leap day",
			mode: ResetModeMonthly,
			now:  time.Date(2024, time.February, 29, 12, 0, 0, 0, time.UTC),
			want: time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "yearly December to January",
			mode: ResetModeYearly,
			now:  time.Date(2026, time.December, 31, 23, 59, 0, 0, time.UTC),
			want: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextResetAt(tc.mode, tc.now); !got.Equal(tc.want) {
				t.Fatalf("NextResetAt() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestWindowStartAtComputesCurrentUTCBoundary(t *testing.T) {
	tests := []struct {
		name string
		mode ResetMode
		now  time.Time
		want time.Time
	}{
		{
			name: "daily midday",
			mode: ResetModeDaily,
			now:  time.Date(2026, time.June, 27, 15, 4, 5, 0, time.UTC),
			want: time.Date(2026, time.June, 27, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "weekly Sunday starts previous Monday",
			mode: ResetModeWeekly,
			now:  time.Date(2026, time.June, 28, 12, 0, 0, 0, time.UTC),
			want: time.Date(2026, time.June, 22, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "weekly exact Monday",
			mode: ResetModeWeekly,
			now:  time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC),
			want: time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "monthly leap day",
			mode: ResetModeMonthly,
			now:  time.Date(2024, time.February, 29, 12, 0, 0, 0, time.UTC),
			want: time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "yearly December",
			mode: ResetModeYearly,
			now:  time.Date(2026, time.December, 31, 23, 59, 0, 0, time.UTC),
			want: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WindowStartAt(tc.mode, tc.now); !got.Equal(tc.want) {
				t.Fatalf("WindowStartAt() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTokenQuotaCalendarResetResetsUsageAtBoundary(t *testing.T) {
	now := time.Date(2026, time.June, 27, 23, 59, 0, 0, time.UTC)
	m := NewManagerWithClock(func() time.Time { return now })
	spec := WindowSpec{ResetMode: ResetModeDaily}
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 5); !errors.Is(err, ErrTokenQuotaExceeded) {
		t.Fatalf("err = %v, want ErrTokenQuotaExceeded", err)
	}
	now = time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 5); err != nil {
		t.Fatalf("after reset: %v", err)
	}
	metrics := m.Metrics()
	if got := metrics.UsedByScope["key::model"]; got != 5 {
		t.Fatalf("used after reset = %d, want 5", got)
	}
	wantReset := time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)
	if resetAt := metrics.ResetAtByScope["key::model"]; !resetAt.Equal(wantReset) {
		t.Fatalf("reset_at = %s, want %s", resetAt, wantReset)
	}
}

func TestTokenQuotaCalendarResetKeepsSeededUsageUntilBoundary(t *testing.T) {
	now := time.Date(2026, time.June, 27, 23, 0, 0, 0, time.UTC)
	m := NewManagerWithClock(func() time.Time { return now })
	m.Seed("key", "model", 8)
	spec := WindowSpec{ResetMode: ResetModeDaily}
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 3); !errors.Is(err, ErrTokenQuotaExceeded) {
		t.Fatalf("err = %v, want ErrTokenQuotaExceeded", err)
	}
	now = time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 3); err != nil {
		t.Fatalf("after seeded calendar reset: %v", err)
	}
}

func TestMetricsAppliesCalendarResetBeforeExportingUsage(t *testing.T) {
	now := time.Date(2026, time.June, 27, 23, 0, 0, 0, time.UTC)
	m := NewManagerWithClock(func() time.Time { return now })
	spec := WindowSpec{ResetMode: ResetModeDaily}
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 6); err != nil {
		t.Fatal(err)
	}

	now = time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	metrics := m.Metrics()

	if got := metrics.UsedByScope["key::model"]; got != 0 {
		t.Fatalf("used after snapshot reset = %d, want 0", got)
	}
	wantReset := time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)
	if resetAt := metrics.ResetAtByScope["key::model"]; !resetAt.Equal(wantReset) {
		t.Fatalf("reset_at = %s, want %s", resetAt, wantReset)
	}
}

func TestRollingWindowSpecMatchesDurationBehavior(t *testing.T) {
	now := time.Unix(4000, 0)
	m := NewManagerWithClock(func() time.Time { return now })
	spec := WindowSpec{ResetMode: ResetModeRolling, Window: time.Second}
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 6); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 5); err != nil {
		t.Fatalf("after rolling reset: %v", err)
	}
	metrics := m.Metrics()
	if got := metrics.UsedByScope["key::model"]; got != 5 {
		t.Fatalf("used after reset = %d, want 5", got)
	}
	if resetAt := metrics.ResetAtByScope["key::model"]; !resetAt.Equal(now.Add(time.Second)) {
		t.Fatalf("reset_at = %s, want %s", resetAt, now.Add(time.Second))
	}
}

func TestTokenQuotaCalendarResetRejectsOverQuota(t *testing.T) {
	m := NewManagerWithClock(func() time.Time {
		return time.Date(2026, time.June, 27, 12, 0, 0, 0, time.UTC)
	})
	spec := WindowSpec{ResetMode: ResetModeMonthly}
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveWithWindowSpec("key", "model", 10, spec, 5); !errors.Is(err, ErrTokenQuotaExceeded) {
		t.Fatalf("err = %v, want ErrTokenQuotaExceeded", err)
	}
}

func TestSeedWithCalendarWindowSpecExposesResetAt(t *testing.T) {
	now := time.Date(2026, time.June, 27, 12, 0, 0, 0, time.UTC)
	m := NewManagerWithClock(func() time.Time { return now })

	m.SeedWithWindowSpec("key", "model", 5, WindowSpec{ResetMode: ResetModeDaily})

	metrics := m.Metrics()
	if got := metrics.UsedByScope["key::model"]; got != 5 {
		t.Fatalf("seeded usage = %d, want 5", got)
	}
	wantReset := time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	if got := metrics.ResetAtByScope["key::model"]; !got.Equal(wantReset) {
		t.Fatalf("reset_at = %s, want %s", got, wantReset)
	}
}

func TestSeedWithCalendarWindowSpecAtResetsWhenClockPassedBoundary(t *testing.T) {
	seedAt := time.Date(2026, time.June, 27, 23, 59, 59, 0, time.UTC)
	now := time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	m := NewManagerWithClock(func() time.Time { return now })

	m.SeedWithWindowSpecAt("key", "model", 5, WindowSpec{ResetMode: ResetModeDaily}, seedAt)

	metrics := m.Metrics()
	if got := metrics.UsedByScope["key::model"]; got != 0 {
		t.Fatalf("seeded usage after boundary = %d, want 0", got)
	}
	wantReset := time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)
	if got := metrics.ResetAtByScope["key::model"]; !got.Equal(wantReset) {
		t.Fatalf("reset_at = %s, want %s", got, wantReset)
	}
}
