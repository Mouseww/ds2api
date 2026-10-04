package sse

import (
	"io"
	"strings"
	"testing"
)

func TestSniffJSONErrorBodyInvalidPow(t *testing.T) {
	body := `{"code":40301,"msg":"INVALID_POW_RESPONSE","data":null}`
	replay, bodyErr := SniffJSONErrorBody(strings.NewReader(body))
	if bodyErr == nil {
		t.Fatalf("expected typed error for INVALID_POW_RESPONSE envelope")
	}
	if bodyErr.Code != UpstreamCodeInvalidPow {
		t.Fatalf("code = %q, want %q", bodyErr.Code, UpstreamCodeInvalidPow)
	}
	if bodyErr.Status != 503 {
		t.Fatalf("status = %d, want 503", bodyErr.Status)
	}
	if !strings.Contains(bodyErr.Message, "proof-of-work") {
		t.Fatalf("message = %q, want proof-of-work mention", bodyErr.Message)
	}
	if replay != nil {
		t.Fatalf("expected nil replay reader for typed envelope")
	}
}

func TestSniffJSONErrorBodyAccountMuted(t *testing.T) {
	body := `{"code":0,"msg":"","data":{"biz_code":5,"biz_msg":"user is muted","biz_data":{"is_muted":1,"mute_until":1790939390.395}}}`
	_, bodyErr := SniffJSONErrorBody(strings.NewReader(body))
	if bodyErr == nil {
		t.Fatalf("expected typed error for mute envelope")
	}
	if bodyErr.Code != UpstreamCodeAccountMuted {
		t.Fatalf("code = %q, want %q", bodyErr.Code, UpstreamCodeAccountMuted)
	}
	if bodyErr.Status != 403 {
		t.Fatalf("status = %d, want 403", bodyErr.Status)
	}
	if !strings.Contains(bodyErr.Message, "muted") {
		t.Fatalf("message = %q, want muted mention", bodyErr.Message)
	}
	if bodyErr.MuteUntil != 1790939390.395 {
		t.Fatalf("mute_until = %v, want 1790939390.395", bodyErr.MuteUntil)
	}
}

func TestSniffJSONErrorBodyGenericBizError(t *testing.T) {
	body := `{"code":0,"msg":"","data":{"biz_code":7,"biz_msg":"something else"}}`
	_, bodyErr := SniffJSONErrorBody(strings.NewReader(body))
	if bodyErr == nil {
		t.Fatalf("expected typed error for generic biz envelope")
	}
	if bodyErr.Code != UpstreamCodeError {
		t.Fatalf("code = %q, want %q", bodyErr.Code, UpstreamCodeError)
	}
	if !strings.Contains(bodyErr.Message, "something else") {
		t.Fatalf("message = %q, want biz_msg surfaced", bodyErr.Message)
	}
}

func TestSniffJSONErrorBodyTopLevelErrorKey(t *testing.T) {
	body := `{"error":{"code":"boom","detail":"kaboom"}}`
	_, bodyErr := SniffJSONErrorBody(strings.NewReader(body))
	if bodyErr == nil {
		t.Fatalf("expected typed error for top-level error key")
	}
	if bodyErr.Code != UpstreamCodeError {
		t.Fatalf("code = %q, want %q", bodyErr.Code, UpstreamCodeError)
	}
}

func TestSniffJSONErrorBodyReplaysHealthySSE(t *testing.T) {
	body := "event: ready\n" +
		`data: {"v":{"response_message_id":"abc"}}` + "\n" +
		`data: {"p":"response/content","v":"hello"}` + "\n" +
		"data: [DONE]\n"
	replay, bodyErr := SniffJSONErrorBody(strings.NewReader(body))
	if bodyErr != nil {
		t.Fatalf("healthy SSE misclassified: %+v", bodyErr)
	}
	if replay == nil {
		t.Fatalf("expected replay reader for healthy SSE")
	}
	got, err := io.ReadAll(replay)
	if err != nil {
		t.Fatalf("replay read failed: %v", err)
	}
	if string(got) != body {
		t.Fatalf("replayed body = %q, want %q", string(got), body)
	}
}

func TestSniffJSONErrorBodyReplaysNonJSONBody(t *testing.T) {
	body := ": keep-alive\n" + `data: {"p":"response/content","v":"hi"}` + "\n"
	replay, bodyErr := SniffJSONErrorBody(strings.NewReader(body))
	if bodyErr != nil {
		t.Fatalf("non-JSON SSE misclassified: %+v", bodyErr)
	}
	got, err := io.ReadAll(replay)
	if err != nil {
		t.Fatalf("replay read failed: %v", err)
	}
	if string(got) != body {
		t.Fatalf("replayed body = %q, want %q", string(got), body)
	}
}

func TestSniffJSONErrorBodyEmptyBody(t *testing.T) {
	replay, bodyErr := SniffJSONErrorBody(strings.NewReader(""))
	if bodyErr != nil {
		t.Fatalf("empty body misclassified: %+v", bodyErr)
	}
	if replay == nil {
		t.Fatalf("expected replay reader for empty body")
	}
}

func TestIsUpstreamBodyErrorCode(t *testing.T) {
	for _, code := range []string{UpstreamCodeAccountMuted, UpstreamCodeInvalidPow, UpstreamCodeError} {
		if !IsUpstreamBodyErrorCode(code) {
			t.Errorf("IsUpstreamBodyErrorCode(%q) = false, want true", code)
		}
	}
	for _, code := range []string{"", "upstream_unavailable", "error", "captcha_required"} {
		if IsUpstreamBodyErrorCode(code) {
			t.Errorf("IsUpstreamBodyErrorCode(%q) = true, want false", code)
		}
	}
}
