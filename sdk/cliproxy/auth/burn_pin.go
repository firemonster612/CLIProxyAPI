package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// Reset-bound burn pin modes: the pin expires when the credential's observed
// usage window resets instead of after a fixed duration.
const (
	// BurnWindowFiveHour pins until the credential's 5-hour unified window resets.
	BurnWindowFiveHour = "five-hour-reset"
	// BurnWindowWeekly pins until the credential's weekly (7d) unified window resets.
	BurnWindowWeekly = "weekly-reset"
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
	// Mode records how the expiry was chosen: a BurnWindow* constant for
	// reset-bound pins, empty for fixed durations and indefinite pins.
	Mode string `json:"mode,omitempty"`
}

// Expired reports whether the pin has lapsed at the supplied time.
func (p BurnPin) Expired(now time.Time) bool {
	return !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(now)
}

// SetBurnPin pins every request for the credential's provider to that
// credential. A non-positive duration pins indefinitely. Setting a pin
// replaces any existing pin for the same provider.
func (m *Manager) SetBurnPin(authID string, duration time.Duration) (BurnPin, error) {
	var expiresAt time.Time
	if duration > 0 {
		expiresAt = time.Now().Add(duration)
	}
	return m.setBurnPin(authID, expiresAt, "")
}

// SetBurnPinUntilReset pins the credential's provider to that credential until
// the credential's observed usage window resets. window must be
// BurnWindowFiveHour or BurnWindowWeekly. When no future reset instant is
// known for the credential (it has served no request since startup and its
// persisted quota snapshot carries none), the pin is refused with code
// "reset_unknown" rather than guessed.
func (m *Manager) SetBurnPinUntilReset(authID string, window string) (BurnPin, error) {
	if m == nil {
		return BurnPin{}, &Error{Code: "manager_unavailable", Message: "auth manager unavailable"}
	}
	if window != BurnWindowFiveHour && window != BurnWindowWeekly {
		return BurnPin{}, &Error{Code: "invalid_request", Message: "unknown burn window: " + window}
	}
	if m.HomeEnabled() {
		// Same refusal setBurnPin would give; checked here so an until pin
		// fails with the same code as a duration pin instead of reset_unknown.
		return BurnPin{}, &Error{Code: "home_unavailable", Message: "burn pins are unavailable while Home is enabled"}
	}
	authID = strings.TrimSpace(authID)
	m.mu.RLock()
	auth := m.auths[authID]
	var authCopy *Auth
	if auth != nil {
		authCopy = auth.Clone()
	}
	m.mu.RUnlock()
	if authCopy == nil {
		return BurnPin{}, &Error{Code: "auth_not_found", Message: "auth not found"}
	}
	reset, errReset := burnResetTime(authCopy, window, time.Now())
	if errReset != nil {
		return BurnPin{}, errReset
	}
	return m.setBurnPin(authID, reset, window)
}

// burnResetTime resolves the next reset instant for the requested window. The
// in-memory usage-window registry is authoritative; the credential's persisted
// quota signal snapshot is the fallback so reset-bound pins survive a restart
// that empties the registry.
func burnResetTime(auth *Auth, window string, now time.Time) (time.Time, error) {
	short, long, _ := UsageWindowsFor(auth.ID)
	target := short.Reset
	signalKey := "Anthropic-Ratelimit-Unified-5h-Reset"
	if window == BurnWindowWeekly {
		target = long.Reset
		signalKey = "Anthropic-Ratelimit-Unified-7d-Reset"
	}
	if target.After(now) {
		return target, nil
	}
	if parsed, ok := parseResetInstant(auth.Quota.Signals[signalKey]); ok && parsed.After(now) {
		return parsed, nil
	}
	return time.Time{}, &Error{
		Code:    "reset_unknown",
		Message: "no upcoming " + window + " known for this credential yet; send a request through it first or use a fixed duration",
	}
}

// parseResetInstant accepts the reset formats seen in unified rate-limit
// headers: unix seconds (fractional allowed) or an RFC 3339 timestamp,
// matching the executor-side window parser's semantics.
func parseResetInstant(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if seconds, errParse := strconv.ParseFloat(value, 64); errParse == nil {
		return time.Unix(int64(seconds), 0), true
	}
	if parsed, errParse := time.Parse(time.RFC3339, value); errParse == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func (m *Manager) setBurnPin(authID string, expiresAt time.Time, mode string) (BurnPin, error) {
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
	var providerAuthIDs []string
	if provider != "" {
		for id, candidate := range m.auths {
			if candidate != nil && executorKeyFromAuth(candidate) == provider {
				providerAuthIDs = append(providerAuthIDs, id)
			}
		}
	}
	m.mu.RUnlock()
	if auth == nil {
		return BurnPin{}, &Error{Code: "auth_not_found", Message: "auth not found"}
	}
	if provider == "" {
		return BurnPin{}, &Error{Code: "invalid_auth", Message: "auth has no provider"}
	}
	pin := BurnPin{AuthID: authID, Provider: provider, ExpiresAt: expiresAt, Mode: mode}
	m.burnPinMu.Lock()
	if m.burnPins == nil {
		m.burnPins = make(map[string]BurnPin)
	}
	m.burnPins[provider] = pin
	m.burnPinMu.Unlock()
	// Drop every session binding to the provider's credentials so live
	// sessions reroute to the pinned credential now and cannot snap back to a
	// stale binding once the pin lapses. Invalidating by credential rather
	// than by cache-key prefix also covers bindings recorded under the
	// "mixed" routing namespace.
	if selector, ok := m.Selector().(*SessionAffinitySelector); ok {
		for _, id := range providerAuthIDs {
			selector.InvalidateAuth(id)
		}
	}
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
