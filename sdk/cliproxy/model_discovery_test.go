package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// The registered executors must implement the listers the discoverers expect;
// a mismatch would silently turn discovery off for that provider.
func TestModelDiscoverersAcceptRegisteredExecutors(t *testing.T) {
	cfg := &config.Config{}
	executors := map[string]coreauth.ProviderExecutor{
		"claude": executor.NewClaudeExecutor(cfg),
		"codex":  executor.NewCodexAutoExecutor(cfg),
	}
	apiKeyAuth := &coreauth.Auth{ID: "k", Attributes: map[string]string{"api_key": "sk-test"}}
	for _, discoverer := range modelDiscoverers {
		exec, ok := executors[discoverer.provider]
		if !ok {
			t.Fatalf("no executor for discoverer %q", discoverer.provider)
		}
		if _, err := discoverer.list(context.Background(), exec, apiKeyAuth); err != nil {
			t.Errorf("%s: %v", discoverer.provider, err)
		}
	}
}
