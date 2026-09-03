package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type burnPinStubExecutor struct{ id string }

func (e burnPinStubExecutor) Identifier() string { return e.id }

func (e burnPinStubExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e burnPinStubExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e burnPinStubExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e burnPinStubExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e burnPinStubExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newBurnPinTestManager(t *testing.T, auths ...*Auth) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(burnPinStubExecutor{id: "claude"})
	for _, auth := range auths {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}
	return m
}

func TestSetBurnPin_ResolvesProviderAndReplaces(t *testing.T) {
	m := newBurnPinTestManager(t,
		&Auth{ID: "burn-a", Provider: "claude"},
		&Auth{ID: "burn-b", Provider: "claude"},
	)

	pin, err := m.SetBurnPin("burn-a", 30*time.Minute)
	if err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}
	if pin.Provider != "claude" || pin.ExpiresAt.IsZero() {
		t.Fatalf("pin = %+v, want claude provider with expiry", pin)
	}

	// A second pin for the same provider replaces the first.
	if _, err = m.SetBurnPin("burn-b", 0); err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}
	pins := m.BurnPins()
	if len(pins) != 1 || pins[0].AuthID != "burn-b" || !pins[0].ExpiresAt.IsZero() {
		t.Fatalf("pins = %+v, want single indefinite pin on burn-b", pins)
	}

	if _, err = m.SetBurnPin("missing", 0); err == nil {
		t.Fatal("SetBurnPin(missing) expected error")
	}
}

func TestBurnPins_PrunesExpired(t *testing.T) {
	m := newBurnPinTestManager(t, &Auth{ID: "burn-exp", Provider: "claude"})
	if _, err := m.SetBurnPin("burn-exp", time.Millisecond); err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if pins := m.BurnPins(); len(pins) != 0 {
		t.Fatalf("pins = %+v, want expired pin pruned", pins)
	}
}

func TestBurnPinnedPick_RoutesAndFallsBack(t *testing.T) {
	pinned := &Auth{ID: "burn-pin", Provider: "claude"}
	other := &Auth{ID: "burn-other", Provider: "claude"}
	m := newBurnPinTestManager(t, pinned, other)
	if _, err := m.SetBurnPin("burn-pin", 0); err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}

	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	auth, executor, provider := m.burnPinnedPick(ctx, []string{"claude"}, "", opts, nil)
	if auth == nil || auth.ID != "burn-pin" || executor == nil || provider != "claude" {
		t.Fatalf("burnPinnedPick() = %v, %v, %q; want burn-pin via claude", auth, executor, provider)
	}

	// Already tried this request: fall back to the strategy.
	tried := map[string]struct{}{"burn-pin": {}}
	if auth, _, _ = m.burnPinnedPick(ctx, []string{"claude"}, "", opts, tried); auth != nil {
		t.Fatalf("burnPinnedPick() with tried = %v, want nil", auth)
	}

	// Cooling down: fall back, then resume once available again.
	m.mu.Lock()
	m.auths["burn-pin"].Unavailable = true
	m.auths["burn-pin"].NextRetryAfter = time.Now().Add(time.Hour)
	m.mu.Unlock()
	if auth, _, _ = m.burnPinnedPick(ctx, []string{"claude"}, "", opts, nil); auth != nil {
		t.Fatalf("burnPinnedPick() while cooling = %v, want nil", auth)
	}
	m.mu.Lock()
	m.auths["burn-pin"].Unavailable = false
	m.auths["burn-pin"].NextRetryAfter = time.Time{}
	m.mu.Unlock()
	if auth, _, _ = m.burnPinnedPick(ctx, []string{"claude"}, "", opts, nil); auth == nil || auth.ID != "burn-pin" {
		t.Fatalf("burnPinnedPick() after recovery = %v, want burn-pin", auth)
	}

	// Cleared: fall back permanently.
	if !m.ClearBurnPinForAuth("burn-pin") {
		t.Fatal("ClearBurnPinForAuth() = false, want true")
	}
	if auth, _, _ = m.burnPinnedPick(ctx, []string{"claude"}, "", opts, nil); auth != nil {
		t.Fatalf("burnPinnedPick() after clear = %v, want nil", auth)
	}
}

func TestBurnPinnedPick_IgnoresOtherProviders(t *testing.T) {
	m := newBurnPinTestManager(t, &Auth{ID: "burn-scope", Provider: "claude"})
	if _, err := m.SetBurnPin("burn-scope", 0); err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}
	if auth, _, _ := m.burnPinnedPick(context.Background(), []string{"gemini"}, "", cliproxyexecutor.Options{}, nil); auth != nil {
		t.Fatalf("burnPinnedPick(gemini) = %v, want nil (pin scoped to claude)", auth)
	}
}

