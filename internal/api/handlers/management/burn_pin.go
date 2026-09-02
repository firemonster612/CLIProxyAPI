package management

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// maxBurnPinDurationSeconds bounds finite pins to 7 days; 0 pins indefinitely.
const maxBurnPinDurationSeconds = 7 * 24 * 3600

// GetBurnPins lists the active burn pins with display metadata.
func (h *Handler) GetBurnPins(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	pins := h.authManager.BurnPins()
	indexByAuthID := make(map[string]string)
	for _, auth := range h.authManager.List() {
		if auth != nil {
			indexByAuthID[auth.ID] = auth.EnsureIndex()
		}
	}
	now := time.Now()
	entries := make([]gin.H, 0, len(pins))
	for _, pin := range pins {
		entries = append(entries, burnPinEntry(pin, indexByAuthID[pin.AuthID], now))
	}
	c.JSON(http.StatusOK, gin.H{"pins": entries})
}

// SetBurnPin pins a provider's traffic to one credential for a duration.
// duration_seconds of 0 pins indefinitely; negative or oversized values are
// rejected rather than reinterpreted.
func (h *Handler) SetBurnPin(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var req struct {
		AuthIndex       string `json:"auth_index"`
		DurationSeconds int64  `json:"duration_seconds"`
	}
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	if req.DurationSeconds < 0 || req.DurationSeconds > maxBurnPinDurationSeconds {
		c.JSON(http.StatusBadRequest, gin.H{"error": "duration_seconds must be between 0 (indefinite) and 604800"})
		return
	}
	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	pin, errSet := h.authManager.SetBurnPin(auth.ID, time.Duration(req.DurationSeconds)*time.Second)
	if errSet != nil {
		c.JSON(burnPinErrorStatus(errSet), gin.H{"error": errSet.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "pin": burnPinEntry(pin, auth.EnsureIndex(), time.Now())})
}

// DeleteBurnPin clears a burn pin by auth_index, or by provider as an escape
// hatch for pins whose credential no longer resolves.
func (h *Handler) DeleteBurnPin(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	provider := strings.TrimSpace(c.Query("provider"))
	switch {
	case authIndex != "":
		auth := h.authByIndex(authIndex)
		if auth == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
			return
		}
		if !h.authManager.ClearBurnPinForAuth(auth.ID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no burn pin for auth"})
			return
		}
	case provider != "":
		if !h.authManager.ClearBurnPinForProvider(provider) {
			c.JSON(http.StatusNotFound, gin.H{"error": "no burn pin for provider"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index or provider is required"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func burnPinErrorStatus(err error) int {
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil {
		switch authErr.Code {
		case "auth_not_found":
			return http.StatusNotFound
		case "invalid_request", "invalid_auth":
			return http.StatusBadRequest
		case "home_unavailable", "manager_unavailable":
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusInternalServerError
}

func burnPinEntry(pin coreauth.BurnPin, authIndex string, now time.Time) gin.H {
	entry := gin.H{
		"provider":   pin.Provider,
		"auth_id":    pin.AuthID,
		"name":       pin.AuthID,
		"indefinite": pin.ExpiresAt.IsZero(),
	}
	if authIndex != "" {
		entry["auth_index"] = authIndex
	}
	if !pin.ExpiresAt.IsZero() {
		entry["expires_at"] = pin.ExpiresAt.Format(time.RFC3339)
		remaining := int64(pin.ExpiresAt.Sub(now).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		entry["remaining_seconds"] = remaining
	}
	return entry
}
