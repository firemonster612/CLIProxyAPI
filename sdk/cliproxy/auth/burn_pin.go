package auth

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// BurnPin routes every request for one provider to a single credential until
// the pin expires or is cleared. While the pinned credential is cooling down
// or otherwise unavailable the configured strategy takes over, and the pin
// re-engages on its own as soon as the credential recovers.
type BurnPin struct {
	// AuthID is the pinned credential.
	AuthID string `json:"auth_id"`
	// Provider is the executor key whose requests the pin captures.
	Provider string `json:"provider"`
	// ExpiresAt is when the pin lapses. Zero means indefinite.
	ExpiresAt time.Time `json:"expires_at"`
}

// Expired reports whether the pin has lapsed at the supplied time.
func (p BurnPin) Expired(now time.Time) bool {
	return !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(now)
}

// SetBurnPin pins every request for the credential's provider to that
// credential. A non-positive duration pins indefinitely. Setting a pin
// replaces any existing pin for the same provider.
func (m *Manager) SetBurnPin(authID string, duration time.Duration) (BurnPin, error) {
	if m == nil {
		return BurnPin{}, &Error{Code: "manager_unavailable", Message: "auth manager unavailable"}
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return BurnPin{}, &Error{Code: "invalid_request", Message: "auth id is required"}
	}
	if m.HomeEnabled() {
		// Home dispatch bypasses local selection entirely; refusing here keeps
		// the API from accepting a pin that would never take effect.
		return BurnPin{}, &Error{Code: "home_unavailable", Message: "burn pins are unavailable while Home is enabled"}
	}
	m.mu.RLock()
	auth := m.auths[authID]
	provider := ""
	if auth != nil {
		provider = executorKeyFromAuth(auth)
	}
	m.mu.RUnlock()
	if auth == nil {
		return BurnPin{}, &Error{Code: "auth_not_found", Message: "auth not found"}
	}
	if provider == "" {
		return BurnPin{}, &Error{Code: "invalid_auth", Message: "auth has no provider"}
	}
	pin := BurnPin{AuthID: authID, Provider: provider}
	if duration > 0 {
		pin.ExpiresAt = time.Now().Add(duration)
	}
	m.burnPinMu.Lock()
	if m.burnPins == nil {
		m.burnPins = make(map[string]BurnPin)
	}
	m.burnPins[provider] = pin
	m.burnPinMu.Unlock()
	return pin, nil
}

// ClearBurnPinForAuth removes any pin bound to the credential and reports
// whether one was removed.
func (m *Manager) ClearBurnPinForAuth(authID string) bool {
	if m == nil {
		return false
	}
	authID = strings.TrimSpace(authID)
	m.burnPinMu.Lock()
	defer m.burnPinMu.Unlock()
	for provider, pin := range m.burnPins {
		if pin.AuthID == authID {
			delete(m.burnPins, provider)
			return true
		}
	}
	return false
}

