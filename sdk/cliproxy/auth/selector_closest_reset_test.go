package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func recordTestWindows(authID string, short, long time.Time) {
	RecordUsageWindows(authID,
		UsageWindow{Reset: short, Span: defaultShortUsageWindow},
		UsageWindow{Reset: long, Span: defaultLongUsageWindow},
	)
}

func TestClosestToResetSelectorPick_PrefersSoonestWeightedReset(t *testing.T) {
	now := time.Now()
	// ctr-soon resets its weekly window in 1h; ctr-late has almost a full week left.
	recordTestWindows("ctr-soon", now.Add(4*time.Hour), now.Add(1*time.Hour))
	recordTestWindows("ctr-late", now.Add(1*time.Hour), now.Add(6*24*time.Hour))

	selector := &ClosestToResetSelector{}
	auths := []*Auth{{ID: "ctr-late"}, {ID: "ctr-soon"}}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "ctr-soon" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "ctr-soon")
	}
}

func TestClosestToResetSelectorPick_FiveHourWeightCanDominate(t *testing.T) {
	now := time.Now()
	// Equal weekly position; only the 5h windows differ.
	recordTestWindows("ctr-5h-soon", now.Add(30*time.Minute), now.Add(3*24*time.Hour))
	recordTestWindows("ctr-5h-late", now.Add(4*time.Hour), now.Add(3*24*time.Hour))

	selector := &ClosestToResetSelector{}
	auths := []*Auth{{ID: "ctr-5h-late"}, {ID: "ctr-5h-soon"}}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "ctr-5h-soon" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "ctr-5h-soon")
	}
}

func TestClosestToResetSelectorPick_UnknownAuthProbedOncePerInterval(t *testing.T) {
	now := time.Now()
	recordTestWindows("ctr-known", now.Add(2*time.Hour), now.Add(30*time.Minute))

	selector := &ClosestToResetSelector{}
	auths := []*Auth{{ID: "ctr-known"}, {ID: "ctr-unknown"}}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "ctr-unknown" {
		t.Fatalf("first Pick() auth.ID = %q, want probe of %q", got.ID, "ctr-unknown")
	}

	// The probe is throttled: while ctr-unknown still has no data, traffic
	// must return to the scored credential instead of pinning the unknown.
	for i := 0; i < 3; i++ {
		got, err = selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != "ctr-known" {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q after probe", i, got.ID, "ctr-known")
		}
	}
}

func TestClosestToResetSelectorPick_NoDataFallsBackToRoundRobin(t *testing.T) {
	selector := &ClosestToResetSelector{}
	auths := []*Auth{{ID: "ctr-nodata-b"}, {ID: "ctr-nodata-a"}}

	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		got, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		seen[got.ID]++
	}
	if seen["ctr-nodata-a"] != 2 || seen["ctr-nodata-b"] != 2 {
		t.Fatalf("Pick() distribution = %v, want even round-robin", seen)
	}
}

func TestClosestToResetSelectorPick_ElapsedResetTreatedAsFresh(t *testing.T) {
	now := time.Now()
	// ctr-fresh's windows elapsed (rolled over): it holds a full budget and
	// must be preserved. ctr-mid is mid-window and closest to reset.
	recordTestWindows("ctr-fresh", now.Add(50*time.Millisecond), now.Add(60*time.Millisecond))
	recordTestWindows("ctr-mid", now.Add(2*time.Hour), now.Add(2*24*time.Hour))
	time.Sleep(80 * time.Millisecond)

	selector := &ClosestToResetSelector{}
	auths := []*Auth{{ID: "ctr-fresh"}, {ID: "ctr-mid"}}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "ctr-mid" {
		t.Fatalf("Pick() auth.ID = %q, want %q (elapsed window means fresh budget)", got.ID, "ctr-mid")
	}
}

func TestClosestToResetSelectorPick_SkipsBlockedAuths(t *testing.T) {
	now := time.Now()
	recordTestWindows("ctr-blocked", now.Add(30*time.Minute), now.Add(time.Hour))
	recordTestWindows("ctr-open", now.Add(4*time.Hour), now.Add(6*24*time.Hour))

	blocked := &Auth{ID: "ctr-blocked"}
	blocked.Unavailable = true
	blocked.NextRetryAfter = now.Add(time.Hour)

	selector := &ClosestToResetSelector{}
	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, []*Auth{blocked, {ID: "ctr-open"}})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "ctr-open" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "ctr-open")
	}
}

func TestRecordUsageWindows_ZeroKeepsPreviousWindow(t *testing.T) {
	now := time.Now()
	weekly := now.Add(2 * 24 * time.Hour)
	RecordUsageWindows("ctr-merge", UsageWindow{}, UsageWindow{Reset: weekly})
	fiveHour := now.Add(90 * time.Minute)
	RecordUsageWindows("ctr-merge", UsageWindow{Reset: fiveHour}, UsageWindow{})

	entry, ok := lookupTestEntry("ctr-merge")
	if !ok {
		t.Fatalf("registry entry missing")
	}
	if !entry.long.Reset.Equal(weekly) {
		t.Fatalf("long reset = %v, want %v", entry.long.Reset, weekly)
	}
	if !entry.short.Reset.Equal(fiveHour) {
		t.Fatalf("short reset = %v, want %v", entry.short.Reset, fiveHour)
	}
}

