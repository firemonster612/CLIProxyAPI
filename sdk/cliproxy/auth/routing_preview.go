package auth

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// Routing preview outcomes.
const (
	routingReasonPinned             = "pinned"
	routingReasonClosestToReset     = "closest-to-reset"
	routingReasonProbe              = "probe"
	routingReasonRoundRobinFallback = "round-robin-fallback"
	routingReasonOnlyAvailable      = "only-available"
	routingReasonNoneAvailable      = "none-available"
)

// Decisions a routing preview cannot reproduce locally.
const (
	routingUnsupportedHomeDispatch    = "home_dispatch"
	routingUnsupportedPluginScheduler = "plugin_scheduler"
	routingUnsupportedStrategy        = "unsupported_strategy"
)

// Reasons a provider credential is excluded from a routing preview.
const (
	routingExcludedDisabled         = "disabled"
	routingExcludedQuotaExceeded    = "quota_exceeded"
	routingExcludedCooldown         = "cooldown"
	routingExcludedExpiredToken     = "expired_token"
	routingExcludedUnauthorized     = "unauthorized"
	routingExcludedModelUnsupported = "model_unsupported"
	routingExcludedLowerPriority    = "lower_priority"
	routingExcludedExecutorMissing  = "executor_missing"
)

// RoutingPreview is a snapshot of where a new request for one provider and
// model, carrying no session context, would be routed.
type RoutingPreview struct {
	Provider               string
	Model                  string
	Strategy               string
	SessionAffinityEnabled bool
	// Supported is false when something the preview cannot reproduce makes the
	// decision; UnsupportedReason then names it and no credential is chosen.
	Supported         bool
	UnsupportedReason string
	// ChosenAuthIndex is empty when no credential would be chosen.
	ChosenAuthIndex string
	Reason          string
	Candidates      []RoutingCandidate
	Excluded        []RoutingExclusion
}

// RoutingCandidate is a credential the strategy chooses among.
type RoutingCandidate struct {
	AuthIndex string
	Priority  int
	// Score is the closest-to-reset score (lower wins), nil without observed
	// usage windows or under another strategy.
	Score         *float64
	ProbeEligible bool
}

// RoutingExclusion is a provider credential that cannot serve the request.
type RoutingExclusion struct {
	AuthIndex string
	Reason    string
	// RetryAt is when a temporary block lifts; zero when unknown.
	RetryAt time.Time
}