// ClearBurnPinForProvider removes the pin for a provider regardless of which
// credential it names. It is the escape hatch for a pin whose credential no
// longer resolves.
func (m *Manager) ClearBurnPinForProvider(provider string) bool {
	if m == nil {
		return false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	m.burnPinMu.Lock()
	defer m.burnPinMu.Unlock()
	if _, ok := m.burnPins[provider]; !ok {
		return false
	}
	delete(m.burnPins, provider)
	return true
}

// BurnPins returns the active pins, pruning any that have expired.
func (m *Manager) BurnPins() []BurnPin {
	if m == nil {
		return nil
	}
	now := time.Now()
	m.burnPinMu.Lock()
	defer m.burnPinMu.Unlock()
	pins := make([]BurnPin, 0, len(m.burnPins))
	for provider, pin := range m.burnPins {
		if pin.Expired(now) {
			delete(m.burnPins, provider)
			continue
		}
		pins = append(pins, pin)
	}
	return pins
}

// hasBurnPins reports whether any pin is still live, so the pick hot path
// costs one RLock once every pin has lapsed or been cleared.
func (m *Manager) hasBurnPins(now time.Time) bool {
	m.burnPinMu.RLock()
	defer m.burnPinMu.RUnlock()
	for _, pin := range m.burnPins {
		if !pin.Expired(now) {
			return true
		}
	}
	return false
}

// burnPinFor returns the active pin for a provider. The common live-pin case
// stays on the read lock; the write lock is only taken to prune an expired
// entry.
func (m *Manager) burnPinFor(provider string, now time.Time) (BurnPin, bool) {
	m.burnPinMu.RLock()
	pin, ok := m.burnPins[provider]
	m.burnPinMu.RUnlock()
	if !ok {
		return BurnPin{}, false
	}
	if !pin.Expired(now) {
		return pin, true
	}
	m.burnPinMu.Lock()
	if current, stillThere := m.burnPins[provider]; stillThere && current.Expired(now) {
		delete(m.burnPins, provider)
	}
	m.burnPinMu.Unlock()
	return BurnPin{}, false
}

// burnPinnedPick returns the pinned credential for one of the candidate
// providers when a pin is active and the credential can serve this request
// right now. It returns nils when no pin applies, the credential is cooling
// down, disabled, already tried for this request, or cannot serve the route
// model — the caller then proceeds with the configured strategy, which is
// exactly the burn feature's fallback semantics.
func (m *Manager) burnPinnedPick(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string) {
	now := time.Now()
	if m == nil || !m.hasBurnPins(now) {
		return nil, nil, ""
	}
	// A request that pins its own credential (existing metadata mechanism)
	// keeps that stronger, request-scoped pin.
	if pinnedAuthIDFromMetadata(opts.Metadata) != "" {
		return nil, nil, ""
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	registryRef := registry.GetGlobalRegistry()
	for _, provider := range providers {
		providerKey := strings.ToLower(strings.TrimSpace(provider))
		if providerKey == "" || providerKey == "mixed" {
			continue
		}
		pin, ok := m.burnPinFor(providerKey, now)
		if !ok {
			continue
		}
		skip := func(reason string) {
			selectorLogEntry(ctx).Debugf(
				"burn-pin: pin skipped, strategy takes over | auth=%s provider=%s model=%s reason=%s",
				pin.AuthID, providerKey, model, reason)
		}
		if _, used := tried[pin.AuthID]; used {
			skip("already_tried")
			continue
		}
		executor, okExecutor := m.Executor(providerKey)
		if !okExecutor {
			skip("executor_missing")
			continue
		}
		m.mu.RLock()
		selected := m.auths[pin.AuthID]
		reason := ""
		switch {
		case selected == nil:
			reason = "auth_missing"
		case selected.Disabled:
			reason = "auth_disabled"
		case executorKeyFromAuth(selected) != providerKey:
			reason = "provider_mismatch"
		case !eligibility.allows(selected):
			reason = "ineligible"
		case !m.authSupportsRouteModel(registryRef, selected, model):
			reason = "model_unsupported"
		default:
			if blocked, _, _ := isAuthBlockedForModel(selected, model, now); blocked {
				reason = "cooling_down"
			}
		}
		var authCopy *Auth
		if reason == "" {
			authCopy = selected.Clone()
		}
		m.mu.RUnlock()
		if authCopy == nil {
			skip(reason)
			continue
		}
		if !selected.indexAssigned {
			m.mu.Lock()
			if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
				current.EnsureIndex()
				authCopy = current.Clone()
			}
			m.mu.Unlock()
		}
		selectorLogEntry(ctx).Debugf(
			"burn-pin: routed to pinned auth | auth=%s provider=%s model=%s",
			authCopy.ID, providerKey, model)
		return authCopy, executor, providerKey
	}
	return nil, nil, ""
}
