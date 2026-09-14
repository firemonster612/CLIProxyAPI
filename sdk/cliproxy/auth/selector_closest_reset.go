package auth

import (
	"context"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	// Default window spans, used when a provider does not report one. They
	// match the short (5h) and long (weekly) windows both Anthropic and
	// ChatGPT Codex subscriptions use.
	defaultShortUsageWindow = 5 * time.Hour
	defaultLongUsageWindow  = 7 * 24 * time.Hour

	// Defaults bias selection toward draining credentials whose long (weekly)
	// cap refreshes soonest; the short (5h) window breaks ties inside a
	// similar week position. The long window dominates because it is the
	// scarce budget: quota left unused when a window rolls over is simply
	// lost, and a week of unused headroom costs far more than five hours.
	defaultClosestToResetWeeklyWeight   = 0.7
	defaultClosestToResetFiveHourWeight = 0.3

	// usageWindowProbeInterval limits how often a credential with no observed
	// window data is speculatively selected. Providers that report windows
	// gain data after one probe; credentials that never report (API keys,
	// providers without usage headers) stay bounded to one probe per interval
	// instead of permanently absorbing traffic.
	usageWindowProbeInterval = 5 * time.Minute

	maxUsageWindowEntries = 4096
)

// UsageWindow describes one usage rate-limit window observed for a credential.
type UsageWindow struct {
	// Reset is when the window's usage resets. Zero means unobserved.
	Reset time.Time
	// Span is the window length used to normalize time-to-reset. Non-positive
	// values fall back to the default for the window's slot (5h short, 7d long).
	Span time.Duration
}

// usageWindowEntry stores the most recent usage windows observed for one
// credential, plus the last time the selector probed it while it had no data.
type usageWindowEntry struct {
	short     UsageWindow
	long      UsageWindow
	lastProbe time.Time
}

func (e usageWindowEntry) hasData() bool {
	return !e.short.Reset.IsZero() || !e.long.Reset.IsZero()
}

var usageWindowRegistry = struct {
	mu sync.RWMutex
	m  map[string]usageWindowEntry
}{m: make(map[string]usageWindowEntry)}

// RecordUsageWindows stores the latest observed usage windows for a
// credential. Windows whose Reset is zero or not in the future are ignored
// (a relative or garbage value misparsed as an absolute timestamp must not
// poison scoring), and an ignored window leaves the previously recorded value
// for that slot untouched, so a response that only reports one window does
// not erase knowledge of the other.
func RecordUsageWindows(authID string, short, long UsageWindow) {
	now := time.Now()
	if !short.Reset.After(now) {
		short = UsageWindow{}
	}
	if !long.Reset.After(now) {
		long = UsageWindow{}
	}
	if authID == "" || (short.Reset.IsZero() && long.Reset.IsZero()) {
		return
	}
	usageWindowRegistry.mu.Lock()
	defer usageWindowRegistry.mu.Unlock()
	entry, ok := usageWindowRegistry.m[authID]
	if !ok && len(usageWindowRegistry.m) >= maxUsageWindowEntries {
		usageWindowRegistry.m = make(map[string]usageWindowEntry)
	}
	if !short.Reset.IsZero() {
		entry.short = short
	}
	if !long.Reset.IsZero() {
		entry.long = long
	}
	usageWindowRegistry.m[authID] = entry
}

// UsageWindowsFor returns the most recently observed usage windows for a
// credential. ok reports whether any window has been observed at all; each
// returned window can still individually be zero.
func UsageWindowsFor(authID string) (short, long UsageWindow, ok bool) {
	usageWindowRegistry.mu.RLock()
	defer usageWindowRegistry.mu.RUnlock()
	entry, found := usageWindowRegistry.m[authID]
	if !found || !entry.hasData() {
		return UsageWindow{}, UsageWindow{}, false
	}
	return entry.short, entry.long, true
}

// snapshotUsageWindows reads the registry entries for a candidate set under a
// single read lock.
func snapshotUsageWindows(auths []*Auth) map[string]usageWindowEntry {
	entries := make(map[string]usageWindowEntry, len(auths))
	usageWindowRegistry.mu.RLock()
	defer usageWindowRegistry.mu.RUnlock()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if entry, ok := usageWindowRegistry.m[auth.ID]; ok {
			entries[auth.ID] = entry
		}
	}
	return entries
}

