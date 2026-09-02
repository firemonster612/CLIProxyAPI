package helps

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseCodexUsageWindows(t *testing.T) {
	now := time.Now()

	t.Run("nil headers", func(t *testing.T) {
		primary, secondary := ParseCodexUsageWindows(nil, now)
		if !primary.Reset.IsZero() || !secondary.Reset.IsZero() {
			t.Fatalf("expected zero windows, got %+v, %+v", primary, secondary)
		}
	})

	t.Run("reset-at absolute timestamps with window minutes", func(t *testing.T) {
		primaryReset := now.Add(30 * time.Minute).Truncate(time.Second)
		secondaryReset := now.Add(24 * time.Hour).Truncate(time.Second)
		h := make(http.Header)
		h.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(primaryReset.Unix(), 10))
		h.Set("X-Codex-Primary-Window-Minutes", "300")
		h.Set("X-Codex-Secondary-Reset-At", strconv.FormatInt(secondaryReset.Unix(), 10))
		h.Set("X-Codex-Secondary-Window-Minutes", "10080")
		primary, secondary := ParseCodexUsageWindows(h, now)
		if !primary.Reset.Equal(primaryReset) {
			t.Fatalf("primary reset = %v, want %v", primary.Reset, primaryReset)
		}
		if primary.Span != 300*time.Minute {
			t.Fatalf("primary span = %v, want 5h", primary.Span)
		}
		if !secondary.Reset.Equal(secondaryReset) {
			t.Fatalf("secondary reset = %v, want %v", secondary.Reset, secondaryReset)
		}
		if secondary.Span != 10080*time.Minute {
			t.Fatalf("secondary span = %v, want 7d", secondary.Span)
		}
	})

	t.Run("legacy reset-after-seconds fallback case-insensitive", func(t *testing.T) {
		h := http.Header{"x-codex-primary-reset-after-seconds": []string{"600"}}
		primary, secondary := ParseCodexUsageWindows(h, now)
		if !primary.Reset.Equal(now.Add(600 * time.Second)) {
			t.Fatalf("primary reset = %v, want %v", primary.Reset, now.Add(600*time.Second))
		}
		if primary.Span != 0 {
			t.Fatalf("primary span = %v, want 0 (absent)", primary.Span)
		}
		if !secondary.Reset.IsZero() {
			t.Fatalf("secondary should be zero, got %+v", secondary)
		}
	})

	t.Run("garbage values ignored", func(t *testing.T) {
		h := make(http.Header)
		h.Set("X-Codex-Primary-Reset-At", "soon")
		h.Set("X-Codex-Secondary-Reset-After-Seconds", "-5")
		primary, secondary := ParseCodexUsageWindows(h, now)
		if !primary.Reset.IsZero() || !secondary.Reset.IsZero() {
			t.Fatalf("expected zero windows, got %+v, %+v", primary, secondary)
		}
	})
}

func TestParseCodexUsageWindowsEvent(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	t.Run("live payload shape uses resets_at", func(t *testing.T) {
		// Shape captured from real Codex websocket traffic (openai/codex#28879):
		// windows carry plural "resets_at" as absolute Unix seconds.
		primaryReset := now.Add(45 * time.Minute)
		secondaryReset := now.Add(3 * 24 * time.Hour)
		payload := fmt.Sprintf(
			`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":41.5,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":27.0,"window_minutes":10080,"resets_at":%d}}}`,
			primaryReset.Unix(), secondaryReset.Unix())
		primary, secondary := ParseCodexUsageWindowsEvent([]byte(payload), now)
		if !primary.Reset.Equal(primaryReset) {
			t.Fatalf("primary reset = %v, want %v", primary.Reset, primaryReset)
		}
		if primary.Span != 300*time.Minute {
			t.Fatalf("primary span = %v, want 5h", primary.Span)
		}
		if !secondary.Reset.Equal(secondaryReset) {
			t.Fatalf("secondary reset = %v, want %v", secondary.Reset, secondaryReset)
		}
	})

	t.Run("reset_at fallback spelling", func(t *testing.T) {
		reset := now.Add(time.Hour)
		payload := fmt.Sprintf(`{"type":"codex.rate_limits","rate_limits":{"primary":{"reset_at":%d}}}`, reset.Unix())
		primary, _ := ParseCodexUsageWindowsEvent([]byte(payload), now)
		if !primary.Reset.Equal(reset) {
			t.Fatalf("primary reset = %v, want %v", primary.Reset, reset)
		}
	})

	t.Run("resets_in_seconds relative fallback", func(t *testing.T) {
		payload := `{"type":"codex.rate_limits","rate_limits":{"primary":{"resets_in_seconds":900}}}`
		primary, _ := ParseCodexUsageWindowsEvent([]byte(payload), now)
		if !primary.Reset.Equal(now.Add(900 * time.Second)) {
			t.Fatalf("primary reset = %v, want %v", primary.Reset, now.Add(900*time.Second))
		}
	})

	t.Run("other event types ignored", func(t *testing.T) {
		primary, secondary := ParseCodexUsageWindowsEvent([]byte(`{"type":"response.completed","rate_limits":{"primary":{"resets_at":99999999999}}}`), now)
		if !primary.Reset.IsZero() || !secondary.Reset.IsZero() {
			t.Fatalf("expected zero windows, got %+v, %+v", primary, secondary)
		}
	})

	t.Run("missing windows", func(t *testing.T) {
		primary, secondary := ParseCodexUsageWindowsEvent([]byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":10}}}`), now)
		if !primary.Reset.IsZero() || !secondary.Reset.IsZero() {
			t.Fatalf("expected zero windows, got %+v, %+v", primary, secondary)
		}
	})
}
