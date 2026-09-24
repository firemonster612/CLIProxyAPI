package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const codexModelListTimeout = 15 * time.Second

// FetchModelList returns the raw ChatGPT Codex /models listing for an OAuth
// credential, so OpenAI models released after this build's catalog become
// routable. API keys and custom base URLs keep their configured model sets. A
// nil result with no error means the credential is not eligible.
func (e *CodexExecutor) FetchModelList(ctx context.Context, auth *cliproxyauth.Auth) ([]byte, error) {
	token, baseURL := codexCreds(auth)
	if token == "" || codexAuthUsesAPIKey(auth) || (baseURL != "" && strings.TrimRight(baseURL, "/") != "https://chatgpt.com/backend-api/codex") {
		return nil, nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, codexModelListTimeout)
	defer cancel()
	query := url.Values{"client_version": {codexClientVersion()}}
	req, errReq := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://chatgpt.com/backend-api/codex/models?"+query.Encode(), nil)
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Originator", codexOriginator)
	// Same precedence as execution requests, so the account presents one
	// client identity to ChatGPT.
	cfgUserAgent, _ := codexHeaderDefaults(e.cfg, auth)
	ensureHeaderWithConfigPrecedence(req.Header, nil, "User-Agent", cfgUserAgent, codexUserAgent())
	if accountID, ok := auth.Metadata["account_id"].(string); ok && accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	applyCodexCloakingHeaders(req.Header, e.cfg, auth)

	resp, errDo := helps.NewUtlsHTTPClient(reqCtx, e.cfg, auth, codexModelListTimeout).Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("codex model list: close response body: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codex model list returned status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
