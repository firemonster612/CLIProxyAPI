package cliproxy

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const claudeModelDiscoveryInterval = time.Hour

type claudeModelLister interface {
	FetchModelList(ctx context.Context, auth *coreauth.Auth) ([]registry.AnthropicModelPage, error)
}

// runClaudeModelDiscovery asks Anthropic which models each Claude OAuth
// credential can use, so a model released after the catalog was published is
// routable immediately. Each credential only gains the models it reported.
func (s *Service) runClaudeModelDiscovery(ctx context.Context) {
	ticker := time.NewTicker(claudeModelDiscoveryInterval)
	defer ticker.Stop()
	for {
		s.discoverClaudeModels(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) discoverClaudeModels(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	exec, ok := s.coreManager.Executor("claude")
	if !ok {
		return
	}
	lister, ok := exec.(claudeModelLister)
	if !ok {
		return
	}
	for _, auth := range s.coreManager.List() {
		if auth == nil || auth.Disabled || auth.Provider != "claude" {
			continue
		}
		pages, errFetch := lister.FetchModelList(ctx, auth)
		if errFetch != nil {
			log.WithError(errFetch).Debugf("claude model discovery failed for auth %s", auth.ID)
			continue
		}
		if pages == nil {
			continue
		}
		changed, errSet := registry.SetDiscoveredClaudeModels(auth.ID, pages)
		if errSet != nil {
			log.WithError(errSet).Warnf("claude model discovery returned an unusable model list for auth %s", auth.ID)
			continue
		}
		if changed && s.refreshModelRegistrationForAuth(auth) {
			log.Infof("re-registered models for auth %s after Claude model discovery", auth.ID)
		}
	}
}
