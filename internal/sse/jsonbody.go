package sse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Typed codes for the plain-JSON error envelopes DeepSeek sometimes returns
// in place of an SSE completion stream (with HTTP 200, so the status check
// alone cannot see them). See SniffJSONErrorBody.
const (
	// UpstreamCodeAccountMuted marks a "user is muted" envelope
	// (data.biz_code != 0 with data.biz_data.is_muted == 1). The account is
	// unusable until mute_until; requests must switch accounts, not retry.
	UpstreamCodeAccountMuted = "upstream_account_muted"
	// UpstreamCodeInvalidPow marks an INVALID_POW_RESPONSE envelope
	// (code 40301): the proof-of-work challenge was rejected. One fresh-PoW
	// retry on the same account is worthwhile.
	UpstreamCodeInvalidPow = "upstream_invalid_pow"
	// UpstreamCodeError marks any other JSON error envelope.
	UpstreamCodeError = "upstream_error"
)

// UpstreamBodyError is a typed classification of a plain-JSON error body
// that DeepSeek returned instead of an SSE completion stream.
type UpstreamBodyError struct {
	Code      string
	Message   string
	Status    int
	MuteUntil float64
}

// IsUpstreamBodyErrorCode reports whether the given output error code is one
// of the typed JSON-envelope codes classified by SniffJSONErrorBody.
func IsUpstreamBodyErrorCode(code string) bool {
	switch code {
	case UpstreamCodeAccountMuted, UpstreamCodeInvalidPow, UpstreamCodeError:
		return true
	}
	return false
}

// JSONEnvelopeContentType reports whether a response with this Content-Type
// can carry the plain-JSON error envelope instead of an SSE stream. DeepSeek
// serves the envelopes as application/json (verified live: HTTP 200 with
// content-type application/json) and healthy completions as text/event-stream.
// Absent or non-JSON content types are not sniffed so the sniff never reads
// ahead of a healthy SSE stream — a blocking first-line read there would
// swallow the keep-alive window before the first ping tick.
func JSONEnvelopeContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "json")
}

// SniffJSONErrorBody checks a completion response body for the plain-JSON
// error envelope DeepSeek occasionally returns instead of an SSE stream —
// observed forms include {"code":40301,"msg":"INVALID_POW_RESPONSE","data":null}
// and {"code":0,"msg":"","data":{"biz_code":5,"biz_msg":"user is muted",
// "biz_data":{"is_muted":1,"mute_until":...}}}. Such bodies arrive with HTTP
// 200, so without this sniff they parse as an empty stream and get
// misreported as a generic "no output" failure.
//
// When the body is a classified error envelope it has been fully consumed
// and the caller only needs to close the original body; the typed error is
// returned. Otherwise the returned reader replays everything that was read
// and the body must be consumed through it (the original body must still be
// closed by whoever owns it).
func SniffJSONErrorBody(body io.Reader) (io.Reader, *UpstreamBodyError) {
	if body == nil {
		return body, nil
	}
	br := bufio.NewReader(body)
	line, readErr := br.ReadBytes('\n')
	if readErr != nil && readErr != io.EOF {
		// A transport-level read failure is not an error envelope; replay
		// what was read and let the SSE scanner surface the failure.
		return io.MultiReader(bytes.NewReader(line), br), nil
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		// Healthy SSE bodies start with "event:"/"data:" lines, never with a
		// bare JSON object.
		return io.MultiReader(bytes.NewReader(line), br), nil
	}
	rest, readErr := io.ReadAll(br)
	if readErr != nil {
		return io.MultiReader(bytes.NewReader(line), bytes.NewReader(rest)), nil
	}
	raw := append(bytes.Clone(line), rest...)
	envelope, ok := decodeUpstreamEnvelope(raw)
	if !ok {
		// A bare JSON first line that is not an error envelope (e.g. an
		// unusual SSE chunk): replay everything unchanged.
		return io.MultiReader(bytes.NewReader(line), bytes.NewReader(rest)), nil
	}
	if bodyErr := classifyUpstreamEnvelope(envelope); bodyErr != nil {
		// Typed envelope: the body is fully consumed; the caller only needs
		// to close the original body.
		return nil, bodyErr
	}
	return io.MultiReader(bytes.NewReader(line), bytes.NewReader(rest)), nil
}

type upstreamEnvelope struct {
	Code  any                   `json:"code"`
	Msg   string                `json:"msg"`
	Data  *upstreamEnvelopeData `json:"data"`
	Error any                   `json:"error"`
}

type upstreamEnvelopeData struct {
	BizCode any                  `json:"biz_code"`
	BizMsg  string               `json:"biz_msg"`
	BizData *upstreamEnvelopeBiz `json:"biz_data"`
}

type upstreamEnvelopeBiz struct {
	IsMuted   any     `json:"is_muted"`
	MuteUntil float64 `json:"mute_until"`
}

func decodeUpstreamEnvelope(raw []byte) (upstreamEnvelope, bool) {
	var envelope upstreamEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return envelope, false
	}
	return envelope, true
}

func classifyUpstreamEnvelope(envelope upstreamEnvelope) *UpstreamBodyError {
	if code := intFromAny(envelope.Code); code != 0 {
		if code == 40301 || strings.Contains(strings.ToUpper(envelope.Msg), "INVALID_POW") {
			return &UpstreamBodyError{
				Code:    UpstreamCodeInvalidPow,
				Status:  http.StatusServiceUnavailable,
				Message: "Upstream rejected the proof-of-work challenge and returned no output.",
			}
		}
		return &UpstreamBodyError{
			Code:    UpstreamCodeError,
			Status:  http.StatusServiceUnavailable,
			Message: fmt.Sprintf("Upstream returned an error instead of a completion (%s).", firstNonBlank(envelope.Msg, fmt.Sprintf("code %d", code))),
		}
	}
	if envelope.Error != nil {
		return &UpstreamBodyError{
			Code:    UpstreamCodeError,
			Status:  http.StatusServiceUnavailable,
			Message: fmt.Sprintf("Upstream returned an error instead of a completion (%v).", envelope.Error),
		}
	}
	if envelope.Data != nil {
		if bizCode := intFromAny(envelope.Data.BizCode); bizCode != 0 {
			if envelope.Data.BizData != nil && intFromAny(envelope.Data.BizData.IsMuted) == 1 {
				return &UpstreamBodyError{
					Code:      UpstreamCodeAccountMuted,
					Status:    http.StatusForbidden,
					Message:   "Upstream account is muted and returned no output.",
					MuteUntil: envelope.Data.BizData.MuteUntil,
				}
			}
			return &UpstreamBodyError{
				Code:    UpstreamCodeError,
				Status:  http.StatusServiceUnavailable,
				Message: fmt.Sprintf("Upstream returned an error instead of a completion (%s).", firstNonBlank(envelope.Data.BizMsg, fmt.Sprintf("biz_code %d", bizCode))),
			}
		}
	}
	return nil
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return 0
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
