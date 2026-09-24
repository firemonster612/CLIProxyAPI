package registry

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

// Models an upstream reports for a credential but the catalog does not list
// yet, keyed by auth ID. They let a new model work on release day, before
// models.json is updated; catalog entries always take precedence. Each
// credential only gains the models its own upstream listing reported.
var (
	discoveredModelsMu sync.RWMutex
	discoveredModels   = make(map[string][]*ModelInfo)
)

// setDiscoveredModels records the models listed for one credential and reports
// whether the set changed.
func setDiscoveredModels(authID string, models []*ModelInfo) (bool, error) {
	if len(models) == 0 {
		return false, fmt.Errorf("upstream model list has no usable models")
	}
	discoveredModelsMu.Lock()
	defer discoveredModelsMu.Unlock()
	if reflect.DeepEqual(discoveredModels[authID], models) {
		return false, nil
	}
	discoveredModels[authID] = models
	return true, nil
}

// WithDiscoveredModels appends the models discovered for authID that the
// catalog list lacks.
func WithDiscoveredModels(authID string, catalog []*ModelInfo) []*ModelInfo {
	discoveredModelsMu.RLock()
	defer discoveredModelsMu.RUnlock()
	discovered := discoveredModels[authID]
	if len(discovered) == 0 {
		return catalog
	}
	known := make(map[string]struct{}, len(catalog))
	for _, model := range catalog {
		if model != nil {
			known[model.ID] = struct{}{}
		}
	}
	for _, model := range discovered {
		if _, ok := known[model.ID]; !ok {
			catalog = append(catalog, cloneModelInfo(model))
		}
	}
	return catalog
}

type anthropicCapability struct {
	Supported bool `json:"supported"`
}

type anthropicModel struct {
	ID             string    `json:"id"`
	DisplayName    string    `json:"display_name"`
	CreatedAt      time.Time `json:"created_at"`
	MaxInputTokens int       `json:"max_input_tokens"`
	MaxTokens      int       `json:"max_tokens"`
	Capabilities   struct {
		ImageInput anthropicCapability `json:"image_input"`
		Thinking   struct {
			Supported bool `json:"supported"`
			Types     struct {
				Adaptive anthropicCapability `json:"adaptive"`
			} `json:"types"`
		} `json:"thinking"`
		Effort struct {
			Supported bool                `json:"supported"`
			Low       anthropicCapability `json:"low"`
			Medium    anthropicCapability `json:"medium"`
			High      anthropicCapability `json:"high"`
			XHigh     anthropicCapability `json:"xhigh"`
			Max       anthropicCapability `json:"max"`
		} `json:"effort"`
	} `json:"capabilities"`
}

