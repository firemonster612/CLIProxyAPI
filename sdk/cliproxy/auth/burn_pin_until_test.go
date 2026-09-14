package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestSetBurnPinUntilReset_UsesRecordedWindows(t *testing.T) {
	m := newBurnPinTestManager(t, &Auth{ID: "burn-until-a", Provider: "claude"})
	fiveHour := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	weekly := time.Now().Add(3 * 24 * time.Hour).Truncate(time.Second)
	RecordUsageWindows("burn-until-a", UsageWindow{Reset: fiveHour}, UsageWindow{Reset: weekly})

	pin, err := m.SetBurnPinUntilReset("burn-until-a", BurnWindowFiveHour)
	if err != nil {
		t.Fatalf("SetBurnPinUntilReset(five-hour) error = %v", err)
	}
	if !pin.ExpiresAt.Equal(fiveHour) || pin.Mode != BurnWindowFiveHour {
		t.Fatalf("pin = %+v, want expiry %v mode %s", pin, fiveHour, BurnWindowFiveHour)
	}

	pin, err = m.SetBurnPinUntilReset("burn-until-a", BurnWindowWeekly)
	if err != nil {
		t.Fatalf("SetBurnPinUntilReset(weekly) error = %v", err)
	}
	if !pin.ExpiresAt.Equal(weekly) || pin.Mode != BurnWindowWeekly {
		t.Fatalf("pin = %+v, want expiry %v mode %s", pin, weekly, BurnWindowWeekly)
	}
}

func TestSetBurnPinUntilReset_FallsBackToQuotaSignals(t *testing.T) {
	weekly := time.Now().Add(4 * 24 * time.Hour).Truncate(time.Second)
	m := newBurnPinTestManager(t, &Auth{
		ID:       "burn-until-signals",
		Provider: "claude",
		Quota: QuotaState{Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(weekly.Unix(), 10),
		}},
	})

	pin, err := m.SetBurnPinUntilReset("burn-until-signals", BurnWindowWeekly)
	if err != nil {
		t.Fatalf("SetBurnPinUntilReset() error = %v", err)
	}
	if !pin.ExpiresAt.Equal(weekly) {
		t.Fatalf("pin expiry = %v, want %v from quota signals", pin.ExpiresAt, weekly)
	}
}

func TestSetBurnPinUntilReset_RefusesUnknownReset(t *testing.T) {
	m := newBurnPinTestManager(t, &Auth{ID: "burn-until-unknown", Provider: "claude"})
	_, err := m.SetBurnPinUntilReset("burn-until-unknown", BurnWindowFiveHour)
	authErr, ok := err.(*Error)
	if !ok || authErr.Code != "reset_unknown" {
		t.Fatalf("error = %v, want reset_unknown", err)
	}

	// A window for the other slot only must not satisfy this one.
	RecordUsageWindows("burn-until-unknown", UsageWindow{}, UsageWindow{Reset: time.Now().Add(24 * time.Hour)})
	if _, err = m.SetBurnPinUntilReset("burn-until-unknown", BurnWindowFiveHour); err == nil {
		t.Fatal("SetBurnPinUntilReset(five-hour) expected reset_unknown with only a weekly window recorded")
	}

	if _, err = m.SetBurnPinUntilReset("burn-until-unknown", "next-tuesday"); err == nil {
		t.Fatal("SetBurnPinUntilReset expected error for unknown window name")
	}
}

func TestSetBurnPin_ClearsProviderSessionAffinity(t *testing.T) {
	affinity := NewSessionAffinitySelector(nil)
	t.Cleanup(affinity.Stop)
	m := NewManager(nil, affinity, nil)
	m.RegisterExecutor(burnPinStubExecutor{id: "claude"})
	for _, auth := range []*Auth{
		{ID: "burn-aff-a", Provider: "claude"},
		{ID: "burn-aff-b", Provider: "claude"},
	} {
		if _, err := m.Register(t.Context(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}
	affinity.cache.Set("claude::session-1::claude-model", "burn-aff-b")
	affinity.cache.Set("gemini::session-2::gemini-model", "gemini-auth")

	if _, err := m.SetBurnPin("burn-aff-a", time.Hour); err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}

	if _, ok := affinity.cache.Get("claude::session-1::claude-model"); ok {
		t.Fatal("expected claude session binding to be cleared by burn pin")
	}
	if _, ok := affinity.cache.Get("gemini::session-2::gemini-model"); !ok {
		t.Fatal("expected other providers' bindings to survive a claude burn pin")
	}
}

func TestParseResetInstant(t *testing.T) {
	unix := time.Unix(1790000000, 0)
	if parsed, ok := parseResetInstant("1790000000"); !ok || !parsed.Equal(unix) {
		t.Fatalf("parseResetInstant(unix) = %v %v", parsed, ok)
	}
	stamp := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if parsed, ok := parseResetInstant(stamp.Format(time.RFC3339)); !ok || !parsed.Equal(stamp) {
		t.Fatalf("parseResetInstant(rfc3339) = %v %v", parsed, ok)
	}
	if _, ok := parseResetInstant("soon"); ok {
		t.Fatal("parseResetInstant(garbage) expected failure")
	}
}
