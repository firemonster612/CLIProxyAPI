// Package usagehistory keeps a bounded, in-memory history of completed usage
// records so the management API can show recent routing without consuming the
// destructive usage queue.
package usagehistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	// MaxPerAuth bounds the entries retained for a single auth index.
	MaxPerAuth = 200
	// MaxTotal bounds the entries retained across all auths.
	MaxTotal = 2000
)

// TimestampLayout renders management API times as RFC3339 with millisecond precision.
const TimestampLayout = "2006-01-02T15:04:05.000Z07:00"

const (
	fingerprintHexLength = 10
	maxUserAgentLength   = 120
)

// Entry is the management-safe projection of a usage record. It deliberately
// omits API keys, token hashes, raw session IDs, forwarded headers and bodies;
// the client key and session appear only as fingerprints.
type Entry struct {
	Timestamp     time.Time `json:"-"`
	Provider      string    `json:"provider"`
	AuthIndex     string    `json:"auth_index"`
	AuthID        string    `json:"auth_id"`
	Model         string    `json:"model"`
	Alias         string    `json:"alias,omitempty"`
	ResponseModel string    `json:"response_model,omitempty"`
	Failed        bool      `json:"failed"`
	StatusCode    int       `json:"status_code,omitempty"`
	LatencyMs     int64     `json:"latency_ms"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	TotalTokens   int64     `json:"total_tokens"`
	// RequestID is the logging ID of the downstream HTTP request, shared by its retries.
	RequestID string `json:"request_id,omitempty"`
	// ClientKeyLabel names the configured client key; the management handler fills it.
	ClientKeyLabel       string `json:"client_key_label,omitempty"`
	ClientKeyFingerprint string `json:"client_key_fingerprint,omitempty"`
	UserAgent            string `json:"user_agent,omitempty"`
	// ClientIP is the peer address the proxy saw, which is a reverse proxy's when one fronts it.
	ClientIP           string `json:"client_ip,omitempty"`
	SessionFingerprint string `json:"session_fingerprint,omitempty"`
}

// Fingerprint returns a short, stable, non-reversible identifier for a secret
// or client-supplied value, or "" for an empty value.
func Fingerprint(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:fingerprintHexLength]
}

// CompletedAt returns when the request finished: its start time plus latency.
func (e Entry) CompletedAt() time.Time {
	return e.Timestamp.Add(time.Duration(e.LatencyMs) * time.Millisecond)
}

// MarshalJSON renders the start and completion times as RFC3339 UTC with millisecond precision.
func (e Entry) MarshalJSON() ([]byte, error) {
	type fields Entry
	return json.Marshal(struct {
		Timestamp   string `json:"timestamp"`
		CompletedAt string `json:"completed_at"`
		fields
	}{
		Timestamp:   e.Timestamp.UTC().Format(TimestampLayout),
		CompletedAt: e.CompletedAt().UTC().Format(TimestampLayout),
		fields:      fields(e),
	})
}

// Filter selects entries returned by Recent. Empty fields match everything.
type Filter struct {
	Provider  string
	AuthIndex string
	Limit     int
}

// History is a mutex-protected bounded log of entries in arrival order.
type History struct {
	mu      sync.Mutex
	entries []Entry
	perAuth map[string]int
}

// New returns an empty history.
func New() *History {
	return &History{perAuth: make(map[string]int)}
}

var defaultHistory = New()

func init() {
	coreusage.RegisterNamedPlugin("usage-history", plugin{})
}

// Default returns the process-wide history fed by the usage manager.
func Default() *History { return defaultHistory }

type plugin struct{}

func (plugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !redisqueue.UsageStatisticsEnabled() {
		return
	}
	defaultHistory.Add(entryFromRecord(ctx, record, time.Now()))
}

func entryFromRecord(ctx context.Context, record coreusage.Record, now time.Time) Entry {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = now
	}
	entry := Entry{
		Timestamp:     timestamp,
		Provider:      strings.TrimSpace(record.Provider),
		AuthIndex:     strings.TrimSpace(record.AuthIndex),
		AuthID:        strings.TrimSpace(record.AuthID),
		Model:         strings.TrimSpace(record.Model),
		Alias:         strings.TrimSpace(record.Alias),
		ResponseModel: strings.TrimSpace(record.ResponseModel),
		Failed:        record.Failed,
		LatencyMs:     record.Latency.Milliseconds(),
		InputTokens:   record.Detail.InputTokens,
		OutputTokens:  record.Detail.OutputTokens,
		TotalTokens:   record.Detail.TotalTokens,
	}
	if record.Failed && record.Fail.StatusCode > 0 {
		entry.StatusCode = record.Fail.StatusCode
	}
	client := logging.GetClientRequestMetadata(ctx)
	entry.RequestID = logging.GetRequestID(ctx)
	entry.ClientKeyFingerprint = Fingerprint(strings.TrimSpace(record.APIKey))
	entry.UserAgent = truncateRunes(strings.TrimSpace(client.UserAgent), maxUserAgentLength)
	entry.ClientIP = strings.TrimSpace(client.ClientIP)
	entry.SessionFingerprint = Fingerprint(strings.TrimSpace(record.SessionID))
	return entry
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// Add appends an entry, evicting the oldest entry of the same auth past
// MaxPerAuth and then the oldest entry overall past MaxTotal.
func (h *History) Add(entry Entry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.entries = append(h.entries, entry)
	h.perAuth[entry.AuthIndex]++
	if h.perAuth[entry.AuthIndex] > MaxPerAuth {
		for i := range h.entries {
			if h.entries[i].AuthIndex == entry.AuthIndex {
				h.removeAt(i)
				break
			}
		}
	}
	if len(h.entries) > MaxTotal {
		h.removeAt(0)
	}
}

func (h *History) removeAt(i int) {
	authIndex := h.entries[i].AuthIndex
	h.entries = append(h.entries[:i], h.entries[i+1:]...)
	h.perAuth[authIndex]--
	if h.perAuth[authIndex] == 0 {
		delete(h.perAuth, authIndex)
	}
}

// Recent returns matching entries by newest completion first, capped at filter.Limit.
func (h *History) Recent(filter Filter) []Entry {
	h.mu.Lock()
	matched := make([]Entry, 0, len(h.entries))
	// Walk newest arrival first so the stable sort keeps arrival order for equal completion times.
	for i := len(h.entries) - 1; i >= 0; i-- {
		entry := h.entries[i]
		if filter.Provider != "" && !strings.EqualFold(entry.Provider, filter.Provider) {
			continue
		}
		if filter.AuthIndex != "" && entry.AuthIndex != filter.AuthIndex {
			continue
		}
		matched = append(matched, entry)
	}
	h.mu.Unlock()

	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].CompletedAt().After(matched[j].CompletedAt())
	})
	if filter.Limit > 0 && len(matched) > filter.Limit {
		matched = matched[:filter.Limit]
	}
	return matched
}
