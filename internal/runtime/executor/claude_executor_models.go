package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	claudeModelListTimeout  = 15 * time.Second
	claudeModelListMaxPages = 10
)

// FetchModelList returns every page of the Anthropic /v1/models listing for an
// OAuth credential, so Claude models released after this build's catalog become
// routable. Only first-party OAuth credentials are listed: API keys and
// third-party base URLs keep their configured model sets. A nil result with no
// error means the credential is not eligible.
func (e *ClaudeExecutor) FetchModelList(ctx context.Context, auth *cliproxyauth.Auth) ([]registry.AnthropicModelPage, error) {
	apiKey, baseURL := claudeCreds(auth)
	if apiKey == "" || auth.AuthKind() != cliproxyauth.AuthKindOAuth || (baseURL != "" && !isAnthropicUpstreamBase(baseURL)) {
		return nil, nil
	}
	client := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, claudeModelListTimeout)
	var pages []registry.AnthropicModelPage
	afterID := ""
	for range claudeModelListMaxPages {
		page, errPage := e.fetchModelPage(ctx, client, apiKey, afterID)
		if errPage != nil {
			return nil, errPage
		}
		pages = append(pages, page)
		if !page.HasMore || page.LastID == "" {
			return pages, nil
		}
		afterID = page.LastID
	}
	return nil, fmt.Errorf("claude model list exceeded %d pages", claudeModelListMaxPages)
}

// fetchModelPage mirrors the request Claude Code 2.1.282 itself sends for
// gateway model discovery: a bare fetch with only these headers, not the
// Stainless SDK envelope used for /v1/messages.
func (e *ClaudeExecutor) fetchModelPage(ctx context.Context, client *http.Client, token, afterID string) (registry.AnthropicModelPage, error) {
	query := url.Values{"limit": {"1000"}}
	if afterID != "" {
		query.Set("after_id", afterID)
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models?"+query.Encode(), nil)
	if errReq != nil {
		return registry.AnthropicModelPage{}, errReq
	}
	req.Header.Set("User-Agent", "claude-code/"+helps.DefaultClaudeVersion(e.cfg))
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return registry.AnthropicModelPage{}, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("claude model list: close response body: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return registry.AnthropicModelPage{}, fmt.Errorf("claude model list returned status %d", resp.StatusCode)
	}
	decoded, errDecode := decodeResponseBody(resp.Body, claudeResponseContentEncoding(resp.Header))
	if errDecode != nil {
		return registry.AnthropicModelPage{}, errDecode
	}
	defer func() {
		if errClose := decoded.Close(); errClose != nil {
			log.Debugf("claude model list: close decoded body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(decoded, 1<<20))
	if errRead != nil {
		return registry.AnthropicModelPage{}, errRead
	}
	return registry.ParseAnthropicModelPage(body)
}