// PreviewRouting reports where a new request without session context would
// go through the execution path's mixed selection, constrained to provider.
// It reads auth, pin, usage-window and rotation state under their locks and
// changes none of it: probes are not claimed, expired pins are not pruned,
// indexes are not assigned and the rotation cursor does not advance. The
// result is a snapshot, not a reservation.
func (m *Manager) PreviewRouting(provider, model string, now time.Time) (RoutingPreview, error) {
	providerKey := canonicalSchedulingProvider(provider)
	model = strings.TrimSpace(model)
	if providerKey == "" {
		return RoutingPreview{}, &Error{Code: "invalid_request", Message: "provider is required", HTTPStatus: http.StatusBadRequest}
	}
	// Home dispatch decides before any local check, and the manager holds no
	// local credentials while Home is enabled, so model and credentials are
	// not required.
	if m.HomeEnabled() {
		return unsupportedRoutingPreview(RoutingPreview{Provider: providerKey, Model: model}, routingUnsupportedHomeDispatch), nil
	}
	if model == "" {
		return RoutingPreview{}, &Error{Code: "invalid_request", Message: "model is required", HTTPStatus: http.StatusBadRequest}
	}
	pin, hasPin := m.peekBurnPin(providerKey, now)
	pluginScheduler := m.hasPluginScheduler()
	registryRef := registry.GetGlobalRegistry()

	m.mu.RLock()
	selector := m.selector
	members := m.providerAuthsLocked(providerKey)
	var pinned *Auth
	if _, okExecutor := m.executorLocked(providerKey); hasPin && okExecutor {
		if auth := m.auths[pin.AuthID]; m.burnPinSkipReasonLocked(auth, providerKey, model, authSelectionEligibility{}, registryRef, now) == "" {
			pinned = auth.Clone()
		}
	}
	available, excluded := m.routingCandidatesLocked(members, model, registryRef, now)
	m.mu.RUnlock()

	if len(members) == 0 {
		return RoutingPreview{}, &Error{Code: "provider_not_found", Message: "no credentials for provider " + providerKey, HTTPStatus: http.StatusBadRequest}
	}
	if pinned != nil {
		available, excluded = withPinnedCandidate(available, excluded, pinned)
	}

	preview := RoutingPreview{Provider: providerKey, Model: model, Supported: true, Excluded: excluded}
	base := selector
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		preview.SessionAffinityEnabled = true
		base = affinity.fallback
	}
	preview.Strategy = routingStrategyName(base)
	preview.Candidates = make([]RoutingCandidate, len(available))
	for i, auth := range available {
		preview.Candidates[i] = RoutingCandidate{AuthIndex: auth.peekIndex(), Priority: authPriority(auth)}
	}

	// Mirror pickNextMixed: Home dispatch (handled above), then an eligible
	// burn pin, then a plugin scheduler, then the configured selector.
	switch {
	case pinned != nil:
		preview.ChosenAuthIndex, preview.Reason = pinned.peekIndex(), routingReasonPinned
		return preview, nil
	case pluginScheduler:
		return unsupportedRoutingPreview(preview, routingUnsupportedPluginScheduler), nil
	}
	closestToReset, ok := base.(*ClosestToResetSelector)
	if !ok {
		return unsupportedRoutingPreview(preview, routingUnsupportedStrategy), nil
	}
	if len(available) == 0 {
		preview.Reason = routingReasonNoneAvailable
		return preview, nil
	}

	scores, best := closestToReset.rankClosestToReset(available, snapshotUsageWindows(available), now)
	probe := -1
	for i, score := range scores {
		if score.scored {
			preview.Candidates[i].Score = &scores[i].score
		}
		// Probes only run while some candidate has window data.
		preview.Candidates[i].ProbeEligible = score.probe && best >= 0
		if probe < 0 && preview.Candidates[i].ProbeEligible {
			probe = i
		}
	}
	var chosen *Auth
	switch {
	case best < 0:
		// The fallback rotation is keyed by the "mixed" provider that
		// execution passes to the selector.
		chosen, preview.Reason = closestToReset.fallback.peekNext("mixed", model, available), routingReasonRoundRobinFallback
	case probe >= 0:
		// Pick claims the first eligible probe; a concurrent pick that claims
		// it first changes the real outcome.
		chosen, preview.Reason = available[probe], routingReasonProbe
	default:
		chosen, preview.Reason = available[best], routingReasonClosestToReset
	}
	preview.ChosenAuthIndex = chosen.peekIndex()
	if len(available) == 1 {
		preview.Reason = routingReasonOnlyAvailable
	}
	return preview, nil
}

func unsupportedRoutingPreview(preview RoutingPreview, reason string) RoutingPreview {
	preview.Supported = false
	preview.UnsupportedReason = reason
	return preview
}

// withPinnedCandidate lists an eligible pinned credential as a candidate. The
// pin bypasses the priority tier and the selector's own availability pass, so
// the candidate filters may have excluded it.
func withPinnedCandidate(available []*Auth, excluded []RoutingExclusion, pinned *Auth) ([]*Auth, []RoutingExclusion) {
	pinnedIndex := pinned.peekIndex()
	kept := make([]RoutingExclusion, 0, len(excluded))
	for _, exclusion := range excluded {
		if exclusion.AuthIndex != pinnedIndex {
			kept = append(kept, exclusion)
		}
	}
	for _, auth := range available {
		if auth.ID == pinned.ID {
			return available, kept
		}
	}
	available = append(available, pinned)
	sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	return available, kept
}

// peekBurnPin returns the provider's live pin without pruning expired pins.
func (m *Manager) peekBurnPin(providerKey string, now time.Time) (BurnPin, bool) {
	m.burnPinMu.RLock()
	defer m.burnPinMu.RUnlock()
	pin, ok := m.burnPins[providerKey]
	return pin, ok && !pin.Expired(now)
}

