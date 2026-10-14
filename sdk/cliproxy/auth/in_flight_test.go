package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const inFlightTestModel = "in-flight-test-model"

// blockingExecutor parks each call until the test releases it, so the test
// observes the attempt while the executor runs.
type blockingExecutor struct {
	started chan struct{}
	release chan error
	chunks  chan cliproxyexecutor.StreamChunk
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{
		started: make(chan struct{}),
		release: make(chan error),
		chunks:  make(chan cliproxyexecutor.StreamChunk),
	}
}

func (e *blockingExecutor) Identifier() string { return "claude" }

func (e *blockingExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.started <- struct{}{}
	select {
	case err := <-e.release:
		return cliproxyexecutor.Response{Payload: []byte("{}")}, err
	case <-ctx.Done():
		return cliproxyexecutor.Response{}, ctx.Err()
	}
}

func (e *blockingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.started <- struct{}{}
	return &cliproxyexecutor.StreamResult{Chunks: e.chunks}, nil
}

func (e *blockingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (e *blockingExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *blockingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newInFlightTestManager(t *testing.T, executor *blockingExecutor) (*Manager, string) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	auth := &Auth{ID: "in-flight-auth", Provider: "claude"}
	registered, err := m.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: inFlightTestModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return m, registered.EnsureIndex()
}

func inFlightTestContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(logging.WithRequestID(context.Background(), "8a1b2c3d"))
}

var inFlightTestRequest = cliproxyexecutor.Request{Model: inFlightTestModel, Payload: []byte(`{}`)}

func requireOneAttempt(t *testing.T, m *Manager, authIndex, kind string) {
	t.Helper()
	attempts := m.InFlightAttempts()
	if len(attempts) != 1 {
		t.Fatalf("in-flight attempts = %+v, want one", attempts)
	}
	got := attempts[0]
	if got.AttemptID == "" || got.RequestID != "8a1b2c3d" || got.Provider != "claude" || got.AuthIndex != authIndex ||
		got.Model != inFlightTestModel || got.RequestedModel != inFlightTestModel || got.Kind != kind || got.StartedAt.IsZero() {
		t.Fatalf("in-flight attempt = %+v, want request 8a1b2c3d on claude %s %s %s", got, authIndex, inFlightTestModel, kind)
	}
}

func requireNoAttempts(t *testing.T, m *Manager) {
	t.Helper()
	if attempts := m.InFlightAttempts(); len(attempts) != 0 {
		t.Fatalf("in-flight attempts = %+v, want none", attempts)
	}
}

func TestInFlightAttemptsTrackNonStreamCalls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  bool
		finish func(e *blockingExecutor, cancel context.CancelFunc)
	}{
		{name: "success", finish: func(e *blockingExecutor, _ context.CancelFunc) { e.release <- nil }},
		{name: "error", finish: func(e *blockingExecutor, _ context.CancelFunc) {
			e.release <- &Error{Code: "invalid_request", Message: "bad", HTTPStatus: http.StatusBadRequest}
		}},
		{name: "cancel", finish: func(_ *blockingExecutor, cancel context.CancelFunc) { cancel() }},
		{name: "count tokens", count: true, finish: func(e *blockingExecutor, _ context.CancelFunc) { e.release <- nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := newBlockingExecutor()
			m, authIndex := newInFlightTestManager(t, executor)
			ctx, cancel := inFlightTestContext()
			defer cancel()

			done := make(chan struct{})
			go func() {
				defer close(done)
				if tc.count {
					_, _ = m.ExecuteCount(ctx, []string{"claude"}, inFlightTestRequest, cliproxyexecutor.Options{})
					return
				}
				_, _ = m.Execute(ctx, []string{"claude"}, inFlightTestRequest, cliproxyexecutor.Options{})
			}()
			<-executor.started
			kind := inFlightKindGenerate
			if tc.count {
				kind = inFlightKindCountTokens
			}
			requireOneAttempt(t, m, authIndex, kind)

			tc.finish(executor, cancel)
			<-done
			requireNoAttempts(t, m)
		})
	}
}

func TestInFlightAttemptsTrackStreamsUntilTheyEnd(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{name: "stream end"},
		{name: "cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := newBlockingExecutor()
			m, authIndex := newInFlightTestManager(t, executor)
			ctx, cancel := inFlightTestContext()
			defer cancel()

			type started struct {
				result *cliproxyexecutor.StreamResult
				err    error
			}
			startedCh := make(chan started)
			go func() {
				result, err := m.ExecuteStream(ctx, []string{"claude"}, inFlightTestRequest, cliproxyexecutor.Options{})
				startedCh <- started{result, err}
			}()
			<-executor.started
			executor.chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
			start := <-startedCh
			if start.err != nil {
				t.Fatalf("ExecuteStream() error = %v", start.err)
			}
			// Startup returned, but the stream is still open.
			requireOneAttempt(t, m, authIndex, inFlightKindStream)
			if chunk := <-start.result.Chunks; string(chunk.Payload) != "first" {
				t.Fatalf("first chunk = %q", chunk.Payload)
			}
			requireOneAttempt(t, m, authIndex, inFlightKindStream)

			if tc.cancel {
				cancel()
			}
			close(executor.chunks)
			for range start.result.Chunks {
			}
			requireNoAttempts(t, m)
		})
	}
}
