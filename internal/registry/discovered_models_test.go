package registry

import "testing"

func TestSetDiscoveredClaudeModelsIsScopedToTheReportingCredential(t *testing.T) {
	t.Cleanup(func() {
		discoveredModelsMu.Lock()
		discoveredModels = make(map[string][]*ModelInfo)
		discoveredModelsMu.Unlock()
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

	models := WithDiscoveredModels("auth-a", GetClaudeModels())
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

	for _, model := range WithDiscoveredModels("auth-b", GetClaudeModels()) {
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

func TestSetDiscoveredCodexModelsSkipsHiddenAndUnknownLevels(t *testing.T) {
	t.Cleanup(func() {
		discoveredModelsMu.Lock()
		discoveredModels = make(map[string][]*ModelInfo)
		discoveredModelsMu.Unlock()
	})
	body := []byte(`{"models":[
		{"slug":"gpt-7-nova","display_name":"GPT-7-Nova","visibility":"list","supported_in_api":true,"max_context_window":900000,
		 "input_modalities":["text","image"],"supports_search_tool":true,
		 "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"ultra"}]},
		{"slug":"gpt-reserve","visibility":"hide","supported_in_api":true},
		{"slug":"gpt-5.5","display_name":"Catalog wins","visibility":"list","supported_in_api":true}
	]}`)
	changed, err := SetDiscoveredCodexModels("codex-auth", body)
	if err != nil || !changed {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}

	var nova *ModelInfo
	for _, model := range WithDiscoveredModels("codex-auth", GetCodexProModels()) {
		switch model.ID {
		case "gpt-7-nova":
			nova = model
		case "gpt-reserve":
			t.Error("hidden model was added")
		case "gpt-5.5":
			if model.DisplayName == "Catalog wins" {
				t.Error("discovered entry replaced the catalog definition")
			}
		}
	}
	if nova == nil {
		t.Fatal("discovered model missing")
	}
	if nova.OwnedBy != "openai" || nova.ContextLength != 900000 || len(nova.SupportedInputModalities) != 2 {
		t.Fatalf("discovered model = %+v", nova)
	}
	if nova.Thinking == nil || len(nova.Thinking.Levels) != 2 {
		t.Fatalf("thinking = %+v, want low and high only", nova.Thinking)
	}
	if nova.NativeCapabilities == nil || nova.NativeCapabilities.WebSearch == nil || !*nova.NativeCapabilities.WebSearch {
		t.Fatal("web search capability not mapped")
	}
}
