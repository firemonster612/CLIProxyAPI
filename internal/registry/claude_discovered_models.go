package registry

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

// Claude models that Anthropic's /v1/models reports for a credential but the
// catalog does not list yet, keyed by auth ID. They let a model work on release
// day, before models.json is updated; catalog entries always take precedence.
var (
	discoveredClaudeModelsMu sync.RWMutex
	discoveredClaudeModels   = make(map[string][]*ModelInfo)
)

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

// SetDiscoveredClaudeModels records the models Anthropic lists for one
// credential and reports whether the set changed. The caller re-registers that
// credential's models when it did.
func SetDiscoveredClaudeModels(authID string, pages []AnthropicModelPage) (bool, error) {
	models := make([]*ModelInfo, 0)
	for _, page := range pages {
		for i := range page.Data {
			if info := claudeModelInfoFromAnthropic(&page.Data[i]); info != nil {
				models = append(models, info)
			}
		}
	}
	if len(models) == 0 {
		return false, fmt.Errorf("anthropic model list has no Claude models")
	}
	discoveredClaudeModelsMu.Lock()
	defer discoveredClaudeModelsMu.Unlock()
	if reflect.DeepEqual(discoveredClaudeModels[authID], models) {
		return false, nil
	}
	discoveredClaudeModels[authID] = models
	return true, nil
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

// WithDiscoveredClaudeModels appends the models discovered for authID that the
// catalog list lacks.
func WithDiscoveredClaudeModels(authID string, catalog []*ModelInfo) []*ModelInfo {
	discoveredClaudeModelsMu.RLock()
	defer discoveredClaudeModelsMu.RUnlock()
	discovered := discoveredClaudeModels[authID]
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
