package claude

import (
	"fmt"
	"strings"

	"ds2api/internal/config"
	"ds2api/internal/prompt"
	"ds2api/internal/promptcompat"
	"ds2api/internal/util"
)

type claudeNormalizedRequest struct {
	Standard           promptcompat.StandardRequest
	NormalizedMessages []any
}

// thinkingInjectionReader is the optional capability a ConfigReader may
// implement to expose the thinking-injection preference. It is kept separate
// from ConfigReader so store stubs that only implement the base interface
// keep compiling (same pattern as promptcompat's stripMaxTokensReader).
type thinkingInjectionReader interface {
	ThinkingInjectionEnabled() bool
	ThinkingInjectionPrompt() string
}

func normalizeClaudeRequest(store ConfigReader, req map[string]any) (claudeNormalizedRequest, error) {
	model, _ := req["model"].(string)
	messagesRaw, _ := req["messages"].([]any)
	if strings.TrimSpace(model) == "" || len(messagesRaw) == 0 {
		return claudeNormalizedRequest{}, fmt.Errorf("request must include 'model' and 'messages'")
	}
	if _, ok := req["max_tokens"]; !ok {
		req["max_tokens"] = 8192
	}
	normalizedMessages := normalizeClaudeMessages(messagesRaw)
	payload := cloneMap(req)
	payload["messages"] = normalizedMessages
	toolsRequested, _ := req["tools"].([]any)
	payload["messages"] = injectClaudeToolPrompt(payload, normalizedMessages, toolsRequested)

	dsPayload := convertClaudeToDeepSeek(payload, store)
	dsModel, _ := dsPayload["model"].(string)
	defaultThinkingEnabled, searchEnabled, ok := config.GetModelConfig(dsModel)
	if !ok {
		searchEnabled = false
	}
	thinkingEnabled := util.ResolveThinkingEnabled(req, defaultThinkingEnabled)
	if config.IsNoThinkingModel(dsModel) {
		thinkingEnabled = false
	}
	// Thinking injection (opt-in): append the configured reasoning-effort
	// prompt to the latest user message before the DeepSeek prompt is
	// assembled, so the injected text lands in both the live prompt and the
	// StandardRequest messages (which feed the current-input file). This is
	// the same shared injection the OpenAI surfaces apply.
	if tr, ok := store.(thinkingInjectionReader); ok && tr.ThinkingInjectionEnabled() && thinkingEnabled {
		if current, ok := payload["messages"].([]any); ok {
			if msgs, changed := promptcompat.AppendThinkingInjectionPromptToLatestUser(current, tr.ThinkingInjectionPrompt()); changed {
				payload["messages"] = msgs
				normalizedMessages = msgs
				// convertClaudeToDeepSeek is pure: rebuild it so the system
				// prepend and the injected messages are assembled together.
				dsPayload = convertClaudeToDeepSeek(payload, store)
			}
		}
	}
	finalPrompt := prompt.MessagesPrepareWithThinking(toMessageMaps(dsPayload["messages"]), thinkingEnabled)
	toolNames := extractClaudeToolNames(toolsRequested)
	if len(toolNames) == 0 && len(toolsRequested) > 0 {
		toolNames = []string{"__any_tool__"}
	}

	return claudeNormalizedRequest{
		Standard: promptcompat.StandardRequest{
			Surface:         "anthropic_messages",
			RequestedModel:  strings.TrimSpace(model),
			ResolvedModel:   dsModel,
			ResponseModel:   strings.TrimSpace(model),
			Messages:        normalizedMessages,
			PromptTokenText: finalPrompt,
			ToolsRaw:        toolsRequested,
			FinalPrompt:     finalPrompt,
			ToolNames:       toolNames,
			Stream:          util.ToBool(req["stream"]),
			Thinking:        thinkingEnabled,
			Search:          searchEnabled,
		},
		NormalizedMessages: normalizedMessages,
	}, nil
}

func injectClaudeToolPrompt(payload map[string]any, normalizedMessages []any, tools []any) []any {
	if len(tools) == 0 {
		return normalizedMessages
	}
	toolPrompt := strings.TrimSpace(buildClaudeToolPrompt(tools))
	if toolPrompt == "" {
		return normalizedMessages
	}

	// Prefer top-level Anthropic-style system prompt when available.
	if systemText, ok := payload["system"].(string); ok && strings.TrimSpace(systemText) != "" {
		payload["system"] = mergeSystemPrompt(systemText, toolPrompt)
		return normalizedMessages
	}

	messages := cloneAnySlice(normalizedMessages)
	for i := range messages {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if !strings.EqualFold(strings.TrimSpace(role), "system") {
			continue
		}
		copied := cloneMap(msg)
		copied["content"] = mergeSystemPrompt(strings.TrimSpace(fmt.Sprintf("%v", copied["content"])), toolPrompt)
		messages[i] = copied
		return messages
	}

	return append([]any{map[string]any{"role": "system", "content": toolPrompt}}, messages...)
}

func mergeSystemPrompt(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n" + extra
	}
}

func cloneAnySlice(in []any) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}
