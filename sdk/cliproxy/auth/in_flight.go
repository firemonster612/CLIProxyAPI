package auth

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// In-flight attempt kinds.
const (
	inFlightKindGenerate    = "generate"
	inFlightKindStream      = "stream"
	inFlightKindCountTokens = "count_tokens"
)

// InFlightAttempt is one executor call that has started and not yet finished.
// A stream attempt lasts until its stream ends, not just until it starts.
type InFlightAttempt struct {
	// AttemptID is unique per executor call; one request can retry the same
	// credential and model, so neither RequestID nor AuthIndex is.
	AttemptID      string
	RequestID      string
	Provider       string
	AuthIndex      string
	Model          string
	RequestedModel string
	Kind           string
	StartedAt      time.Time
}

// inFlightAttempts tracks executor calls independently of usage publishing,
// so active work stays visible when usage statistics are off.
type inFlightAttempts struct {
	mu       sync.Mutex
	lastID   uint64
	attempts map[string]InFlightAttempt
}

// begin records an attempt and returns the idempotent function that removes it.
func (t *inFlightAttempts) begin(attempt InFlightAttempt) (finish func()) {
	t.mu.Lock()
	t.lastID++
	attempt.AttemptID = strconv.FormatUint(t.lastID, 36)
	if t.attempts == nil {
		t.attempts = make(map[string]InFlightAttempt)
	}
	t.attempts[attempt.AttemptID] = attempt
	t.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.attempts, attempt.AttemptID)
			t.mu.Unlock()
		})
	}
}

func (t *inFlightAttempts) snapshot() []InFlightAttempt {
	t.mu.Lock()
	out := make([]InFlightAttempt, 0, len(t.attempts))
	for _, attempt := range t.attempts {
		out = append(out, attempt)
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].AttemptID < out[j].AttemptID
	})
	return out
}

// beginInFlight records an executor call about to dispatch model for auth.
// requestedModel is the client's model, before aliasing and routing.
func (m *Manager) beginInFlight(ctx context.Context, kind, provider string, auth *Auth, model, requestedModel string) (finish func()) {
	return m.inFlight.begin(InFlightAttempt{
		RequestID:      logging.GetRequestID(ctx),
		Provider:       provider,
		AuthIndex:      auth.peekIndex(),
		Model:          model,
		RequestedModel: requestedModel,
		Kind:           kind,
		StartedAt:      time.Now(),
	})
}

// InFlightAttempts returns the executor calls currently running, oldest first.
func (m *Manager) InFlightAttempts() []InFlightAttempt {
	if m == nil {
		return nil
	}
	return m.inFlight.snapshot()
}

// executeTracked runs a non-stream executor call, listing it as in flight while it runs.
func (m *Manager) executeTracked(ctx context.Context, executor ProviderExecutor, auth *Auth, provider, routeModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	defer m.beginInFlight(ctx, inFlightKindGenerate, provider, auth, req.Model, requestedModelAliasFromOptions(opts, routeModel))()
	return executor.Execute(ctx, auth, req, opts)
}

// countTokensTracked runs a token-count executor call, listing it as in flight while it runs.
func (m *Manager) countTokensTracked(ctx context.Context, executor ProviderExecutor, auth *Auth, provider, routeModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	defer m.beginInFlight(ctx, inFlightKindCountTokens, provider, auth, req.Model, requestedModelAliasFromOptions(opts, routeModel))()
	return executor.CountTokens(ctx, auth, req, opts)
}
