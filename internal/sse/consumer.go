package sse

import (
	"io"
	"net/http"
	"strings"

	dsprotocol "ds2api/internal/deepseek/protocol"
	"ds2api/internal/util"
)

// CollectResult holds the aggregated text and thinking content from a
// DeepSeek SSE stream, consumed to completion (non-streaming use case).
type CollectResult struct {
	Text                  string
	Thinking              string
	ToolDetectionThinking string
	ContentFilter         bool
	CitationLinks         map[int]string
	ResponseMessageID     int

	// BodyError is set when the response body was not an SSE stream but a
	// plain-JSON error envelope (see SniffJSONErrorBody); Text/Thinking are
	// empty in that case.
	BodyError *UpstreamBodyError
	// ErrorMessage carries the raw in-stream error payload when the stream
	// stopped on a data:{"error":...} line (previously dropped, which made
	// such streams look like empty output).
	ErrorMessage string
}

// CollectStream fully consumes a DeepSeek SSE response and separates
// thinking content from text content. This replaces the duplicated
// stream-collection logic in openai.handleNonStream, claude.collectDeepSeek,
// and admin.testAccount.
//
// The caller is responsible for closing resp.Body unless closeBody is true.
func CollectStream(resp *http.Response, thinkingEnabled bool, closeBody bool) CollectResult {
	if closeBody {
		defer func() { _ = resp.Body.Close() }()
	}
	// DeepSeek occasionally answers a completion request with HTTP 200 and a
	// plain-JSON error envelope instead of an SSE stream (INVALID_POW_RESPONSE,
	// "user is muted"). Sniff for that first so callers see the real cause
	// instead of an empty stream misreported as "no output". The sniff is
	// gated on the JSON content type so it never reads ahead of a healthy
	// SSE stream.
	body := io.Reader(resp.Body)
	if JSONEnvelopeContentType(resp.Header.Get("Content-Type")) {
		replay, bodyErr := SniffJSONErrorBody(resp.Body)
		if bodyErr != nil {
			return CollectResult{BodyError: bodyErr}
		}
		body = replay
	}
	text := strings.Builder{}
	thinking := strings.Builder{}
	toolDetectionThinking := strings.Builder{}
	contentFilter := false
	stopped := false
	errorMessage := ""
	collector := newCitationLinkCollector()
	responseMessageID := 0
	currentType := "text"
	if thinkingEnabled {
		currentType = "thinking"
	}
	_ = dsprotocol.ScanSSELinesReader(body, func(line []byte) bool {
		chunk, done, parsed := ParseDeepSeekSSELine(line)
		if parsed && !done {
			collector.ingestChunk(chunk)
			observeResponseMessageID(chunk, &responseMessageID)
		}
		if done {
			return false
		}
		if stopped {
			return true
		}
		result := ParseDeepSeekContentLine(line, thinkingEnabled, currentType)
		currentType = result.NextType
		if !result.Parsed {
			return true
		}
		if result.Stop {
			if result.ContentFilter {
				contentFilter = true
			}
			if result.ErrorMessage != "" && errorMessage == "" {
				errorMessage = result.ErrorMessage
			}
			// Keep scanning to collect late-arriving citation metadata lines
			// that can appear after response/status=FINISHED, but stop as soon
			// as [DONE] arrives.
			stopped = true
			return true
		}
		for _, p := range result.Parts {
			if p.Type == "thinking" {
				trimmed := TrimContinuationOverlap(thinking.String(), p.Text)
				thinking.WriteString(trimmed)
			} else {
				trimmed := TrimContinuationOverlap(text.String(), p.Text)
				text.WriteString(trimmed)
			}
		}
		for _, p := range result.ToolDetectionThinkingParts {
			trimmed := TrimContinuationOverlap(toolDetectionThinking.String(), p.Text)
			toolDetectionThinking.WriteString(trimmed)
		}
		return true
	})
	return CollectResult{
		Text:                  text.String(),
		Thinking:              thinking.String(),
		ToolDetectionThinking: toolDetectionThinking.String(),
		ContentFilter:         contentFilter,
		CitationLinks:         collector.build(),
		ResponseMessageID:     responseMessageID,
		ErrorMessage:          errorMessage,
	}
}

// observeResponseMessageID extracts the response_message_id from a parsed SSE
// chunk. It mirrors the extraction logic in client_continue.go's observe
// method, checking top-level response_message_id, v.response.message_id, and
// message.response.message_id.
func observeResponseMessageID(chunk map[string]any, out *int) {
	if chunk == nil || out == nil {
		return
	}
	if id := util.IntFrom(chunk["response_message_id"]); id > 0 {
		*out = id
	}
	v, _ := chunk["v"].(map[string]any)
	if response, _ := v["response"].(map[string]any); response != nil {
		if id := util.IntFrom(response["message_id"]); id > 0 {
			*out = id
		}
	}
	if message, _ := chunk["message"].(map[string]any); message != nil {
		if response, _ := message["response"].(map[string]any); response != nil {
			if id := util.IntFrom(response["message_id"]); id > 0 {
				*out = id
			}
		}
	}
}