func TestRecordUsageWindows_RejectsNonFutureResets(t *testing.T) {
	now := time.Now()
	// A relative value misparsed as an absolute timestamp lands near the
	// epoch; it must not create a registry entry.
	RecordUsageWindows("ctr-garbage", UsageWindow{Reset: time.Unix(18000, 0)}, UsageWindow{Reset: now.Add(-time.Hour)})
	if _, ok := lookupTestEntry("ctr-garbage"); ok {
		t.Fatalf("non-future resets must be rejected")
	}
}

func lookupTestEntry(authID string) (usageWindowEntry, bool) {
	entries := snapshotUsageWindows([]*Auth{{ID: authID}})
	entry, ok := entries[authID]
	return entry, ok
}

func TestClosestToResetSelectorWeights_PerFieldDefaults(t *testing.T) {
	t.Parallel()

	selector := &ClosestToResetSelector{}
	weekly, fiveHour := selector.weights()
	if weekly != defaultClosestToResetWeeklyWeight || fiveHour != defaultClosestToResetFiveHourWeight {
		t.Fatalf("weights() = %v, %v, want defaults", weekly, fiveHour)
	}

	selector = &ClosestToResetSelector{WeeklyWeight: 3, FiveHourWeight: 1}
	weekly, fiveHour = selector.weights()
	if weekly != 0.75 || fiveHour != 0.25 {
		t.Fatalf("weights() = %v, %v, want 0.75, 0.25", weekly, fiveHour)
	}

	// Setting only one weight keeps the other at its default instead of
	// silently zeroing it.
	selector = &ClosestToResetSelector{WeeklyWeight: 0.9}
	weekly, fiveHour = selector.weights()
	wantWeekly := 0.9 / 1.2
	wantFiveHour := 0.3 / 1.2
	if diff := weekly - wantWeekly; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("weekly = %v, want %v", weekly, wantWeekly)
	}
	if diff := fiveHour - wantFiveHour; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("fiveHour = %v, want %v", fiveHour, wantFiveHour)
	}
}

func TestWindowRemainingFraction_Semantics(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if got := windowRemainingFraction(UsageWindow{}, now, defaultShortUsageWindow); got != 1 {
		t.Fatalf("unobserved fraction = %v, want 1 (defensive guard, never urgency)", got)
	}
	if got := windowRemainingFraction(UsageWindow{Reset: now.Add(-time.Minute)}, now, defaultShortUsageWindow); got != 1 {
		t.Fatalf("elapsed fraction = %v, want 1 (fresh budget)", got)
	}
	if got := windowRemainingFraction(UsageWindow{Reset: now.Add(10 * time.Hour)}, now, defaultShortUsageWindow); got != 1 {
		t.Fatalf("beyond window fraction = %v, want 1", got)
	}
	// A provider-reported span overrides the default.
	got := windowRemainingFraction(UsageWindow{Reset: now.Add(30 * time.Minute), Span: time.Hour}, now, defaultShortUsageWindow)
	if got < 0.49 || got > 0.51 {
		t.Fatalf("custom span fraction = %v, want ~0.5", got)
	}
}

func TestClosestToResetSelectorPick_PartialDataRenormalized(t *testing.T) {
	now := time.Now()
	// ctr-partial reports only its short window and is fully fresh there.
	// ctr-full reports both windows and is genuinely closer to its resets.
	// Without renormalization the missing weekly slot would gift ctr-partial
	// a free 0 for 70% of the score and it would win.
	RecordUsageWindows("ctr-partial", UsageWindow{Reset: now.Add(5 * time.Hour)}, UsageWindow{})
	recordTestWindows("ctr-full", now.Add(2*time.Hour), now.Add(2*24*time.Hour))

	selector := &ClosestToResetSelector{}
	auths := []*Auth{{ID: "ctr-partial"}, {ID: "ctr-full"}}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "ctr-full" {
		t.Fatalf("Pick() auth.ID = %q, want %q (partial data must not read as urgency)", got.ID, "ctr-full")
	}
}

func TestTryMarkUsageWindowProbe_AtomicThrottle(t *testing.T) {
	now := time.Now()
	if !tryMarkUsageWindowProbe("ctr-probe-claim", now) {
		t.Fatal("first claim should succeed")
	}
	if tryMarkUsageWindowProbe("ctr-probe-claim", now.Add(time.Second)) {
		t.Fatal("second claim inside the interval should fail")
	}
	if !tryMarkUsageWindowProbe("ctr-probe-claim", now.Add(usageWindowProbeInterval+time.Second)) {
		t.Fatal("claim after the interval should succeed")
	}

	// Once data exists the credential is scored, never probed.
	RecordUsageWindows("ctr-probe-data", UsageWindow{Reset: now.Add(time.Hour)}, UsageWindow{})
	if tryMarkUsageWindowProbe("ctr-probe-data", now.Add(2*usageWindowProbeInterval)) {
		t.Fatal("credential with data must not be claimable as a probe")
	}
}
