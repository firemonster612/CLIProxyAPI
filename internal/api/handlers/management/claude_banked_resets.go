package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	claude "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// resolveClaudeBankedResetAuth maps an auth_index to a Claude OAuth credential
// and its access token, writing the error response itself when it fails.
func (h *Handler) resolveClaudeBankedResetAuth(c *gin.Context, authIndex string) (*coreauth.Auth, string, bool) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return nil, "", false
	}
	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return nil, "", false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth is not a claude credential"})
		return nil, "", false
	}
	accessToken := strings.TrimSpace(claude.ReadMetadataString(&auth.Metadata, "access_token"))
	if accessToken == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "credential has no OAuth access token"})
		return nil, "", false
	}
	return auth, accessToken, true
}

// GetClaudeBankedResets reports the banked-resets status for one Claude OAuth
// credential straight from Anthropic's usage endpoint. The block is returned
// raw so operators see every field Anthropic sends, null when the account has
// no banked-resets data.
func (h *Handler) GetClaudeBankedResets(c *gin.Context) {
	auth, accessToken, ok := h.resolveClaudeBankedResetAuth(c, c.Query("auth_index"))
	if !ok {
		return
	}
	status, errFetch := claude.NewClaudeAuth(h.cfg).FetchBankedResetStatus(c.Request.Context(), accessToken)
	if errFetch != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch banked resets: %v", errFetch)})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"auth_index":    auth.EnsureIndex(),
		"banked_resets": status,
	})
}

// UseClaudeBankedReset spends one banked reset for a Claude OAuth credential.
// grant_id is optional; when absent the grant is chosen from a fresh status
// fetch (the server-designated next grant). A successful claim also clears the
// credential's local quota/cooldown state so the routing selector can pick it
// up again immediately instead of waiting out a cooldown that Anthropic just
// lifted.
func (h *Handler) UseClaudeBankedReset(c *gin.Context) {
	var req struct {
		AuthIndex string `json:"auth_index"`
		GrantID   string `json:"grant_id"`
	}
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	auth, accessToken, ok := h.resolveClaudeBankedResetAuth(c, req.AuthIndex)
	if !ok {
		return
	}
	anthropicAuth := claude.NewClaudeAuth(h.cfg)
	ctx := c.Request.Context()

	grantID := strings.TrimSpace(req.GrantID)
	if grantID == "" {
		status, errFetch := anthropicAuth.FetchBankedResetStatus(ctx, accessToken)
		if errFetch != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to fetch banked resets: %v", errFetch)})
			return
		}
		grantID = claude.NextBankedResetGrantID(status)
		if grantID == "" {
			c.JSON(http.StatusConflict, gin.H{"error": "no usable banked reset grant", "banked_resets": status})
			return
		}
	}

	organizationUUID, errOrg := h.claudeOrganizationUUID(ctx, anthropicAuth, auth, accessToken)
	if errOrg != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errOrg.Error()})
		return
	}

	claim, errSpend := anthropicAuth.SpendBankedReset(ctx, accessToken, organizationUUID, grantID)
	if errSpend != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to spend banked reset: %v", errSpend)})
		return
	}

	quotaReset := false
	if claim.Result == "reset" && h.authManager != nil {
		if _, _, errReset := h.authManager.ResetQuota(ctx, auth.ID); errReset != nil {
			log.WithError(errReset).Warnf("banked reset claimed but local quota state not cleared for %s", auth.EnsureIndex())
		} else {
			quotaReset = true
		}
	}

	claimJSON, _ := json.Marshal(claim)
	c.JSON(http.StatusOK, gin.H{
		"status":      "ok",
		"auth_index":  auth.EnsureIndex(),
		"grant_id":    grantID,
		"claim":       json.RawMessage(claimJSON),
		"quota_reset": quotaReset,
	})
}

// claudeOrganizationUUID resolves the organization for a claim from credential
// metadata, falling back to the OAuth profile for credentials saved before the
// organization was recorded.
func (h *Handler) claudeOrganizationUUID(ctx context.Context, anthropicAuth *claude.ClaudeAuth, auth *coreauth.Auth, accessToken string) (string, error) {
	if organizationUUID := strings.TrimSpace(claude.ReadMetadataString(&auth.Metadata, "organization_uuid")); organizationUUID != "" {
		return organizationUUID, nil
	}
	profile, errProfile := anthropicAuth.FetchOAuthProfile(ctx, accessToken)
	if errProfile != nil {
		return "", fmt.Errorf("credential has no organization_uuid and the profile lookup failed: %v", errProfile)
	}
	if organizationUUID := strings.TrimSpace(profile.Organization.UUID); organizationUUID != "" {
		return organizationUUID, nil
	}
	return "", fmt.Errorf("credential has no organization_uuid and the profile reports none")
}
