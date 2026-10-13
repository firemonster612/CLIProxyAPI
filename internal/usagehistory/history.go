// Package usagehistory keeps a bounded, in-memory history of completed usage
// records so the management API can show recent routing without consuming the
// destructive usage queue.
package usagehistory

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	// MaxPerAuth bounds the entries retained for a single auth index.
	MaxPerAuth = 200
	// MaxTotal bounds the entries retained across all auths.
	MaxTotal = 2000
)

const timestampLayout = "2006-01-02T15:04:05.000Z07:00"

// Entry is the management-safe projection of a usage record. It deliberately
// omits API keys, token hashes, client metadata, session IDs and bodies.
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
}

// MarshalJSON renders the timestamp as RFC3339 UTC with millisecond precision.
func (e Entry) MarshalJSON() ([]byte, error) {
	type fields Entry
	return json.Marshal(struct {
		Timestamp string `json:"timestamp"`
		fields
	}{Timestamp: e.Timestamp.UTC().Format(timestampLayout), fields: fields(e)})
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

func (plugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if !redisqueue.UsageStatisticsEnabled() {
		return
	}
	defaultHistory.Add(entryFromRecord(record, time.Now()))
}

func entryFromRecord(record coreusage.Record, now time.Time) Entry {
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
	return entry
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

// Recent returns matching entries newest first, capped at filter.Limit.
func (h *History) Recent(filter Filter) []Entry {
	h.mu.Lock()
	matched := make([]Entry, 0, len(h.entries))
	// Walk newest arrival first so the stable sort keeps arrival order for equal timestamps.
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
		return matched[i].Timestamp.After(matched[j].Timestamp)
	})
	if filter.Limit > 0 && len(matched) > filter.Limit {
		matched = matched[:filter.Limit]
	}
	return matched
}
