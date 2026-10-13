package management

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagehistory"
)

func withRecentRequestHistory(t *testing.T, statisticsEnabled bool, entries ...usagehistory.Entry) {
	t.Helper()
	history := usagehistory.New()
	for _, entry := range entries {
		history.Add(entry)
	}
	previousHistory := recentRequestHistory
	previousEnabled := redisqueue.UsageStatisticsEnabled()
	recentRequestHistory = history
	redisqueue.SetUsageStatisticsEnabled(statisticsEnabled)
	t.Cleanup(func() {
		recentRequestHistory = previousHistory
		redisqueue.SetUsageStatisticsEnabled(previousEnabled)
	})
}

func getRecentRequests(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/recent-requests"+query, nil)
	(&Handler{}).GetRecentRequests(ginCtx)
	return rec
}

func TestGetRecentRequestsReturnsFilteredEntriesNewestFirst(t *testing.T) {
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	withRecentRequestHistory(t, true,
		usagehistory.Entry{Timestamp: base, Provider: "claude", AuthIndex: "a", AuthID: "auth-a", Model: "older", LatencyMs: 10},
		usagehistory.Entry{Timestamp: base.Add(time.Second), Provider: "codex", AuthIndex: "b", AuthID: "auth-b", Model: "codex"},
		usagehistory.Entry{Timestamp: base.Add(2 * time.Second), Provider: "claude", AuthIndex: "a", AuthID: "auth-a", Model: "newer", Failed: true, StatusCode: 429, InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
	)

	rec := getRecentRequests(t, "?provider=claude&auth_index=a")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	want := `{"enabled":true,"requests":[` +
		`{"timestamp":"2026-10-13T12:00:02.000Z","completed_at":"2026-10-13T12:00:02.000Z","provider":"claude","auth_index":"a","auth_id":"auth-a","model":"newer","failed":true,"status_code":429,"latency_ms":0,"input_tokens":1,"output_tokens":2,"total_tokens":3},` +
		`{"timestamp":"2026-10-13T12:00:00.000Z","completed_at":"2026-10-13T12:00:00.010Z","provider":"claude","auth_index":"a","auth_id":"auth-a","model":"older","failed":false,"latency_ms":10,"input_tokens":0,"output_tokens":0,"total_tokens":0}` +
		`]}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}
}

func TestGetRecentRequestsReportsDisabledStatisticsWithEmptyList(t *testing.T) {
	withRecentRequestHistory(t, false, usagehistory.Entry{Provider: "claude", AuthIndex: "a"})

	rec := getRecentRequests(t, "")

	if rec.Code != http.StatusOK || rec.Body.String() != `{"enabled":false,"requests":[]}` {
		t.Fatalf("status = %d body = %s, want 200 with enabled=false and empty list", rec.Code, rec.Body.String())
	}
}

func TestGetRecentRequestsRejectsInvalidLimit(t *testing.T) {
	withRecentRequestHistory(t, true)

	for _, query := range []string{"?limit=0", "?limit=-1", "?limit=abc"} {
		if rec := getRecentRequests(t, query); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want %d", query, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestParseRecentRequestsLimitDefaultsAndClamps(t *testing.T) {
	cases := map[string]int{"": 200, "5": 5, "2000": 2000, "5000": 2000}
	for input, want := range cases {
		got, err := parseRecentRequestsLimit(input)
		if err != nil || got != want {
			t.Fatalf("parseRecentRequestsLimit(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
}
