package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const previewTestModel = "routing-preview-test-model"

// newPreviewTestManager mirrors the live routing setup: closest-to-reset behind
// session affinity, with every auth serving previewTestModel unless listed in
// unsupported.
func newPreviewTestManager(t *testing.T, auths []*Auth, unsupported ...string) (*Manager, *ClosestToResetSelector) {
	t.Helper()
	m := newBurnPinTestManager(t, auths...)
	closestToReset := &ClosestToResetSelector{}
	m.SetSelector(NewSessionAffinitySelector(closestToReset))
	skip := make(map[string]bool, len(unsupported))
	for _, id := range unsupported {
		skip[id] = true
	}
	for _, auth := range auths {
		if skip[auth.ID] {
			continue
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: previewTestModel}})
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	})
	return m, closestToReset
}

func claudeAuths(ids ...string) []*Auth {
	auths := make([]*Auth, len(ids))
	for i, id := range ids {
		auths[i] = &Auth{ID: id, Provider: "claude"}
	}
	return auths
}

func (m *Manager) mutateTestAuth(id string, mutate func(*Auth)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mutate(m.auths[id])
}

// previewThenPick checks the preview's choice against the expected credential
// and reason, then makes the real execution-path pick and checks it agrees.
func previewThenPick(t *testing.T, m *Manager, wantID, wantReason string) RoutingPreview {
	t.Helper()
	preview, err := m.PreviewRouting("claude", previewTestModel, time.Now())
	if err != nil {
		t.Fatalf("PreviewRouting() error = %v", err)
	}
	m.mu.RLock()
	wantIndex := m.auths[wantID].peekIndex()
	m.mu.RUnlock()
	if preview.ChosenAuthIndex != wantIndex || preview.Reason != wantReason {
		t.Fatalf("preview chose %q (%s), want %s %q (%s)", preview.ChosenAuthIndex, preview.Reason, wantID, wantIndex, wantReason)
	}
	picked, _, _, err := m.pickNextMixed(context.Background(), []string{"claude"}, previewTestModel, cliproxyexecutor.Options{}, map[string]struct{}{})
	if err != nil {
		t.Fatalf("pickNextMixed() error = %v", err)
	}
	if picked.ID != wantID {
		t.Fatalf("real pick = %q, preview predicted %q", picked.ID, wantID)
	}
	return preview
}

func TestPreviewRoutingAgreesWithClosestToResetPick(t *testing.T) {
	now := time.Now()
	recordTestWindows("pv-ctr-a", now.Add(4*time.Hour), now.Add(time.Hour))
	recordTestWindows("pv-ctr-b", now.Add(time.Hour), now.Add(6*24*time.Hour))
	m, _ := newPreviewTestManager(t, claudeAuths("pv-ctr-a", "pv-ctr-b", "pv-ctr-disabled", "pv-ctr-quota", "pv-ctr-low", "pv-ctr-other-model"), "pv-ctr-other-model")
	m.mutateTestAuth("pv-ctr-disabled", func(a *Auth) { a.Disabled = true })
	retryAt := now.Add(time.Hour).Truncate(time.Second)
	m.mutateTestAuth("pv-ctr-quota", func(a *Auth) {
		a.ModelStates = map[string]*ModelState{previewTestModel: {
			Unavailable:    true,
			NextRetryAfter: retryAt,
			Quota:          QuotaState{Exceeded: true, NextRecoverAt: retryAt},
		}}
	})
	m.mutateTestAuth("pv-ctr-low", func(a *Auth) { a.Attributes = map[string]string{"priority": "-1"} })

	preview := previewThenPick(t, m, "pv-ctr-a", routingReasonClosestToReset)

	if !preview.Supported || preview.Strategy != "closest-to-reset" || !preview.SessionAffinityEnabled {
		t.Fatalf("preview = supported %v strategy %q affinity %v, want supported closest-to-reset with affinity", preview.Supported, preview.Strategy, preview.SessionAffinityEnabled)
	}
	if len(preview.Candidates) != 2 || preview.Candidates[0].Score == nil || preview.Candidates[1].Score == nil || *preview.Candidates[0].Score >= *preview.Candidates[1].Score {
		t.Fatalf("candidates = %+v, want a and b scored with a lower", preview.Candidates)
	}
	m.mu.RLock()
	wantExcluded := map[string]RoutingExclusion{}
	for id, exclusion := range map[string]RoutingExclusion{
		"pv-ctr-disabled":    {Reason: routingExcludedDisabled},
		"pv-ctr-low":         {Reason: routingExcludedLowerPriority},
		"pv-ctr-other-model": {Reason: routingExcludedModelUnsupported},
		"pv-ctr-quota":       {Reason: routingExcludedQuotaExceeded, RetryAt: retryAt},
	} {
		exclusion.AuthIndex = m.auths[id].peekIndex()
		wantExcluded[exclusion.AuthIndex] = exclusion
	}
	m.mu.RUnlock()
	gotExcluded := make(map[string]RoutingExclusion, len(preview.Excluded))
	for _, exclusion := range preview.Excluded {
		gotExcluded[exclusion.AuthIndex] = exclusion
	}
	if !reflect.DeepEqual(gotExcluded, wantExcluded) {
		t.Fatalf("excluded = %+v\nwant       %+v", gotExcluded, wantExcluded)
	}
}

