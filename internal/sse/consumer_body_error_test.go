package sse

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCollectStreamTypedBodyError(t *testing.T) {
	body := `{"code":40301,"msg":"INVALID_POW_RESPONSE","data":null}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	resp.Header = http.Header{"Content-Type": []string{"application/json"}}
	got := CollectStream(resp, true, true)
	if got.BodyError == nil {
		t.Fatalf("expected BodyError for JSON envelope body")
	}
	if got.BodyError.Code != UpstreamCodeInvalidPow {
		t.Fatalf("code = %q, want %q", got.BodyError.Code, UpstreamCodeInvalidPow)
	}
	if got.Text != "" || got.Thinking != "" {
		t.Fatalf("no content expected, got text=%q thinking=%q", got.Text, got.Thinking)
	}
}

func TestCollectStreamMutedBodyError(t *testing.T) {
	body := `{"code":0,"msg":"","data":{"biz_code":5,"biz_msg":"user is muted","biz_data":{"is_muted":1,"mute_until":1790939390.395}}}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	resp.Header = http.Header{"Content-Type": []string{"application/json"}}
	got := CollectStream(resp, true, true)
	if got.BodyError == nil {
		t.Fatalf("expected BodyError for mute envelope body")
	}
	if got.BodyError.Code != UpstreamCodeAccountMuted {
		t.Fatalf("code = %q, want %q", got.BodyError.Code, UpstreamCodeAccountMuted)
	}
	if got.BodyError.MuteUntil != 1790939390.395 {
		t.Fatalf("mute_until = %v, want 1790939390.395", got.BodyError.MuteUntil)
	}
}

func TestCollectStreamEnvelopeWithoutJSONContentTypeNotSniffed(t *testing.T) {
	// Graceful degradation: if DeepSeek ever serves the envelope without a
	// JSON content type, the sniff is skipped and the body is scanned as SSE
	// (i.e. today's behavior) instead of blocking on a first-line read of a
	// healthy stream.
	body := `{"code":40301,"msg":"INVALID_POW_RESPONSE","data":null}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	resp.Header = http.Header{"Content-Type": []string{"text/event-stream"}}
	got := CollectStream(resp, true, true)
	if got.BodyError != nil {
		t.Fatalf("non-JSON content type must not be sniffed: %+v", got.BodyError)
	}
	if got.Text != "" {
		t.Fatalf("no content expected, got %q", got.Text)
	}
}

func TestCollectStreamHealthyBodyStillCollected(t *testing.T) {
	body := "event: ready\n" +
		`data: {"response_message_id":42}` + "\n" +
		`data: {"p":"response/content","v":"hello"}` + "\n" +
		"data: [DONE]\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	got := CollectStream(resp, true, true)
	if got.BodyError != nil {
		t.Fatalf("healthy stream misclassified: %+v", got.BodyError)
	}
	if got.Text != "hello" {
		t.Fatalf("text = %q, want %q", got.Text, "hello")
	}
	if got.ResponseMessageID != 42 {
		t.Fatalf("response_message_id = %v, want 42", got.ResponseMessageID)
	}
}

func TestCollectStreamInStreamErrorMessage(t *testing.T) {
	body := `data: {"error":{"code":"upstream_broken","message":"model unavailable"}}` + "\n" +
		"data: [DONE]\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	got := CollectStream(resp, true, true)
	if got.BodyError != nil {
		t.Fatalf("in-stream event is not a body error: %+v", got.BodyError)
	}
	if got.ErrorMessage == "" {
		t.Fatalf("expected ErrorMessage to capture the in-stream error event")
	}
	if !strings.Contains(got.ErrorMessage, "upstream_broken") {
		t.Fatalf("ErrorMessage = %q, want the upstream error surfaced", got.ErrorMessage)
	}
}
