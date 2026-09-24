package cliproxy

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const modelDiscoveryInterval = time.Hour

// modelDiscoverer lists the models one provider's upstream offers a credential.
// list returns changed=false with no error when the credential is not
// eligible for discovery or its listing is unchanged.
type modelDiscoverer struct {
	provider string
	list     func(ctx context.Context, exec coreauth.ProviderExecutor, auth *coreauth.Auth) (changed bool, err error)
}

// modelDiscoverers covers every provider whose upstream publishes a per-account
// model listing. Each credential only gains the models it reported.
var modelDiscoverers = []modelDiscoverer{
	{provider: "claude", list: discoverClaudeModels},
	{provider: "codex", list: discoverCodexModels},
}

func discoverClaudeModels(ctx context.Context, exec coreauth.ProviderExecutor, auth *coreauth.Auth) (bool, error) {
	lister, ok := exec.(interface {
		FetchModelList(context.Context, *coreauth.Auth) ([]registry.AnthropicModelPage, error)
	})
	if !ok {
		return false, fmt.Errorf("executor %T cannot list models", exec)
	}
	pages, errFetch := lister.FetchModelList(ctx, auth)
	if errFetch != nil || pages == nil {
		return false, errFetch
	}
	return registry.SetDiscoveredClaudeModels(auth.ID, pages)
}

func discoverCodexModels(ctx context.Context, exec coreauth.ProviderExecutor, auth *coreauth.Auth) (bool, error) {
	lister, ok := exec.(interface {
		FetchModelList(context.Context, *coreauth.Auth) ([]byte, error)
	})
	if !ok {
		return false, fmt.Errorf("executor %T cannot list models", exec)
	}
	body, errFetch := lister.FetchModelList(ctx, auth)
	if errFetch != nil || body == nil {
		return false, errFetch
	}
	return registry.SetDiscoveredCodexModels(auth.ID, body)
}

// runModelDiscovery asks each provider's upstream which models each OAuth
// credential can use, so a model released after the catalog was published is
// routable immediately.
func (s *Service) runModelDiscovery(ctx context.Context) {
	ticker := time.NewTicker(modelDiscoveryInterval)
	defer ticker.Stop()
	for {
		s.discoverModels(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) discoverModels(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	for _, discoverer := range modelDiscoverers {
		exec, ok := s.coreManager.Executor(discoverer.provider)
		if !ok {
			continue
		}
		for _, auth := range s.coreManager.List() {
			if auth == nil || auth.Disabled || auth.Provider != discoverer.provider {
				continue
			}
			changed, errList := discoverer.list(ctx, exec, auth)
			if errList != nil {
				log.WithError(errList).Debugf("%s model discovery failed for auth %s", discoverer.provider, auth.ID)
				continue
			}
			if changed && s.refreshModelRegistrationForAuth(auth) {
				log.Infof("re-registered models for auth %s after %s model discovery", auth.ID, discoverer.provider)
			}
		}
	}
}
