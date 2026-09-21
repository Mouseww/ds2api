package claude

import (
	"strings"
	"testing"

	"ds2api/internal/promptcompat"
)

type mockClaudeConfig struct {
	aliases           map[string]string
	thinkingInjection *bool
	thinkingPrompt    string
}

func (m mockClaudeConfig) ModelAliases() map[string]string { return m.aliases }
func (mockClaudeConfig) CurrentInputFileEnabled() bool     { return true }
func (mockClaudeConfig) CurrentInputFileMinChars() int     { return 0 }
func (mockClaudeConfig) AutoDeleteMode() string            { return "none" }
func (m mockClaudeConfig) ThinkingInjectionEnabled() bool {
	if m.thinkingInjection == nil {
		return false
	}
	return *m.thinkingInjection
}
func (m mockClaudeConfig) ThinkingInjectionPrompt() string { return m.thinkingPrompt }

// TestNormalizeClaudeRequestAppliesThinkingInjection guards the Claude surface
// honoring the thinking-injection setting: the configured reasoning-effort
// prompt must land in the latest user message of both the live prompt and the
// StandardRequest messages (which feed the current-input file). Claude used to
// skip the injection entirely — only the OpenAI surfaces applied it.
func TestNormalizeClaudeRequestAppliesThinkingInjection(t *testing.T) {
	enabled := true
	req := map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	out, err := normalizeClaudeRequest(mockClaudeConfig{
		aliases:           map[string]string{"claude-sonnet-4-6": "deepseek-v4-pro"},
		thinkingInjection: &enabled,
		thinkingPrompt:    "REASONING-EFFORT-MARKER-XYZ",
	}, req)
	if err != nil {
		t.Fatalf("normalizeClaudeRequest error: %v", err)
	}
	if !strings.Contains(out.Standard.FinalPrompt, "REASONING-EFFORT-MARKER-XYZ") {
		t.Fatalf("expected injection prompt in final prompt, got %q", out.Standard.FinalPrompt)
	}
	if !strings.Contains(out.Standard.PromptTokenText, "REASONING-EFFORT-MARKER-XYZ") {
		t.Fatalf("expected injection prompt in prompt token text")
	}
}

// TestNormalizeClaudeRequestSkipsThinkingInjectionWhenDisabled guards the
// default-off behavior: no injection text may appear without the setting.
func TestNormalizeClaudeRequestSkipsThinkingInjectionWhenDisabled(t *testing.T) {
	req := map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	out, err := normalizeClaudeRequest(mockClaudeConfig{
		aliases: map[string]string{"claude-sonnet-4-6": "deepseek-v4-pro"},
	}, req)
	if err != nil {
		t.Fatalf("normalizeClaudeRequest error: %v", err)
	}
	if strings.Contains(out.Standard.FinalPrompt, promptcompat.ThinkingInjectionMarker) {
		t.Fatalf("expected no injection when disabled, got %q", out.Standard.FinalPrompt)
	}
}

func TestNormalizeClaudeRequestUsesGlobalAliasMapping(t *testing.T) {
	req := map[string]any{
		"model": "claude-opus-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	out, err := normalizeClaudeRequest(mockClaudeConfig{
		aliases: map[string]string{
			"claude-opus-4-6": "deepseek-v4-pro-search",
		},
	}, req)
	if err != nil {
		t.Fatalf("normalizeClaudeRequest error: %v", err)
	}
	if out.Standard.ResolvedModel != "deepseek-v4-pro-search" {
		t.Fatalf("resolved model mismatch: got=%q", out.Standard.ResolvedModel)
	}
	if !out.Standard.Thinking || !out.Standard.Search {
		t.Fatalf("unexpected flags: thinking=%v search=%v", out.Standard.Thinking, out.Standard.Search)
	}
}

func TestNormalizeClaudeRequestDisablesThinkingWhenRequested(t *testing.T) {
	req := map[string]any{
		"model": "claude-opus-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"thinking": map[string]any{"type": "disabled"},
	}
	out, err := normalizeClaudeRequest(mockClaudeConfig{
		aliases: map[string]string{
			"claude-opus-4-6": "deepseek-v4-pro",
		},
	}, req)
	if err != nil {
		t.Fatalf("normalizeClaudeRequest error: %v", err)
	}
	if out.Standard.Thinking {
		t.Fatalf("expected explicit Claude thinking disable to win")
	}
}

func TestNormalizeClaudeRequestEnablesThinkingWhenRequested(t *testing.T) {
	req := map[string]any{
		"model": "claude-opus-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024},
	}
	out, err := normalizeClaudeRequest(mockClaudeConfig{
		aliases: map[string]string{
			"claude-opus-4-6": "deepseek-v4-pro",
		},
	}, req)
	if err != nil {
		t.Fatalf("normalizeClaudeRequest error: %v", err)
	}
	if !out.Standard.Thinking {
		t.Fatalf("expected explicit Claude thinking request to enable downstream thinking")
	}
}

func TestNormalizeClaudeRequestNoThinkingAliasForcesThinkingOff(t *testing.T) {
	req := map[string]any{
		"model": "claude-opus-4-6-nothinking",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024},
	}
	out, err := normalizeClaudeRequest(mockClaudeConfig{}, req)
	if err != nil {
		t.Fatalf("normalizeClaudeRequest error: %v", err)
	}
	if out.Standard.ResolvedModel != "deepseek-v4-pro-nothinking" {
		t.Fatalf("resolved model mismatch: got=%q", out.Standard.ResolvedModel)
	}
	if out.Standard.Thinking {
		t.Fatalf("expected nothinking alias to force downstream thinking off")
	}
}

func TestNormalizeClaudeRequestPrefersGlobalAliasMapping(t *testing.T) {
	req := map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	out, err := normalizeClaudeRequest(mockClaudeConfig{
		aliases: map[string]string{
			"claude-sonnet-4-6": "deepseek-v4-flash",
		},
	}, req)
	if err != nil {
		t.Fatalf("normalizeClaudeRequest error: %v", err)
	}
	if out.Standard.ResolvedModel != "deepseek-v4-flash" {
		t.Fatalf("expected global alias to win for explicit model, got=%q", out.Standard.ResolvedModel)
	}
}
