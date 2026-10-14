package management

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagehistory"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// routingPreviewScope says what the preview predicts: a fresh request with no
// session context, which session affinity can route elsewhere.
const routingPreviewScope = "new-request-without-session"

type routingPreviewResponse struct {
	ObservedAt             string                    `json:"observed_at"`
	Provider               string                    `json:"provider"`
	Model                  string                    `json:"model"`
	ModelSource            string                    `json:"model_source"`
	Scope                  string                    `json:"scope"`
	Strategy               string                    `json:"strategy"`
	SessionAffinityEnabled bool                      `json:"session_affinity_enabled"`
	Supported              bool                      `json:"supported"`
	UnsupportedReason      string                    `json:"unsupported_reason,omitempty"`
	ChosenAuthIndex        *string                   `json:"chosen_auth_index"`
	Reason                 string                    `json:"reason,omitempty"`
	Candidates             []routingPreviewCandidate `json:"candidates"`
	Excluded               []routingPreviewExclusion `json:"excluded"`
}

type routingPreviewCandidate struct {
	AuthIndex     string   `json:"auth_index"`
	Priority      int      `json:"priority"`
	Score         *float64 `json:"score"`
	ProbeEligible bool     `json:"probe_eligible"`
}

type routingPreviewExclusion struct {
	AuthIndex string `json:"auth_index"`
	Reason    string `json:"reason"`
	RetryAt   string `json:"retry_at,omitempty"`
}

// GetRoutingPreview reports, without changing any routing state, which
// credential a new request for the provider and model would use.
func (h *Handler) GetRoutingPreview(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
		return
	}
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}
	model, modelSource := h.routingPreviewModel(provider, strings.TrimSpace(c.Query("model")))
	// Under Home dispatch the preview is unsupported whatever the model.
	if model == "" && !h.authManager.HomeEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no model given or known for provider " + provider})
		return
	}

	now := time.Now()
	preview, errPreview := h.authManager.PreviewRouting(provider, model, now)
	if errPreview != nil {
		status := http.StatusInternalServerError
		var authErr *coreauth.Error
		if errors.As(errPreview, &authErr) && authErr.HTTPStatus != 0 {
			status = authErr.HTTPStatus
		}
		c.JSON(status, gin.H{"error": errPreview.Error()})
		return
	}
	c.JSON(http.StatusOK, newRoutingPreviewResponse(preview, modelSource, now))
}

// routingPreviewModel picks the model to preview: the query's, else the newest
// recent request's client model for the provider, else the registered model
// most of the provider's credentials serve.
func (h *Handler) routingPreviewModel(provider, queryModel string) (model, source string) {
	if queryModel != "" {
		return queryModel, "query"
	}
	if recent := recentRequestHistory.Recent(usagehistory.Filter{Provider: provider, Limit: 1}); len(recent) > 0 {
		if model := recent[0].Alias; model != "" {
			return model, "recent"
		}
		if model := recent[0].Model; model != "" {
			return model, "recent"
		}
	}
	return h.authManager.RepresentativeModel(provider), "registry"
}

func newRoutingPreviewResponse(preview coreauth.RoutingPreview, modelSource string, observedAt time.Time) routingPreviewResponse {
	response := routingPreviewResponse{
		ObservedAt:             observedAt.UTC().Format(usagehistory.TimestampLayout),
		Provider:               preview.Provider,
		Model:                  preview.Model,
		ModelSource:            modelSource,
		Scope:                  routingPreviewScope,
		Strategy:               preview.Strategy,
		SessionAffinityEnabled: preview.SessionAffinityEnabled,
		Supported:              preview.Supported,
		UnsupportedReason:      preview.UnsupportedReason,
		Reason:                 preview.Reason,
		Candidates:             make([]routingPreviewCandidate, 0, len(preview.Candidates)),
		Excluded:               make([]routingPreviewExclusion, 0, len(preview.Excluded)),
	}
	if preview.ChosenAuthIndex != "" {
		response.ChosenAuthIndex = &preview.ChosenAuthIndex
	}
	for _, candidate := range preview.Candidates {
		response.Candidates = append(response.Candidates, routingPreviewCandidate(candidate))
	}
	for _, exclusion := range preview.Excluded {
		entry := routingPreviewExclusion{AuthIndex: exclusion.AuthIndex, Reason: exclusion.Reason}
		if !exclusion.RetryAt.IsZero() {
			entry.RetryAt = exclusion.RetryAt.UTC().Format(usagehistory.TimestampLayout)
		}
		response.Excluded = append(response.Excluded, entry)
	}
	return response
}