func TestPreviewRoutingAgreesWithProbePick(t *testing.T) {
	now := time.Now()
	recordTestWindows("pv-probe-a", now.Add(time.Hour), now.Add(time.Hour))
	m, _ := newPreviewTestManager(t, claudeAuths("pv-probe-a", "pv-probe-b"))

	preview := previewThenPick(t, m, "pv-probe-b", routingReasonProbe)
	if preview.Candidates[0].ProbeEligible || !preview.Candidates[1].ProbeEligible || preview.Candidates[1].Score != nil {
		t.Fatalf("candidates = %+v, want only the unscored b probe-eligible", preview.Candidates)
	}
	// The real pick claimed the probe, so the next request goes to the scored credential.
	previewThenPick(t, m, "pv-probe-a", routingReasonClosestToReset)
}

func TestPreviewRoutingAgreesWithRoundRobinFallback(t *testing.T) {
	m, _ := newPreviewTestManager(t, claudeAuths("pv-rr-a", "pv-rr-b", "pv-rr-c"))

	previewThenPick(t, m, "pv-rr-a", routingReasonRoundRobinFallback)
	previewThenPick(t, m, "pv-rr-b", routingReasonRoundRobinFallback)
	previewThenPick(t, m, "pv-rr-c", routingReasonRoundRobinFallback)
	previewThenPick(t, m, "pv-rr-a", routingReasonRoundRobinFallback)
}

func TestPreviewRoutingAgreesWithBurnPin(t *testing.T) {
	now := time.Now()
	recordTestWindows("pv-pin-a", now.Add(time.Hour), now.Add(time.Hour))
	recordTestWindows("pv-pin-b", now.Add(4*time.Hour), now.Add(6*24*time.Hour))
	m, _ := newPreviewTestManager(t, claudeAuths("pv-pin-a", "pv-pin-b"))
	if _, err := m.SetBurnPin("pv-pin-b", 0); err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}

	previewThenPick(t, m, "pv-pin-b", routingReasonPinned)

	// A cooling pinned credential yields to the strategy.
	m.mutateTestAuth("pv-pin-b", func(a *Auth) {
		a.Unavailable = true
		a.NextRetryAfter = now.Add(time.Hour)
	})
	previewThenPick(t, m, "pv-pin-a", routingReasonOnlyAvailable)
}

func TestPreviewRoutingReportsOnlyAndNoneAvailable(t *testing.T) {
	m, _ := newPreviewTestManager(t, claudeAuths("pv-one-a", "pv-one-b"))
	m.mutateTestAuth("pv-one-b", func(a *Auth) { a.Disabled = true })
	previewThenPick(t, m, "pv-one-a", routingReasonOnlyAvailable)

	m.mutateTestAuth("pv-one-a", func(a *Auth) { a.Disabled = true })
	preview, err := m.PreviewRouting("claude", previewTestModel, time.Now())
	if err != nil {
		t.Fatalf("PreviewRouting() error = %v", err)
	}
	if preview.ChosenAuthIndex != "" || preview.Reason != routingReasonNoneAvailable {
		t.Fatalf("preview chose %q (%s), want none-available", preview.ChosenAuthIndex, preview.Reason)
	}
	if _, _, _, errPick := m.pickNextMixed(context.Background(), []string{"claude"}, previewTestModel, cliproxyexecutor.Options{}, map[string]struct{}{}); errPick == nil {
		t.Fatal("real pick succeeded, preview predicted none available")
	}
}

func TestPreviewRoutingReportsUnsupportedStrategyAndUnknownProvider(t *testing.T) {
	m, _ := newPreviewTestManager(t, claudeAuths("pv-unsupported-a"))
	m.SetSelector(&RoundRobinSelector{})
	preview, err := m.PreviewRouting("claude", previewTestModel, time.Now())
	if err != nil {
		t.Fatalf("PreviewRouting() error = %v", err)
	}
	if preview.Supported || preview.UnsupportedReason != routingUnsupportedStrategy || preview.ChosenAuthIndex != "" || len(preview.Candidates) != 1 {
		t.Fatalf("preview = %+v, want unsupported_strategy with the candidate listed", preview)
	}

	if _, err := m.PreviewRouting("codex", previewTestModel, time.Now()); err == nil {
		t.Fatal("PreviewRouting(codex) error = nil, want unknown provider")
	}
}

