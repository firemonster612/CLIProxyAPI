package usagehistory

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

var baseTime = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func entryAt(authIndex string, second int) Entry {
	return Entry{AuthIndex: authIndex, Provider: "claude", Timestamp: baseTime.Add(time.Duration(second) * time.Second)}
}

func TestAddEvictsOldestEntryOfSameAuthPastPerAuthBound(t *testing.T) {
	h := New()
	h.Add(entryAt("other", 0))
	for i := 1; i <= MaxPerAuth+5; i++ {
		h.Add(entryAt("busy", i))
	}

	busy := h.Recent(Filter{AuthIndex: "busy"})
	if len(busy) != MaxPerAuth {
		t.Fatalf("busy entries = %d, want %d", len(busy), MaxPerAuth)
	}
	if got, want := busy[len(busy)-1].Timestamp, baseTime.Add(6*time.Second); !got.Equal(want) {
		t.Fatalf("oldest busy entry = %v, want %v", got, want)
	}
	if other := h.Recent(Filter{AuthIndex: "other"}); len(other) != 1 {
		t.Fatalf("other entries = %d, want 1 (per-auth eviction must not touch other auths)", len(other))
	}
}

func TestAddEvictsOldestEntryPastGlobalBound(t *testing.T) {
	h := New()
	for i := 0; i < MaxTotal+3; i++ {
		h.Add(entryAt(fmt.Sprintf("auth-%d", i%20), i))
	}

	all := h.Recent(Filter{})
	if len(all) != MaxTotal {
		t.Fatalf("entries = %d, want %d", len(all), MaxTotal)
	}
	if got, want := all[len(all)-1].Timestamp, baseTime.Add(3*time.Second); !got.Equal(want) {
		t.Fatalf("oldest entry = %v, want %v", got, want)
	}
	if got := len(h.Recent(Filter{AuthIndex: "auth-0"})); got != 100 {
		t.Fatalf("auth-0 entries = %d, want 100", got)
	}
}

func TestRecentFiltersAndOrdersNewestFirst(t *testing.T) {
	h := New()
	h.Add(Entry{AuthIndex: "a", Provider: "claude", Model: "late-start", Timestamp: baseTime.Add(5 * time.Second)})
	h.Add(Entry{AuthIndex: "b", Provider: "codex", Model: "codex", Timestamp: baseTime.Add(2 * time.Second)})
	h.Add(Entry{AuthIndex: "a", Provider: "claude", Model: "early-start", Timestamp: baseTime.Add(1 * time.Second)})
	h.Add(Entry{AuthIndex: "a", Provider: "claude", Model: "tie-later", Timestamp: baseTime.Add(5 * time.Second)})

	models := func(entries []Entry) string {
		out := make([]string, len(entries))
		for i, e := range entries {
			out[i] = e.Model
		}
		return fmt.Sprint(out)
	}
	if got, want := models(h.Recent(Filter{})), "[tie-later late-start codex early-start]"; got != want {
		t.Fatalf("all = %s, want %s", got, want)
	}
	if got, want := models(h.Recent(Filter{Provider: "CODEX"})), "[codex]"; got != want {
		t.Fatalf("provider filter = %s, want %s", got, want)
	}
	if got, want := models(h.Recent(Filter{Provider: "claude", AuthIndex: "a", Limit: 2})), "[tie-later late-start]"; got != want {
		t.Fatalf("auth filter with limit = %s, want %s", got, want)
	}
}

func TestEntryFromRecordProjectsSafeFields(t *testing.T) {
	record := coreusage.Record{
		Provider:          "claude",
		Model:             "claude-sonnet",
		Alias:             "sonnet",
		ResponseModel:     "claude-sonnet-20260101",
		APIKey:            "sk-secret",
		AccessTokenSHA256: "hash-secret",
		SessionID:         "session-secret",
		AuthID:            "auth-1",
		AuthIndex:         "idx-1",
		RequestedAt:       time.Date(2026, 10, 13, 12, 0, 0, 123_000_000, time.UTC),
		Latency:           1234 * time.Millisecond,
		Failed:            true,
		Fail:              coreusage.Failure{StatusCode: 429, Body: "body-secret"},
		Detail:            coreusage.Detail{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
	}

	payload, err := json.Marshal(entryFromRecord(record, baseTime))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"timestamp":"2026-10-13T12:00:00.123Z","provider":"claude","auth_index":"idx-1","auth_id":"auth-1","model":"claude-sonnet","alias":"sonnet","response_model":"claude-sonnet-20260101","failed":true,"status_code":429,"latency_ms":1234,"input_tokens":1,"output_tokens":2,"total_tokens":3}`
	if string(payload) != want {
		t.Fatalf("json = %s\nwant   %s", payload, want)
	}
}

func TestEntryFromRecordOmitsStatusForSuccessAndDefaultsTimestamp(t *testing.T) {
	entry := entryFromRecord(coreusage.Record{Provider: "codex", Fail: coreusage.Failure{StatusCode: 200}}, baseTime)
	if entry.StatusCode != 0 {
		t.Fatalf("status_code = %d, want omitted for successful record", entry.StatusCode)
	}
	if !entry.Timestamp.Equal(baseTime) {
		t.Fatalf("timestamp = %v, want fallback %v", entry.Timestamp, baseTime)
	}
}