// providerAuthsLocked returns the provider's credentials sorted by ID. m.mu must be held.
func (m *Manager) providerAuthsLocked(providerKey string) []*Auth {
	var members []*Auth
	for _, auth := range m.auths {
		if auth != nil && executorKeyFromAuth(auth) == providerKey {
			members = append(members, auth)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return members
}

// routingCandidatesLocked applies the mixed selection path's filters to the
// provider's credentials: membership checks, the manager's per-auth
// availability pass, the highest priority tier, and the selector's own second
// availability pass on the route model. It returns clones of the survivors,
// ID-sorted, and the reason every other credential was dropped. m.mu must be held.
//
// It duplicates those filters to report a reason for each exclusion. Keep it
// in step with the candidate loop in pickNextMixedLegacy,
// availableAuthsForRouteModelWithPriorityMode, and getAvailableAuths as
// called by ClosestToResetSelector.Pick.
func (m *Manager) routingCandidatesLocked(members []*Auth, routeModel string, registryRef *registry.ModelRegistry, now time.Time) ([]*Auth, []RoutingExclusion) {
	var excluded []RoutingExclusion
	exclude := func(auth *Auth, reason string, retryAt time.Time) {
		excluded = append(excluded, RoutingExclusion{AuthIndex: auth.peekIndex(), Reason: reason, RetryAt: retryAt})
	}
	var eligible []*Auth
	bestPriority := 0
	for _, auth := range members {
		switch {
		case auth.Disabled:
			exclude(auth, routingExcludedDisabled, time.Time{})
			continue
		case !m.hasExecutorLocked(executorKeyFromAuth(auth)):
			exclude(auth, routingExcludedExecutorMissing, time.Time{})
			continue
		case !m.authSupportsRouteModel(registryRef, auth, routeModel):
			exclude(auth, routingExcludedModelUnsupported, time.Time{})
			continue
		}
		if blocked, reason, next := isAuthBlockedForModel(auth, m.selectionModelForAuth(auth, routeModel), now); blocked {
			exclude(auth, routingBlockReason(auth, reason, now), next)
			continue
		}
		if priority := authPriority(auth); len(eligible) == 0 || priority > bestPriority {
			bestPriority = priority
		}
		eligible = append(eligible, auth)
	}

	var available []*Auth
	for _, auth := range eligible {
		if authPriority(auth) != bestPriority {
			exclude(auth, routingExcludedLowerPriority, time.Time{})
			continue
		}
		if blocked, reason, next := isAuthBlockedForModel(auth, routeModel, now); blocked {
			exclude(auth, routingBlockReason(auth, reason, now), next)
			continue
		}
		available = append(available, auth.Clone())
	}
	return available, excluded
}

func (m *Manager) hasExecutorLocked(providerKey string) bool {
	_, ok := m.executorLocked(providerKey)
	return ok
}

// routingBlockReason names an isAuthBlockedForModel result, checking causes in
// the order that function does.
func routingBlockReason(auth *Auth, reason blockReason, now time.Time) string {
	switch {
	case reason == blockReasonDisabled:
		return routingExcludedDisabled
	case reason == blockReasonCooldown:
		return routingExcludedQuotaExceeded
	case hasUnauthorizedAuthFailure(auth):
		return routingExcludedUnauthorized
	case accessTokenExpired(auth, now):
		return routingExcludedExpiredToken
	default:
		return routingExcludedCooldown
	}
}

func routingStrategyName(selector Selector) string {
	switch selector.(type) {
	case *RoundRobinSelector:
		return "round-robin"
	case *WeightedRoundRobinSelector:
		return "weighted-round-robin"
	case *FillFirstSelector:
		return "fill-first"
	case *ClosestToResetSelector:
		return "closest-to-reset"
	default:
		return "custom"
	}
}

// RepresentativeModel returns the registered model served by the most enabled
// credentials of provider, breaking ties by model ID, or "" when none is registered.
func (m *Manager) RepresentativeModel(provider string) string {
	providerKey := canonicalSchedulingProvider(provider)
	m.mu.RLock()
	var authIDs []string
	for _, auth := range m.providerAuthsLocked(providerKey) {
		if !auth.Disabled {
			authIDs = append(authIDs, auth.ID)
		}
	}
	m.mu.RUnlock()

	registryRef := registry.GetGlobalRegistry()
	counts := make(map[string]int)
	for _, authID := range authIDs {
		for _, info := range registryRef.GetModelsForClient(authID) {
			if info != nil && info.ID != "" {
				counts[info.ID]++
			}
		}
	}
	best := ""
	for modelID, count := range counts {
		if best == "" || count > counts[best] || (count == counts[best] && modelID < best) {
			best = modelID
		}
	}
	return best
}