// AnthropicModelPage is one page of an Anthropic /v1/models response.
type AnthropicModelPage struct {
	Data    []anthropicModel `json:"data"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}

// ParseAnthropicModelPage decodes one /v1/models response page.
func ParseAnthropicModelPage(body []byte) (AnthropicModelPage, error) {
	var page AnthropicModelPage
	if errUnmarshal := json.Unmarshal(body, &page); errUnmarshal != nil {
		return AnthropicModelPage{}, fmt.Errorf("decode anthropic model list: %w", errUnmarshal)
	}
	return page, nil
}

// SetDiscoveredClaudeModels records the Claude models Anthropic lists for one
// credential and reports whether the set changed.
func SetDiscoveredClaudeModels(authID string, pages []AnthropicModelPage) (bool, error) {
	var models []*ModelInfo
	for _, page := range pages {
		for i := range page.Data {
			if info := claudeModelInfoFromAnthropic(&page.Data[i]); info != nil {
				models = append(models, info)
			}
		}
	}
	return setDiscoveredModels(authID, models)
}

func claudeModelInfoFromAnthropic(model *anthropicModel) *ModelInfo {
	id := strings.TrimSpace(model.ID)
	if !strings.HasPrefix(id, "claude-") {
		return nil
	}
	info := &ModelInfo{
		ID:                        id,
		Object:                    "model",
		OwnedBy:                   "anthropic",
		Type:                      "claude",
		DisplayName:               model.DisplayName,
		ContextLength:             model.MaxInputTokens,
		MaxCompletionTokens:       model.MaxTokens,
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	}
	if !model.CreatedAt.IsZero() {
		info.Created = model.CreatedAt.Unix()
	}
	if model.Capabilities.ImageInput.Supported {
		info.SupportedInputModalities = append(info.SupportedInputModalities, "image")
	}
	if thinking := model.Capabilities.Thinking; thinking.Supported {
		info.Thinking = &ThinkingSupport{ZeroAllowed: true}
		if thinking.Types.Adaptive.Supported {
			info.Thinking.DynamicAllowed = true
		} else {
			info.Thinking.Min = 1024
			info.Thinking.Max = model.MaxTokens
		}
		if effort := model.Capabilities.Effort; effort.Supported {
			for _, level := range []struct {
				name string
				cap  anthropicCapability
			}{{"low", effort.Low}, {"medium", effort.Medium}, {"high", effort.High}, {"xhigh", effort.XHigh}, {"max", effort.Max}} {
				if level.cap.Supported {
					info.Thinking.Levels = append(info.Thinking.Levels, level.name)
				}
			}
		}
	}
	return info
}

// codexModel is the subset of a ChatGPT Codex /models entry this proxy needs.
type codexModel struct {
	Slug                     string   `json:"slug"`
	DisplayName              string   `json:"display_name"`
	Description              string   `json:"description"`
	Visibility               string   `json:"visibility"`
	SupportedInAPI           bool     `json:"supported_in_api"`
	MaxContextWindow         int      `json:"max_context_window"`
	InputModalities          []string `json:"input_modalities"`
	SupportsSearchTool       bool     `json:"supports_search_tool"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

// SetDiscoveredCodexModels records the models the ChatGPT Codex /models
// endpoint lists for one credential and reports whether the set changed.
// Hidden internal models (visibility "hide") are skipped, matching what the
// Codex client itself offers.
func SetDiscoveredCodexModels(authID string, body []byte) (bool, error) {
	var listing struct {
		Models []codexModel `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(body, &listing); errUnmarshal != nil {
		return false, fmt.Errorf("decode codex model list: %w", errUnmarshal)
	}
	var models []*ModelInfo
	for i := range listing.Models {
		if info := codexModelInfo(&listing.Models[i]); info != nil {
			models = append(models, info)
		}
	}
	return setDiscoveredModels(authID, models)
}

func codexModelInfo(model *codexModel) *ModelInfo {
	id := strings.TrimSpace(model.Slug)
	if id == "" || model.Visibility != "list" || !model.SupportedInAPI {
		return nil
	}
	info := &ModelInfo{
		ID:                        id,
		Object:                    "model",
		OwnedBy:                   "openai",
		Type:                      "openai",
		DisplayName:               model.DisplayName,
		Description:               model.Description,
		ContextLength:             model.MaxContextWindow,
		SupportedParameters:       []string{"tools"},
		SupportedInputModalities:  append([]string(nil), model.InputModalities...),
		SupportedOutputModalities: []string{"text"},
	}
	if len(info.SupportedInputModalities) == 0 {
		info.SupportedInputModalities = []string{"text"}
	}
	for _, level := range model.SupportedReasoningLevels {
		// Levels the thinking pipeline does not know (for example "ultra")
		// would be rejected by validation, so only standard ones are listed.
		switch level.Effort {
		case "minimal", "low", "medium", "high", "xhigh", "max":
			if info.Thinking == nil {
				info.Thinking = &ThinkingSupport{}
			}
			info.Thinking.Levels = append(info.Thinking.Levels, level.Effort)
		}
	}
	if model.SupportsSearchTool {
		webSearch := true
		info.NativeCapabilities = &NativeCapabilities{WebSearch: &webSearch}
	}
	return info
}
