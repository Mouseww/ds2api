package claude

import (
	"ds2api/internal/assistantturn"
	"ds2api/internal/responsehistory"
	"ds2api/internal/sse"
	"ds2api/internal/toolcall"
	"ds2api/internal/toolstream"
	"encoding/json"
	"fmt"
	"time"
)

func (s *claudeStreamRuntime) closeThinkingBlock() {
	if !s.thinkingBlockOpen {
		return
	}
	s.send("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": s.thinkingBlockIndex,
	})
	s.thinkingBlockOpen = false
	s.thinkingBlockIndex = -1
}

func (s *claudeStreamRuntime) closeTextBlock() {
	if !s.textBlockOpen {
		return
	}
	s.send("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": s.textBlockIndex,
	})
	s.textBlockOpen = false
	s.textBlockIndex = -1
}

func (s *claudeStreamRuntime) sendToolUseBlock(idx int, tc toolcall.ParsedToolCall) {
	s.send("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": idx,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    fmt.Sprintf("toolu_%d_%d", time.Now().Unix(), idx),
			"name":  tc.Name,
			"input": map[string]any{},
		},
	})
	inputBytes, _ := json.Marshal(tc.Input)
	s.send("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": idx,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": string(inputBytes),
		},
	})
	s.send("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": idx,
	})
}

func (s *claudeStreamRuntime) finalize(stopReason string, deferEmptyOutput bool) bool {
	if s.ended {
		return true
	}

	if s.bufferToolContent {
		for _, evt := range toolstream.Flush(&s.sieve, s.toolNames) {
			if len(evt.ToolCalls) > 0 {
				s.closeTextBlock()
				s.toolCallsDetected = true
				normalized := toolcall.NormalizeParsedToolCallsForSchemas(evt.ToolCalls, s.toolsRaw)
				for _, tc := range normalized {
					idx := s.nextBlockIndex
					s.nextBlockIndex++
					s.sendToolUseBlock(idx, tc)
				}
				continue
			}
			if evt.Content != "" {
				cleaned := cleanVisibleOutput(evt.Content, s.stripReferenceMarkers)
				if cleaned == "" || (s.searchEnabled && sse.IsCitation(cleaned)) {
					continue
				}
				if !s.textBlockOpen {
					s.textBlockIndex = s.nextBlockIndex
					s.nextBlockIndex++
					s.send("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": s.textBlockIndex,
						"content_block": map[string]any{
							"type": "text",
							"text": "",
						},
					})
					s.textBlockOpen = true
				}
				s.send("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": s.textBlockIndex,
					"delta": map[string]any{
						"type": "text_delta",
						"text": cleaned,
					},
				})
				s.textEmitted = true
			}
		}
	}

	s.closeTextBlock()

	turn := assistantturn.BuildTurnFromStreamSnapshot(assistantturn.StreamSnapshot{
		RawText:               s.rawText.String(),
		VisibleText:           s.text.String(),
		RawThinking:           s.rawThinking.String(),
		VisibleThinking:       s.thinking.String(),
		DetectionThinking:     s.toolDetectionThinking.String(),
		ResponseMessageID:     s.responseMessageID,
		AlreadyEmittedCalls:   s.toolCallsDetected,
		AlreadyEmittedToolRaw: s.toolCallsDetected,
	}, assistantturn.BuildOptions{
		Model:                 s.model,
		Prompt:                s.promptTokenText,
		SearchEnabled:         s.searchEnabled,
		StripReferenceMarkers: s.stripReferenceMarkers,
		ToolNames:             s.toolNames,
		ToolsRaw:              s.toolsRaw,
	})
	finalText := turn.Text
	outcome := assistantturn.FinalizeTurn(turn, assistantturn.FinalizeOptions{
		AlreadyEmittedToolCalls: s.toolCallsDetected,
	})
	if outcome.ShouldFail {
		if deferEmptyOutput {
			s.deferredRetryKind = assistantturn.ClassifyRetryKind(turn)
			return false
		}
		s.ended = true
		s.closeThinkingBlock()
		s.closeTextBlock()
		if s.history != nil {
			s.history.Error(outcome.Error.Status, outcome.Error.Message, outcome.Error.Code, responsehistory.ThinkingForArchive(turn.RawThinking, turn.DetectionThinking, turn.Thinking), responsehistory.TextForArchive(turn.RawText, turn.Text))
		}
		s.sendErrorWithCode(outcome.Error.Status, outcome.Error.Message, outcome.Error.Code)
		return true
	}
	// A malformed tool-call attempt (corrupted DSML markup that parsed into
	// no tool call) defers even when visible text exists: the model intended
	// to call a tool, and a text-only terminal would end the caller's agent
	// turn mid-task. The retry re-emits with a corrective suffix.
	if deferEmptyOutput && !s.toolCallsDetected &&
		assistantturn.ClassifyRetryKind(turn) == assistantturn.RetryKindMalformedToolCall {
		s.deferredRetryKind = assistantturn.RetryKindMalformedToolCall
		return false
	}

	s.ended = true
	s.closeThinkingBlock()

	if s.bufferToolContent && !s.toolCallsDetected {
		if len(turn.ToolCalls) > 0 {
			stopReason = "tool_use"
			for _, tc := range turn.ToolCalls {
				idx := s.nextBlockIndex
				s.nextBlockIndex++
				s.sendToolUseBlock(idx, tc)
			}
		} else if finalText != "" && !s.textEmitted {
			idx := s.nextBlockIndex
			s.nextBlockIndex++
			s.send("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": idx,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			})
			s.send("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]any{
					"type": "text_delta",
					"text": finalText,
				},
			})
			s.textEmitted = true
			s.send("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": idx,
			})
		}
	}

	if outcome.HasToolCalls {
		stopReason = "tool_use"
	}
	if s.history != nil {
		s.history.Success(
			200,
			responsehistory.ThinkingForArchive(turn.RawThinking, turn.DetectionThinking, turn.Thinking),
			responsehistory.TextForArchive(turn.RawText, turn.Text),
			stopReason,
			responsehistory.GenericUsage(turn),
		)
	}

	s.send("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": outcome.Usage.OutputTokens,
		},
	})
	s.send("message_stop", map[string]any{"type": "message_stop"})
	return true
}
