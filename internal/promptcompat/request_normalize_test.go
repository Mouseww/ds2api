package promptcompat

import (
	"strings"
	"testing"
)

type toolChoiceTestConfig struct {
	aliases map[string]string
}

func (c toolChoiceTestConfig) ModelAliases() map[string]string { return c.aliases }

func toolChoiceTestTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "get_weather",
				"description": "Get current weather",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"city": map[string]any{"type": "string"}},
				},
			},
		},
	}
}

// TestNormalizeOpenAIChatRequestParsesToolChoice guards the Chat surface
// honoring the request's tool_choice the same way Responses does. Chat used to
// hardcode the default auto policy, so "required" / forced-function requests
// were silently treated as auto.
func TestNormalizeOpenAIChatRequestParsesToolChoice(t *testing.T) {
	cfg := toolChoiceTestConfig{}

	t.Run("required", func(t *testing.T) {
		req := map[string]any{
			"model":       "deepseek-v4-flash",
			"messages":    []any{map[string]any{"role": "user", "content": "hello"}},
			"tools":       toolChoiceTestTools(),
			"tool_choice": "required",
		}
		out, err := NormalizeOpenAIChatRequest(cfg, req, "")
		if err != nil {
			t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
		}
		if out.ToolChoice.Mode != ToolChoiceRequired {
			t.Fatalf("expected tool_choice mode=required, got %q", out.ToolChoice.Mode)
		}
		if !strings.Contains(out.FinalPrompt, "you MUST call at least one tool") {
			t.Fatalf("expected required-mode instruction in prompt, got: %s", out.FinalPrompt)
		}
	})

	t.Run("forced function", func(t *testing.T) {
		req := map[string]any{
			"model":    "deepseek-v4-flash",
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
			"tools":    toolChoiceTestTools(),
			"tool_choice": map[string]any{
				"type":     "function",
				"function": map[string]any{"name": "get_weather"},
			},
		}
		out, err := NormalizeOpenAIChatRequest(cfg, req, "")
		if err != nil {
			t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
		}
		if out.ToolChoice.Mode != ToolChoiceForced {
			t.Fatalf("expected tool_choice mode=forced, got %q", out.ToolChoice.Mode)
		}
		if out.ToolChoice.ForcedName != "get_weather" {
			t.Fatalf("expected forced name get_weather, got %q", out.ToolChoice.ForcedName)
		}
		if !strings.Contains(out.FinalPrompt, "you MUST call exactly this tool name: get_weather") {
			t.Fatalf("expected forced-mode instruction in prompt, got: %s", out.FinalPrompt)
		}
	})

	t.Run("none", func(t *testing.T) {
		req := map[string]any{
			"model":       "deepseek-v4-flash",
			"messages":    []any{map[string]any{"role": "user", "content": "hello"}},
			"tools":       toolChoiceTestTools(),
			"tool_choice": "none",
		}
		out, err := NormalizeOpenAIChatRequest(cfg, req, "")
		if err != nil {
			t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
		}
		if out.ToolChoice.Mode != ToolChoiceNone {
			t.Fatalf("expected tool_choice mode=none, got %q", out.ToolChoice.Mode)
		}
	})

	t.Run("required without tools is rejected", func(t *testing.T) {
		req := map[string]any{
			"model":       "deepseek-v4-flash",
			"messages":    []any{map[string]any{"role": "user", "content": "hello"}},
			"tool_choice": "required",
		}
		if _, err := NormalizeOpenAIChatRequest(cfg, req, ""); err == nil {
			t.Fatal("expected tool_choice=required without tools to be rejected")
		}
	})

	t.Run("default stays auto", func(t *testing.T) {
		req := map[string]any{
			"model":    "deepseek-v4-flash",
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		}
		out, err := NormalizeOpenAIChatRequest(cfg, req, "")
		if err != nil {
			t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
		}
		if out.ToolChoice.Mode != ToolChoiceAuto {
			t.Fatalf("expected default tool_choice mode=auto, got %q", out.ToolChoice.Mode)
		}
	})
}
