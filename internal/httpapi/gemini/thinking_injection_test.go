package gemini

import (
	"strings"
	"testing"

	"ds2api/internal/promptcompat"
)

type geminiThinkingInjectionConfig struct {
	enabled bool
	prompt  string
}

func (geminiThinkingInjectionConfig) ModelAliases() map[string]string { return nil }
func (geminiThinkingInjectionConfig) CurrentInputFileEnabled() bool   { return false }
func (geminiThinkingInjectionConfig) CurrentInputFileMinChars() int   { return 0 }
func (geminiThinkingInjectionConfig) AutoDeleteMode() string          { return "none" }
func (c geminiThinkingInjectionConfig) ThinkingInjectionEnabled() bool {
	return c.enabled
}
func (c geminiThinkingInjectionConfig) ThinkingInjectionPrompt() string { return c.prompt }

// TestNormalizeGeminiRequestAppliesThinkingInjection guards the Gemini surface
// honoring the thinking-injection setting: the configured reasoning-effort
// prompt must land in the latest user message before the DeepSeek prompt is
// assembled. Gemini used to skip the injection entirely — only the OpenAI
// surfaces applied it.
func TestNormalizeGeminiRequestAppliesThinkingInjection(t *testing.T) {
	req := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": "hello"}},
			},
		},
	}
	out, err := normalizeGeminiRequest(geminiThinkingInjectionConfig{
		enabled: true,
		prompt:  "REASONING-EFFORT-MARKER-XYZ",
	}, "gemini-2.5-pro", req, false)
	if err != nil {
		t.Fatalf("normalizeGeminiRequest error: %v", err)
	}
	if !strings.Contains(out.FinalPrompt, "REASONING-EFFORT-MARKER-XYZ") {
		t.Fatalf("expected injection prompt in final prompt, got %q", out.FinalPrompt)
	}
	if !strings.Contains(out.PromptTokenText, "REASONING-EFFORT-MARKER-XYZ") {
		t.Fatalf("expected injection prompt in prompt token text")
	}
}

// TestNormalizeGeminiRequestSkipsThinkingInjectionWhenDisabled guards the
// default-off behavior: no injection text may appear without the setting.
func TestNormalizeGeminiRequestSkipsThinkingInjectionWhenDisabled(t *testing.T) {
	req := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": "hello"}},
			},
		},
	}
	out, err := normalizeGeminiRequest(geminiThinkingInjectionConfig{}, "gemini-2.5-pro", req, false)
	if err != nil {
		t.Fatalf("normalizeGeminiRequest error: %v", err)
	}
	if strings.Contains(out.FinalPrompt, promptcompat.ThinkingInjectionMarker) {
		t.Fatalf("expected no injection when disabled, got %q", out.FinalPrompt)
	}
}
