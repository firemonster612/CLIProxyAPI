package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Banked resets let a subscription account bank the right to refill its unified
// rate-limit windows and spend it later. Claude Code 2.1.278 calls the program
// "cedar_ember" on the wire: the status block rides the OAuth usage endpoint
// with cedar_ember=1, and spending a reset posts a claim to the organization's
// reset_rate_limits endpoint with program "cedar_ember".
const (
	// UsageURL is the OAuth usage endpoint including the banked-resets status
	// block. skip_spend=1 matches the native client's read-only fetches so a
	// status poll can never consume anything.
	UsageURL = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1"

	bankedResetProgram       = "cedar_ember"
	resetRateLimitsURLFormat = "https://api.anthropic.com/api/organizations/%s/reset_rate_limits"
)

// BankedResetClaim is the decoded response of a reset_rate_limits claim.
// Result is one of reset, already_used, not_limited, cooldown, ineligible, or
// unavailable; the remaining fields are advisory detail from the same response.
type BankedResetClaim struct {
	Result         string   `json:"result"`
	Reason         string   `json:"reason,omitempty"`
	ResetsLeft     *int     `json:"resets_left,omitempty"`
	Cleared        []string `json:"cleared,omitempty"`
	WeeklyResetsAt string   `json:"weekly_resets_at,omitempty"`
	CooldownUntil  string   `json:"cooldown_until,omitempty"`
}

// FetchBankedResetStatus retrieves the account's banked-resets status block.
// It returns the raw cedar_ember object so callers keep every field Anthropic
// adds without this package chasing the schema; a null value means the account
// has no banked-resets data (feature off or not granted).
func (o *ClaudeAuth) FetchBankedResetStatus(ctx context.Context, accessToken string) (json.RawMessage, error) {
	body, errFetch := o.fetchOAuthControlPlaneJSON(ctx, UsageURL, accessToken, "usage")
	if errFetch != nil {
		return nil, errFetch
	}
	status := gjson.GetBytes(body, "cedar_ember")
	if !status.Exists() || status.Type == gjson.Null {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(status.Raw), nil
}

// NextBankedResetGrantID picks the grant a claim should spend from a status
// block returned by FetchBankedResetStatus: the server-designated next grant
// when present, otherwise the first grant with resets left that is usable now.
// It returns "" when the status offers nothing to spend.
func NextBankedResetGrantID(status json.RawMessage) string {
	parsed := gjson.ParseBytes(status)
	if next := strings.TrimSpace(parsed.Get("next_grant_id").String()); next != "" {
		return next
	}
	for _, grant := range parsed.Get("grants").Array() {
		if grant.Get("resets_left").Int() > 0 && grant.Get("usable_now").Bool() && !grant.Get("paused").Bool() {
			if id := strings.TrimSpace(grant.Get("id").String()); id != "" {
				return id
			}
		}
	}
	return ""
}

// SpendBankedReset claims one banked reset for the organization. The request id
// is a fresh UUID per attempt, matching the native client; Anthropic uses it to
// deduplicate retries of the same claim.
func (o *ClaudeAuth) SpendBankedReset(ctx context.Context, accessToken, organizationUUID, grantID string) (*BankedResetClaim, error) {
	if o == nil || o.httpClient == nil {
		return nil, fmt.Errorf("spend Claude banked reset: HTTP client is nil")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("spend Claude banked reset: access token is empty")
	}
	organizationUUID = strings.TrimSpace(organizationUUID)
	if organizationUUID == "" {
		return nil, fmt.Errorf("spend Claude banked reset: organization UUID is empty")
	}
	grantID = strings.TrimSpace(grantID)
	if grantID == "" {
		return nil, fmt.Errorf("spend Claude banked reset: grant ID is empty")
	}

	payload, errMarshal := json.Marshal(map[string]string{
		"program":    bankedResetProgram,
		"grant_id":   grantID,
		"request_id": uuid.NewString(),
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("encode Claude banked reset claim: %w", errMarshal)
	}
	endpoint := fmt.Sprintf(resetRateLimitsURLFormat, url.PathEscape(organizationUUID))
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if errRequest != nil {
		return nil, fmt.Errorf("create Claude banked reset claim request: %w", errRequest)
	}
	applyClaudeOAuthAxiosHeaders(req)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("send Claude banked reset claim: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close Claude banked reset claim response body: %v", errClose)
		}
	}()
	body, errRead := readClaudeOAuthResponseBody(resp)
	if errRead != nil {
		return nil, fmt.Errorf("read Claude banked reset claim response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Claude banked reset claim failed with status %d", resp.StatusCode)
	}
	var claim BankedResetClaim
	if errUnmarshal := json.Unmarshal(body, &claim); errUnmarshal != nil {
		return nil, fmt.Errorf("parse Claude banked reset claim response: %w", errUnmarshal)
	}
	if strings.TrimSpace(claim.Result) == "" {
		return nil, fmt.Errorf("Claude banked reset claim response carries no result")
	}
	return &claim, nil
}