// tryMarkUsageWindowProbe atomically claims the probe slot for a credential
// that still has no window data. It re-checks eligibility under the write
// lock so concurrent picks cannot all elect the same credential and defeat
// the once-per-interval throttle.
func tryMarkUsageWindowProbe(authID string, now time.Time) bool {
	usageWindowRegistry.mu.Lock()
	defer usageWindowRegistry.mu.Unlock()
	entry, ok := usageWindowRegistry.m[authID]
	if entry.hasData() {
		return false
	}
	if !entry.lastProbe.IsZero() && now.Sub(entry.lastProbe) < usageWindowProbeInterval {
		return false
	}
	if !ok && len(usageWindowRegistry.m) >= maxUsageWindowEntries {
		usageWindowRegistry.m = make(map[string]usageWindowEntry)
	}
	entry.lastProbe = now
	usageWindowRegistry.m[authID] = entry
	return true
}

// ClosestToResetSelector prefers the available credential whose usage windows
// reset soonest, so quota that would otherwise expire unused gets burned first
// while freshly reset credentials are preserved as runway. The score blends
// the long (weekly) and short (5h) windows, each normalized to the fraction of
// its span still remaining; lower scores win. Credentials with no observed
// window data are probed (at most once per usageWindowProbeInterval each) to
// bootstrap the registry; if no credential in the pool reports windows at all,
// selection falls back to round-robin.
type ClosestToResetSelector struct {
	// WeeklyWeight and FiveHourWeight bias the score between the long (7d)
	// and short (5h) windows. Non-positive values fall back individually to
	// the defaults (0.7 / 0.3); the pair is normalized, so only the ratio
	// matters.
	WeeklyWeight   float64
	FiveHourWeight float64

	fallback RoundRobinSelector
}

func (s *ClosestToResetSelector) weights() (weekly, fiveHour float64) {
	weekly = s.WeeklyWeight
	if weekly <= 0 {
		weekly = defaultClosestToResetWeeklyWeight
	}
	fiveHour = s.FiveHourWeight
	if fiveHour <= 0 {
		fiveHour = defaultClosestToResetFiveHourWeight
	}
	total := weekly + fiveHour
	return weekly / total, fiveHour / total
}

// windowRemainingFraction reports how much of a rate-limit window is still
// pending as a value in (0, 1]. An elapsed reset means the window rolled over
// and the credential holds a full fresh budget — the farthest possible state
// from its next reset — so it scores 1, keeping fresh credentials preserved
// as runway. Callers must not pass unobserved windows (zero Reset): scoring
// renormalizes over observed slots instead, so absent data never reads as an
// imminent reset. The zero-Reset branch is a defensive guard only.
func windowRemainingFraction(window UsageWindow, now time.Time, defaultSpan time.Duration) float64 {
	if window.Reset.IsZero() {
		return 1
	}
	remaining := window.Reset.Sub(now)
	if remaining <= 0 {
		return 1
	}
	span := window.Span
	if span <= 0 {
		span = defaultSpan
	}
	if remaining >= span {
		return 1
	}
	return float64(remaining) / float64(span)
}

// Pick selects the available auth with the lowest weighted time-to-reset.
func (s *ClosestToResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	entries := snapshotUsageWindows(available)
	weeklyWeight, fiveHourWeight := s.weights()
	bestIndex := -1
	bestScore := 0.0
	var probeCandidates []int
	for i, candidate := range available {
		entry, ok := entries[candidate.ID]
		if ok && entry.hasData() {
			// Renormalize over the slots actually observed so a credential
			// reporting only one window competes on that window alone rather
			// than pocketing the missing slot's weight as false urgency.
			scoreSum := 0.0
			weightSum := 0.0
			if !entry.long.Reset.IsZero() {
				scoreSum += weeklyWeight * windowRemainingFraction(entry.long, now, defaultLongUsageWindow)
				weightSum += weeklyWeight
			}
			if !entry.short.Reset.IsZero() {
				scoreSum += fiveHourWeight * windowRemainingFraction(entry.short, now, defaultShortUsageWindow)
				weightSum += fiveHourWeight
			}
			score := scoreSum / weightSum
			if bestIndex < 0 || score < bestScore {
				bestIndex = i
				bestScore = score
			}
			continue
		}
		if !ok || now.Sub(entry.lastProbe) >= usageWindowProbeInterval {
			probeCandidates = append(probeCandidates, i)
		}
	}
	if bestIndex < 0 {
		// No credential in this pool reports usage windows (yet); spread load
		// instead of pinning the first auth.
		return s.fallback.Pick(ctx, provider, model, opts, available)
	}
	for _, index := range probeCandidates {
		probed := available[index]
		if !tryMarkUsageWindowProbe(probed.ID, now) {
			continue
		}
		selectorLogEntry(ctx).Debugf(
			"closest-to-reset: probing auth without window data | auth=%s provider=%s model=%s",
			probed.ID, provider, model)
		return probed, nil
	}
	picked := available[bestIndex]
	selectorLogEntry(ctx).Debugf(
		"closest-to-reset: picked auth | auth=%s provider=%s model=%s score=%.4f candidates=%d",
		picked.ID, provider, model, bestScore, len(available))
	return picked, nil
}