func TestPickNextMixed_BurnPinOverridesSessionAffinityAndFallsBack(t *testing.T) {
	authA := &Auth{ID: "burn-hook-a", Provider: "claude"}
	authB := &Auth{ID: "burn-hook-b", Provider: "claude"}
	m := newBurnPinTestManager(t, authA, authB)
	m.SetSelector(NewSessionAffinitySelector(&RoundRobinSelector{}))

	ctx := context.Background()
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"burn-hook-session"}}}

	// Establish a sticky session binding first.
	bound, _, _, err := m.pickNextMixed(ctx, []string{"claude"}, "", opts, map[string]struct{}{})
	if err != nil || bound == nil {
		t.Fatalf("pickNextMixed() bootstrap = %v, %v", bound, err)
	}
	pinnedID := "burn-hook-a"
	if bound.ID == pinnedID {
		pinnedID = "burn-hook-b"
	}

	// Pin the credential the session is NOT bound to: the pin must win.
	if _, errPin := m.SetBurnPin(pinnedID, 0); errPin != nil {
		t.Fatalf("SetBurnPin() error = %v", errPin)
	}
	got, _, provider, err := m.pickNextMixed(ctx, []string{"claude"}, "", opts, map[string]struct{}{})
	if err != nil {
		t.Fatalf("pickNextMixed() error = %v", err)
	}
	if got.ID != pinnedID || provider != "claude" {
		t.Fatalf("pickNextMixed() = %q via %q, want pinned %q via claude", got.ID, provider, pinnedID)
	}

	// While the pinned credential cools down the strategy takes over.
	m.mu.Lock()
	m.auths[pinnedID].Unavailable = true
	m.auths[pinnedID].NextRetryAfter = time.Now().Add(time.Hour)
	m.mu.Unlock()
	got, _, _, err = m.pickNextMixed(ctx, []string{"claude"}, "", opts, map[string]struct{}{})
	if err != nil {
		t.Fatalf("pickNextMixed() while cooling error = %v", err)
	}
	if got.ID == pinnedID {
		t.Fatalf("pickNextMixed() while cooling = %q, want fallback to another auth", got.ID)
	}

	// On recovery the pin re-engages.
	m.mu.Lock()
	m.auths[pinnedID].Unavailable = false
	m.auths[pinnedID].NextRetryAfter = time.Time{}
	m.mu.Unlock()
	got, _, _, err = m.pickNextMixed(ctx, []string{"claude"}, "", opts, map[string]struct{}{})
	if err != nil {
		t.Fatalf("pickNextMixed() after recovery error = %v", err)
	}
	if got.ID != pinnedID {
		t.Fatalf("pickNextMixed() after recovery = %q, want pinned %q", got.ID, pinnedID)
	}

	// A pinned auth that already failed this request degrades to the strategy.
	got, _, _, err = m.pickNextMixed(ctx, []string{"claude"}, "", opts, map[string]struct{}{pinnedID: {}})
	if err != nil {
		t.Fatalf("pickNextMixed() with tried error = %v", err)
	}
	if got.ID == pinnedID {
		t.Fatalf("pickNextMixed() with tried = %q, want fallback", got.ID)
	}
}

func TestRemove_ClearsBurnPin(t *testing.T) {
	m := newBurnPinTestManager(t, &Auth{ID: "burn-rm", Provider: "claude"})
	if _, err := m.SetBurnPin("burn-rm", 0); err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}
	m.Remove(context.Background(), "burn-rm")
	if pins := m.BurnPins(); len(pins) != 0 {
		t.Fatalf("pins after Remove = %+v, want none", pins)
	}
}

func TestLoad_ClearsBurnPinForRemovedAuth(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	if _, err := store.Save(ctx, &Auth{ID: "burn-reload", Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatalf("store.Save() error = %v", err)
	}
	m := NewManager(store, nil, nil)
	m.RegisterExecutor(burnPinStubExecutor{id: "claude"})
	if errLoad := m.Load(ctx); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	if _, err := m.SetBurnPin("burn-reload", 0); err != nil {
		t.Fatalf("SetBurnPin() error = %v", err)
	}
	// The credential file vanishes and the watcher reloads: the pin must not
	// survive to capture traffic when the same ID is re-added later.
	if err := store.Delete(ctx, "burn-reload"); err != nil {
		t.Fatalf("store.Delete() error = %v", err)
	}
	if errLoad := m.Load(ctx); errLoad != nil {
		t.Fatalf("Load() after delete error = %v", errLoad)
	}
	if pins := m.BurnPins(); len(pins) != 0 {
		t.Fatalf("pins after reload = %+v, want none", pins)
	}
}