type routingStateSnapshot struct {
	pins          map[string]BurnPin
	windows       map[string]usageWindowEntry
	cursor        map[string]string
	sessions      int
	indexAssigned map[string]bool
}

func snapshotRoutingState(m *Manager, closestToReset *ClosestToResetSelector, authIDs []string) routingStateSnapshot {
	snapshot := routingStateSnapshot{
		pins:          make(map[string]BurnPin),
		windows:       make(map[string]usageWindowEntry),
		cursor:        make(map[string]string),
		indexAssigned: make(map[string]bool),
	}
	m.burnPinMu.RLock()
	for provider, pin := range m.burnPins {
		snapshot.pins[provider] = pin
	}
	m.burnPinMu.RUnlock()
	usageWindowRegistry.mu.RLock()
	for _, id := range authIDs {
		if entry, ok := usageWindowRegistry.m[id]; ok {
			snapshot.windows[id] = entry
		}
	}
	usageWindowRegistry.mu.RUnlock()
	closestToReset.fallback.mu.Lock()
	for key, id := range closestToReset.fallback.lastPicked {
		snapshot.cursor[key] = id
	}
	closestToReset.fallback.mu.Unlock()
	m.mu.RLock()
	cache := m.selector.(*SessionAffinitySelector).cache
	for _, id := range authIDs {
		snapshot.indexAssigned[id] = m.auths[id].indexAssigned
	}
	m.mu.RUnlock()
	cache.mu.RLock()
	snapshot.sessions = len(cache.entries) + len(cache.groups)
	cache.mu.RUnlock()
	return snapshot
}

func TestPreviewRoutingChangesNoState(t *testing.T) {
	now := time.Now()
	ids := []string{"pv-state-a", "pv-state-b", "pv-state-c"}
	recordTestWindows("pv-state-a", now.Add(time.Hour), now.Add(time.Hour))
	m, closestToReset := newPreviewTestManager(t, claudeAuths(ids...))

	// Bind a session and advance state through real picks, then leave an
	// expired pin that pruning would delete and a never-picked auth whose
	// index is unassigned.
	sessionOpts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"pv-state-session"}}}
	if _, _, _, err := m.pickNextMixed(context.Background(), []string{"claude"}, previewTestModel, sessionOpts, map[string]struct{}{}); err != nil {
		t.Fatalf("pickNextMixed() error = %v", err)
	}
	m.burnPinMu.Lock()
	m.burnPins = map[string]BurnPin{"claude": {AuthID: "pv-state-a", Provider: "claude", ExpiresAt: now.Add(-time.Minute)}}
	m.burnPinMu.Unlock()
	m.mutateTestAuth("pv-state-c", func(a *Auth) { a.Index, a.indexAssigned = "", false })

	before := snapshotRoutingState(m, closestToReset, ids)
	if before.sessions == 0 {
		t.Fatal("fixture bound no session")
	}
	preview, err := m.PreviewRouting("claude", previewTestModel, now)
	if err != nil {
		t.Fatalf("PreviewRouting() error = %v", err)
	}
	if preview.Reason != routingReasonProbe {
		t.Fatalf("preview reason = %q, want probe so the probe path is exercised", preview.Reason)
	}
	if after := snapshotRoutingState(m, closestToReset, ids); !reflect.DeepEqual(before, after) {
		t.Fatalf("routing state changed\nbefore %+v\nafter  %+v", before, after)
	}

	// The no-data fallback path reads the rotation cursor; it must not move it.
	rrIDs := []string{"pv-state-rr-a", "pv-state-rr-b"}
	rr, rrSelector := newPreviewTestManager(t, claudeAuths(rrIDs...))
	if _, _, _, err := rr.pickNextMixed(context.Background(), []string{"claude"}, previewTestModel, cliproxyexecutor.Options{}, map[string]struct{}{}); err != nil {
		t.Fatalf("pickNextMixed() error = %v", err)
	}
	before = snapshotRoutingState(rr, rrSelector, rrIDs)
	if preview, err = rr.PreviewRouting("claude", previewTestModel, now); err != nil || preview.Reason != routingReasonRoundRobinFallback {
		t.Fatalf("PreviewRouting() = %q, %v; want round-robin-fallback", preview.Reason, err)
	}
	if after := snapshotRoutingState(rr, rrSelector, rrIDs); !reflect.DeepEqual(before, after) {
		t.Fatalf("rotation state changed\nbefore %+v\nafter  %+v", before, after)
	}
}
