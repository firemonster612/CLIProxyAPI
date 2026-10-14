package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	c.JSON(http.StatusOK, gin.H{"enabled": enabled, "requests": requests})
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
