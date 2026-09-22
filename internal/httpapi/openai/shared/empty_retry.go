package shared

import "strings"

const EmptyOutputRetrySuffix = "Previous reply had no visible output. Please regenerate the visible final answer or tool call now."

// MalformedToolCallRetrySuffix teaches the exact DSML tool-call format after
// the model emitted a corrupted tool call (fullwidth ｜ pipes, doubled
// parameter names, missing wrapper tags). The retry gives the model one more
// chance to emit the call properly so the caller's agent loop receives a
// structured tool call instead of a text-only response that ends its turn.
const MalformedToolCallRetrySuffix = "Your previous reply contained a malformed tool call: the tool-call markup was corrupted (for example fullwidth ｜ characters instead of halfwidth |, doubled parameter names, or missing wrapper tags), so it could not be executed. Re-emit the tool call using exactly this format, with halfwidth | characters:\n<|DSML|tool_calls>\n<|DSML|invoke name=\"tool_name\">\n<|DSML|parameter name=\"parameter_name\"><![CDATA[value]]></|DSML|parameter>\n</|DSML|invoke>\n</|DSML|tool_calls>\nIf you did not intend to call a tool, reply with the final answer text only."

func EmptyOutputRetryEnabled() bool {
	return true
}

func EmptyOutputRetryMaxAttempts() int {
	return 1
}

func ClonePayloadWithEmptyOutputRetryPrompt(payload map[string]any) map[string]any {
	return ClonePayloadForEmptyOutputRetry(payload, 0)
}

// ClonePayloadForEmptyOutputRetry creates a retry payload with the suffix
// appended and, if parentMessageID > 0, sets parent_message_id so the
// retry is submitted as a proper follow-up turn in the same DeepSeek
// session rather than a disconnected root message.
func ClonePayloadForEmptyOutputRetry(payload map[string]any, parentMessageID int) map[string]any {
	clone := make(map[string]any, len(payload))
	for k, v := range payload {
		clone[k] = v
	}
	original, _ := payload["prompt"].(string)
	clone["prompt"] = AppendEmptyOutputRetrySuffix(original)
	if parentMessageID > 0 {
		clone["parent_message_id"] = parentMessageID
	}
	return clone
}

func AppendEmptyOutputRetrySuffix(prompt string) string {
	prompt = strings.TrimRight(prompt, "\r\n\t ")
	if prompt == "" {
		return EmptyOutputRetrySuffix
	}
	return prompt + "\n\n" + EmptyOutputRetrySuffix
}

func UsagePromptWithEmptyOutputRetry(originalPrompt string, retryAttempts int) string {
	return usagePromptWithRetrySuffix(originalPrompt, retryAttempts, AppendEmptyOutputRetrySuffix)
}

// ClonePayloadForMalformedToolCallRetry is the malformed-tool-call variant of
// ClonePayloadForEmptyOutputRetry: same parent-message threading, corrective
// suffix.
func ClonePayloadForMalformedToolCallRetry(payload map[string]any, parentMessageID int) map[string]any {
	clone := make(map[string]any, len(payload))
	for k, v := range payload {
		clone[k] = v
	}
	original, _ := payload["prompt"].(string)
	clone["prompt"] = appendRetrySuffix(original, MalformedToolCallRetrySuffix)
	if parentMessageID > 0 {
		clone["parent_message_id"] = parentMessageID
	}
	return clone
}

// UsagePromptWithMalformedToolCallRetry is the malformed-tool-call variant of
// UsagePromptWithEmptyOutputRetry.
func UsagePromptWithMalformedToolCallRetry(originalPrompt string, retryAttempts int) string {
	return usagePromptWithRetrySuffix(originalPrompt, retryAttempts, func(prompt string) string {
		return appendRetrySuffix(prompt, MalformedToolCallRetrySuffix)
	})
}

func appendRetrySuffix(prompt, suffix string) string {
	prompt = strings.TrimRight(prompt, "\r\n\t ")
	if prompt == "" {
		return suffix
	}
	return prompt + "\n\n" + suffix
}

func usagePromptWithRetrySuffix(originalPrompt string, retryAttempts int, appendSuffix func(string) string) string {
	if retryAttempts <= 0 {
		return originalPrompt
	}
	parts := make([]string, 0, retryAttempts+1)
	parts = append(parts, originalPrompt)
	next := originalPrompt
	for i := 0; i < retryAttempts; i++ {
		next = appendSuffix(next)
		parts = append(parts, next)
	}
	return strings.Join(parts, "\n")
}
