package registry

import "testing"

func TestSetDiscoveredClaudeModelsIsScopedToTheReportingCredential(t *testing.T) {
	t.Cleanup(func() {
		discoveredClaudeModelsMu.Lock()
		discoveredClaudeModels = make(map[string][]*ModelInfo)
		discoveredClaudeModelsMu.Unlock()
	})
	first, err := ParseAnthropicModelPage([]byte(`{"has_more":true,"last_id":"claude-opus-5-5","data":[
		{"id":"claude-opus-5-5","display_name":"Catalog wins","max_input_tokens":1,"max_tokens":1},
		{"id":"not-claude","max_tokens":1}]}`))
	if err != nil || !first.HasMore || first.LastID != "claude-opus-5-5" {
		t.Fatalf("page = %+v, err = %v", first, err)
	}
	second, _ := ParseAnthropicModelPage([]byte(`{"has_more":false,"data":[
		{"id":"claude-future-9","display_name":"Claude Future 9","created_at":"2026-12-01T00:00:00Z","max_input_tokens":2000000,"max_tokens":256000,
		 "capabilities":{"image_input":{"supported":true},"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}},
		 "effort":{"supported":true,"low":{"supported":true},"high":{"supported":true},"max":{"supported":true}}}}]}`))

	changed, err := SetDiscoveredClaudeModels("auth-a", []AnthropicModelPage{first, second})
	if err != nil || !changed {
		t.Fatalf("changed = %v, err = %v; want a change", changed, err)
	}
	if changed, _ = SetDiscoveredClaudeModels("auth-a", []AnthropicModelPage{first, second}); changed {
		t.Fatal("identical listing reported as a change")
	}

	models := WithDiscoveredClaudeModels("auth-a", GetClaudeModels())
	var future *ModelInfo
	opusCount := 0
	for _, model := range models {
		switch model.ID {
		case "claude-future-9":
			future = model
		case "claude-opus-5-5":
			opusCount++
			if model.DisplayName != "Claude Opus 5.5" {
				t.Errorf("claude-opus-5-5 display name = %q, want the catalog definition", model.DisplayName)
			}
		case "not-claude":
			t.Error("non-Claude id was added")
		}
	}
	if opusCount != 1 {
		t.Errorf("claude-opus-5-5 listed %d times, want 1", opusCount)
	}
	if future == nil {
		t.Fatal("model from the second page is missing")
	}
	if future.Type != "claude" || future.ContextLength != 2000000 || future.MaxCompletionTokens != 256000 || len(future.SupportedInputModalities) != 2 {
		t.Fatalf("discovered model = %+v", future)
	}
	if future.Thinking == nil || !future.Thinking.DynamicAllowed || len(future.Thinking.Levels) != 3 {
		t.Fatalf("thinking = %+v, want adaptive with low/high/max", future.Thinking)
	}

	for _, model := range WithDiscoveredClaudeModels("auth-b", GetClaudeModels()) {
		if model.ID == "claude-future-9" {
			t.Fatal("model discovered for auth-a leaked to auth-b")
		}
	}
	if LookupStaticModelInfo("claude-future-9") != nil {
		t.Fatal("discovered model leaked into the static catalog")
	}
}

func TestSetDiscoveredClaudeModelsRejectsListingWithoutClaudeModels(t *testing.T) {
	page, _ := ParseAnthropicModelPage([]byte(`{"data":[{"id":"not-claude"}]}`))
	if _, err := SetDiscoveredClaudeModels("auth-a", []AnthropicModelPage{page}); err == nil {
		t.Fatal("listing without Claude models accepted")
	}
}
