package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagehistory"
)

const defaultRecentRequestsLimit = 200

// recentRequestHistory is a variable so tests can substitute an isolated history.
var recentRequestHistory = usagehistory.Default()

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}

// GetRecentRequests returns recent usage records newest first without consuming them.
func (h *Handler) GetRecentRequests(c *gin.Context) {
	limit, errLimit := parseRecentRequestsLimit(c.Query("limit"))
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errLimit.Error()})
		return
	}

	enabled := redisqueue.UsageStatisticsEnabled()
	requests := []usagehistory.Entry{}
	if enabled {
		requests = recentRequestHistory.Recent(usagehistory.Filter{
			Provider:  strings.TrimSpace(c.Query("provider")),
			AuthIndex: strings.TrimSpace(c.Query("auth_index")),
			Limit:     limit,
		})
	}
	var apiKeys []string
	if h != nil && h.cfg != nil {
		apiKeys = h.cfg.APIKeys
	}
	labelClientKeys(requests, apiKeys)
	c.JSON(http.StatusOK, gin.H{"enabled": enabled, "requests": requests})
}

// labelClientKeys names each entry's client key by its 1-based position in the
// configured keys, matched by fingerprint, or "Key ?" when no configured key matches.
func labelClientKeys(entries []usagehistory.Entry, apiKeys []string) {
	labels := make(map[string]string, len(apiKeys))
	for i, key := range apiKeys {
		fingerprint := usagehistory.Fingerprint(strings.TrimSpace(key))
		if _, seen := labels[fingerprint]; fingerprint != "" && !seen {
			labels[fingerprint] = "Key #" + strconv.Itoa(i+1)
		}
	}
	for i := range entries {
		fingerprint := entries[i].ClientKeyFingerprint
		if fingerprint == "" {
			continue
		}
		if label, ok := labels[fingerprint]; ok {
			entries[i].ClientKeyLabel = label
		} else {
			entries[i].ClientKeyLabel = "Key ?"
		}
	}
}

func parseRecentRequestsLimit(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultRecentRequestsLimit, nil
	}
	limit, errLimit := strconv.Atoi(value)
	if errLimit != nil || limit <= 0 {
		return 0, errors.New("limit must be a positive integer")
	}
	return min(limit, usagehistory.MaxTotal), nil
}

// maxInFlightRequests caps the attempts one /in-flight-requests response lists.
const maxInFlightRequests = 500

type inFlightRequest struct {
	AttemptID      string `json:"attempt_id"`
	RequestID      string `json:"request_id"`
	Provider       string `json:"provider"`
	AuthIndex      string `json:"auth_index"`
	Model          string `json:"model"`
	RequestedModel string `json:"requested_model"`
	Kind           string `json:"kind"`
	StartedAt      string `json:"started_at"`
}

// GetInFlightRequests lists executor attempts currently running, oldest first.
func (h *Handler) GetInFlightRequests(c *gin.Context) {
	provider := strings.TrimSpace(c.Query("provider"))
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	observedAt := time.Now()

	requests := []inFlightRequest{}
	truncated := false
	if h != nil && h.authManager != nil {
		for _, attempt := range h.authManager.InFlightAttempts() {
			if provider != "" && !strings.EqualFold(attempt.Provider, provider) {
				continue
			}
			if authIndex != "" && attempt.AuthIndex != authIndex {
				continue
			}
			if len(requests) == maxInFlightRequests {
				truncated = true
				break
			}
			requests = append(requests, inFlightRequest{
				AttemptID:      attempt.AttemptID,
				RequestID:      attempt.RequestID,
				Provider:       attempt.Provider,
				AuthIndex:      attempt.AuthIndex,
				Model:          attempt.Model,
				RequestedModel: attempt.RequestedModel,
				Kind:           attempt.Kind,
				StartedAt:      attempt.StartedAt.UTC().Format(usagehistory.TimestampLayout),
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"observed_at": observedAt.UTC().Format(usagehistory.TimestampLayout),
		"truncated":   truncated,
		"requests":    requests,
	})
}
