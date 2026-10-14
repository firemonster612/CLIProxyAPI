package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagehistory"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func getRoutingPreview(t *testing.T, handler *Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/routing/preview"+query, nil)
	handler.GetRoutingPreview(ginCtx)
	return rec
}

func newRoutingPreviewTestHandler(t *testing.T) *Handler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"preview-handler-a", "preview-handler-b"} {
		if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, Provider: "claude"}); err != nil {
			t.Fatalf("Register(%s) error = %v", id, err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	registry.GetGlobalRegistry().RegisterClient("preview-handler-a", "claude", []*registry.ModelInfo{{ID: "preview-common"}, {ID: "preview-only-a"}})
	registry.GetGlobalRegistry().RegisterClient("preview-handler-b", "claude", []*registry.ModelInfo{{ID: "preview-common"}})
	return &Handler{authManager: manager}
}

func TestGetRoutingPreviewRejectsMissingOrUnknownProvider(t *testing.T) {
	withRecentRequestHistory(t, true)
	handler := newRoutingPreviewTestHandler(t)
	for _, query := range []string{"", "?model=preview-common", "?provider=codex&model=preview-common", "?provider=codex"} {
		if rec := getRoutingPreview(t, handler, query); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: status = %d body = %s, want 400", query, rec.Code, rec.Body.String())
		}
	}
}

func TestGetRoutingPreviewReportsHomeDispatchWithoutLocalCredentials(t *testing.T) {
	withRecentRequestHistory(t, true)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	handler := &Handler{authManager: manager}
	for _, query := range []string{"?provider=claude", "?provider=claude&model=preview-common"} {
		rec := getRoutingPreview(t, handler, query)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d body = %s, want 200", query, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%q: decode: %v", query, err)
		}
		if body["supported"] != false || body["unsupported_reason"] != "home_dispatch" || body["chosen_auth_index"] != nil {
			t.Fatalf("%q: body = %v, want unsupported home_dispatch with a null choice", query, body)
		}
	}
}

func TestGetRoutingPreviewChoosesModelFromQueryRecentThenRegistry(t *testing.T) {
	handler := newRoutingPreviewTestHandler(t)
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body
	}

	withRecentRequestHistory(t, true)
	body := decode(getRoutingPreview(t, handler, "?provider=claude"))
	if body["model"] != "preview-common" || body["model_source"] != "registry" {
		t.Fatalf("registry fallback = %v/%v, want preview-common/registry", body["model"], body["model_source"])
	}
	// The default round-robin selector runs on the scheduler, which the preview does not reproduce.
	if body["scope"] != "new-request-without-session" || body["supported"] != false || body["unsupported_reason"] != "unsupported_strategy" || body["chosen_auth_index"] != nil {
		t.Fatalf("body = %v, want unsupported_strategy with a null choice", body)
	}

	withRecentRequestHistory(t, true, usagehistory.Entry{Provider: "claude", Model: "upstream-name", Alias: "preview-only-a"})
	body = decode(getRoutingPreview(t, handler, "?provider=claude"))
	if body["model"] != "preview-only-a" || body["model_source"] != "recent" {
		t.Fatalf("recent model = %v/%v, want preview-only-a/recent", body["model"], body["model_source"])
	}

	body = decode(getRoutingPreview(t, handler, "?provider=claude&model=preview-common"))
	if body["model"] != "preview-common" || body["model_source"] != "query" {
		t.Fatalf("query model = %v/%v, want preview-common/query", body["model"], body["model_source"])
	}
}
