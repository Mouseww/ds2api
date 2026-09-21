package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ds2api/internal/account"
	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/promptcompat"
)

func TestIsVercelStreamPrepareRequest(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions?__stream_prepare=1", nil)
	if !isVercelStreamPrepareRequest(req) {
		t.Fatalf("expected prepare request to be detected")
	}

	req2 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if isVercelStreamPrepareRequest(req2) {
		t.Fatalf("expected non-prepare request")
	}
}

func TestIsVercelStreamReleaseRequest(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions?__stream_release=1", nil)
	if !isVercelStreamReleaseRequest(req) {
		t.Fatalf("expected release request to be detected")
	}

	req2 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if isVercelStreamReleaseRequest(req2) {
		t.Fatalf("expected non-release request")
	}
}

func TestVercelInternalSecret(t *testing.T) {
	t.Run("prefer explicit secret", func(t *testing.T) {
		t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")
		t.Setenv("DS2API_ADMIN_KEY", "admin-fallback")
		if got := vercelInternalSecret(); got != "stream-secret" {
			t.Fatalf("expected explicit secret, got %q", got)
		}
	})

	t.Run("fallback to admin key", func(t *testing.T) {
		t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "")
		t.Setenv("DS2API_ADMIN_KEY", "admin-fallback")
		if got := vercelInternalSecret(); got != "admin-fallback" {
			t.Fatalf("expected admin key fallback, got %q", got)
		}
	})

	t.Run("default admin when env missing", func(t *testing.T) {
		t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "")
		t.Setenv("DS2API_ADMIN_KEY", "")
		if got := vercelInternalSecret(); got != "admin" {
			t.Fatalf("expected default admin fallback, got %q", got)
		}
	})
}

func TestStreamLeaseLifecycle(t *testing.T) {
	h := &Handler{}
	leaseID := h.holdStreamLease(&auth.RequestAuth{UseConfigToken: false}, promptcompat.StandardRequest{}, "test-session-id")
	if leaseID == "" {
		t.Fatalf("expected non-empty lease id")
	}
	if lease, ok := h.releaseStreamLease(leaseID); !ok {
		t.Fatalf("expected lease release success")
	} else if lease.SessionID != "test-session-id" {
		t.Fatalf("expected released session id, got %q", lease.SessionID)
	}
	if _, ok := h.releaseStreamLease(leaseID); ok {
		t.Fatalf("expected duplicate release to fail")
	}
}

func TestStreamLeaseTTL(t *testing.T) {
	t.Setenv("DS2API_VERCEL_STREAM_LEASE_TTL_SECONDS", "120")
	if got := streamLeaseTTL(); got != 120*time.Second {
		t.Fatalf("expected ttl=120s, got %v", got)
	}
	t.Setenv("DS2API_VERCEL_STREAM_LEASE_TTL_SECONDS", "invalid")
	if got := streamLeaseTTL(); got != 15*time.Minute {
		t.Fatalf("expected default ttl on invalid value, got %v", got)
	}
}

func TestHandleVercelStreamPrepareAppliesCurrentInputFile(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	ds := &inlineUploadDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?__stream_prepare=1", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ds2-Internal-Token", "stream-secret")
	rec := httptest.NewRecorder()

	h.handleVercelStreamPrepare(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 current input upload, got %d", len(ds.uploadCalls))
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	payload, _ := body["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("expected payload object, got %#v", body["payload"])
	}
	promptText, _ := payload["prompt"].(string)
	if !strings.Contains(promptText, "Continue from the latest state in the attached DS2API_HISTORY.txt context.") {
		t.Fatalf("expected continuation prompt, got %s", promptText)
	}
	if strings.Contains(promptText, "first user turn") || strings.Contains(promptText, "latest user turn") {
		t.Fatalf("expected original turns hidden from prompt, got %s", promptText)
	}
	refIDs, _ := payload["ref_file_ids"].([]any)
	if len(refIDs) == 0 || refIDs[0] != "file-inline-1" {
		t.Fatalf("expected uploaded history file first in ref_file_ids, got %#v", payload["ref_file_ids"])
	}
}

func TestHandleVercelStreamPrepareUsesHalfwidthDSMLToolPrompt(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	h := &Handler{
		Store: mockOpenAIConfig{},
		Auth:  streamStatusAuthStub{},
		DS:    &inlineUploadDSStub{},
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "search docs"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "search",
					"description": "search docs",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{"type": "string"},
						},
						"required": []any{"query"},
					},
				},
			},
		},
		"stream": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?__stream_prepare=1", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ds2-Internal-Token", "stream-secret")
	rec := httptest.NewRecorder()

	h.handleVercelStreamPrepare(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	finalPrompt, _ := body["final_prompt"].(string)
	payload, _ := body["payload"].(map[string]any)
	payloadPrompt, _ := payload["prompt"].(string)
	for label, promptText := range map[string]string{"final_prompt": finalPrompt, "payload.prompt": payloadPrompt} {
		if !strings.Contains(promptText, "<|DSML|tool_calls>") || !strings.Contains(promptText, "Tag punctuation alphabet: ASCII < > / = \" plus the halfwidth pipe |.") {
			t.Fatalf("expected %s to contain halfwidth DSML tool instructions, got %q", label, promptText)
		}
		if strings.Contains(promptText, "\uff5c") || strings.Contains(promptText, "full"+"width vertical bar") {
			t.Fatalf("expected %s not to contain legacy pipe guidance, got %q", label, promptText)
		}
	}
	toolNames, _ := body["tool_names"].([]any)
	if len(toolNames) != 1 || toolNames[0] != "search" {
		t.Fatalf("expected prepared tool names to align with request tools, got %#v", body["tool_names"])
	}
}

type vercelReleaseAutoDeleteDSStub struct {
	resp             *http.Response
	deleteCallCount  int
	deletedSessionID string
	deletedToken     string
	deleteErr        error
	events           *[]string
}

func (m *vercelReleaseAutoDeleteDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-id", nil
}

func (m *vercelReleaseAutoDeleteDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (m *vercelReleaseAutoDeleteDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id", Filename: "file.txt", Bytes: 1, Status: "uploaded"}, nil
}

func (m *vercelReleaseAutoDeleteDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	return m.resp, nil
}

func (m *vercelReleaseAutoDeleteDSStub) DeleteSessionForToken(_ context.Context, token string, sessionID string) (*dsclient.DeleteSessionResult, error) {
	if m.events != nil {
		*m.events = append(*m.events, "delete")
	}
	m.deleteCallCount++
	m.deletedSessionID = sessionID
	m.deletedToken = token
	if m.deleteErr != nil {
		return nil, m.deleteErr
	}
	return &dsclient.DeleteSessionResult{SessionID: sessionID, Success: true}, nil
}

func (m *vercelReleaseAutoDeleteDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

type vercelReleaseAuthStub struct {
	events *[]string
}

func (a *vercelReleaseAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{DeepSeekToken: "test-token", AccountID: "test-account"}, nil
}

func (a *vercelReleaseAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{DeepSeekToken: "test-token", AccountID: "test-account"}, nil
}

func (a *vercelReleaseAuthStub) Release(_ *auth.RequestAuth) {
	if a.events != nil {
		*a.events = append(*a.events, "release")
	}
}

func TestHandleVercelStreamReleaseTriggersAutoDelete(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	events := []string{}
	ds := &vercelReleaseAutoDeleteDSStub{events: &events}
	h := &Handler{
		Store: mockOpenAIConfig{
			autoDeleteMode: "single",
		},
		Auth: &vercelReleaseAuthStub{events: &events},
		DS:   ds,
	}

	leaseID := h.holdStreamLease(&auth.RequestAuth{DeepSeekToken: "test-token", AccountID: "test-account"}, promptcompat.StandardRequest{}, "session-to-delete")
	if leaseID == "" {
		t.Fatalf("expected non-empty lease id")
	}

	reqBody := map[string]any{"lease_id": leaseID}
	reqJSON, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?__stream_release=1", strings.NewReader(string(reqJSON)))
	req.Header.Set("X-Ds2-Internal-Token", "stream-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleVercelStreamRelease(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ds.deleteCallCount != 1 {
		t.Fatalf("expected auto delete call count=1, got %d", ds.deleteCallCount)
	}
	if ds.deletedSessionID != "session-to-delete" {
		t.Fatalf("expected deleted session id=session-to-delete, got %q", ds.deletedSessionID)
	}
	if got, want := strings.Join(events, ","), "delete,release"; got != want {
		t.Fatalf("expected auto-delete before auth release, got %s", got)
	}
}

func TestHandleVercelStreamPrepareUploadsToolsSeparately(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	ds := &inlineUploadDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{currentInputEnabled: true},
		Auth:  streamStatusAuthStub{},
		DS:    ds,
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "search docs"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "search",
					"description": "search docs",
					"parameters":  map[string]any{"type": "object"},
				},
			},
		},
		"stream": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?__stream_prepare=1", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ds2-Internal-Token", "stream-secret")
	rec := httptest.NewRecorder()

	h.handleVercelStreamPrepare(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.uploadCalls) != 2 {
		t.Fatalf("expected history and tools uploads, got %d", len(ds.uploadCalls))
	}
	if ds.uploadCalls[0].Filename != "DS2API_HISTORY.txt" || ds.uploadCalls[1].Filename != "DS2API_TOOLS.txt" {
		t.Fatalf("unexpected upload filenames: %#v", ds.uploadCalls)
	}
	if strings.Contains(string(ds.uploadCalls[0].Data), "Description: search docs") {
		t.Fatalf("history transcript should not embed tool descriptions, got %q", string(ds.uploadCalls[0].Data))
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	finalPrompt, _ := body["final_prompt"].(string)
	payload, _ := body["payload"].(map[string]any)
	payloadPrompt, _ := payload["prompt"].(string)
	for label, promptText := range map[string]string{"final_prompt": finalPrompt, "payload.prompt": payloadPrompt} {
		if !strings.Contains(promptText, "DS2API_TOOLS.txt") || !strings.Contains(promptText, "TOOL CALL FORMAT") {
			t.Fatalf("expected %s to reference tools file and retain tool instructions, got %q", label, promptText)
		}
		if strings.Contains(promptText, "Description: search docs") {
			t.Fatalf("expected %s not to inline tool descriptions, got %q", label, promptText)
		}
	}
	refIDs, _ := payload["ref_file_ids"].([]any)
	if len(refIDs) < 2 || refIDs[0] != "file-inline-1" || refIDs[1] != "file-inline-2" {
		t.Fatalf("expected history and tools ref ids first, got %#v", payload["ref_file_ids"])
	}
}

func TestHandleVercelStreamPrepareMapsCurrentInputFileManagedAuthFailureTo401(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	ds := &inlineUploadDSStub{
		uploadErr: &dsclient.RequestFailure{Op: "upload file", Kind: dsclient.FailureManagedUnauthorized, Message: "expired token"},
	}
	h := &Handler{
		Store: mockOpenAIConfig{
			currentInputEnabled: true,
		},
		Auth: streamStatusManagedAuthStub{},
		DS:   ds,
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?__stream_prepare=1", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer managed-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ds2-Internal-Token", "stream-secret")
	rec := httptest.NewRecorder()

	h.handleVercelStreamPrepare(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Please re-login the account in admin") {
		t.Fatalf("expected managed auth error message, got %s", rec.Body.String())
	}
}

func TestHandleVercelStreamSwitchReuploadsCurrentInputFile(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@test.com","password":"pwd"},
			{"email":"acc2@test.com","password":"pwd"}
		]
	}`)
	store := config.LoadStore()
	resolver := auth.NewResolver(store, account.NewPool(store), func(_ context.Context, acc config.Account) (string, error) {
		return "token-" + acc.Identifier(), nil
	})
	authReq := httptest.NewRequest(http.MethodPost, "/", nil)
	authReq.Header.Set("Authorization", "Bearer managed-key")
	a, err := resolver.Determine(authReq)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	defer resolver.Release(a)

	ds := &inlineUploadDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{currentInputEnabled: true},
		Auth:  resolver,
		DS:    ds,
	}
	stdReq := promptcompat.StandardRequest{
		RequestedModel:          "deepseek-v4-flash",
		ResolvedModel:           "deepseek-v4-flash",
		ResponseModel:           "deepseek-v4-flash",
		FinalPrompt:             "Continue from the latest state in the attached DS2API_HISTORY.txt context. Available tool descriptions and parameter schemas are attached in DS2API_TOOLS.txt; use only those tools and follow the tool-call format rules in this prompt.",
		PromptTokenText:         "# DS2API_HISTORY.txt\n\n=== 1. USER ===\nhello\n\n# DS2API_TOOLS.txt\nAvailable tool descriptions and parameter schemas for this request.\n\nYou have access to these tools:\n\nTool: search\nDescription: search docs\nParameters: {\"type\":\"object\"}\n",
		HistoryText:             "# DS2API_HISTORY.txt\n\n=== 1. USER ===\nhello\n",
		CurrentInputFileApplied: true,
		CurrentInputFileID:      "file-old",
		CurrentToolsFileID:      "file-old-tools",
		ToolsRaw: []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "search",
					"description": "search docs",
					"parameters":  map[string]any{"type": "object"},
				},
			},
		},
		RefFileIDs: []string{"file-old", "file-old-tools", "client-file"},
		Thinking:   true,
	}
	leaseID := h.holdStreamLease(a, stdReq, "")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?__stream_switch=1", strings.NewReader(`{"lease_id":"`+leaseID+`"}`))
	req.Header.Set("X-Ds2-Internal-Token", "stream-secret")
	rec := httptest.NewRecorder()

	h.handleVercelStreamSwitch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.uploadCalls) != 2 {
		t.Fatalf("expected current input and tools reupload on switched account, got %d", len(ds.uploadCalls))
	}
	if ds.uploadCalls[0].Filename != "DS2API_HISTORY.txt" || ds.uploadCalls[1].Filename != "DS2API_TOOLS.txt" {
		t.Fatalf("unexpected reupload filenames: %#v", ds.uploadCalls)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if body["deepseek_token"] != "token-acc2@test.com" {
		t.Fatalf("expected switched account token, got %#v", body["deepseek_token"])
	}
	payload, _ := body["payload"].(map[string]any)
	refIDs, _ := payload["ref_file_ids"].([]any)
	if len(refIDs) != 3 || refIDs[0] != "file-inline-1" || refIDs[1] != "file-inline-2" || refIDs[2] != "client-file" {
		t.Fatalf("expected reuploaded current input ref plus client ref, got %#v", payload["ref_file_ids"])
	}
	promptText, _ := payload["prompt"].(string)
	if !strings.Contains(promptText, "DS2API_TOOLS.txt") {
		t.Fatalf("expected switched payload prompt to retain tools file reference, got %q", promptText)
	}
}

type prepareProbeDSStub struct {
	createSessionErr error
	getPowErr        error
	events           []string
	uploadCount      int
}

func (m *prepareProbeDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	m.events = append(m.events, "create_session")
	if m.createSessionErr != nil {
		return "", m.createSessionErr
	}
	return "session-id", nil
}

func (m *prepareProbeDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	m.events = append(m.events, "get_pow")
	if m.getPowErr != nil {
		return "", m.getPowErr
	}
	return "pow", nil
}

func (m *prepareProbeDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, req dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	m.uploadCount++
	m.events = append(m.events, "upload:"+req.Filename)
	return &dsclient.UploadFileResult{
		ID:       fmt.Sprintf("file-probe-%d", m.uploadCount),
		Filename: req.Filename,
		Bytes:    int64(len(req.Data)),
		Status:   "uploaded",
	}, nil
}

func (m *prepareProbeDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	m.events = append(m.events, "call_completion")
	return makeOpenAISSEHTTPResponse(
		`data: {"p":"response/content","v":"ok"}`,
		`data: [DONE]`,
	), nil
}

func (m *prepareProbeDSStub) DeleteSessionForToken(_ context.Context, _ string, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (m *prepareProbeDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func vercelPrepareTestRequest(reqBody map[string]any) *http.Request {
	raw, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?__stream_prepare=1", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ds2-Internal-Token", "stream-secret")
	return req
}

func TestHandleVercelStreamPrepareResponseShapeUnchanged(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	ds := &prepareProbeDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{},
		Auth:  streamStatusAuthStub{},
		DS:    ds,
	}
	req := vercelPrepareTestRequest(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"stream":   true,
		"thinking": true,
	})
	rec := httptest.NewRecorder()

	h.handleVercelStreamPrepare(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	wantKeys := map[string]bool{
		"session_id": false, "lease_id": false, "model": false, "final_prompt": false,
		"thinking_enabled": false, "search_enabled": false, "tool_names": false,
		"deepseek_token": false, "pow_header": false, "payload": false,
	}
	if len(body) != len(wantKeys) {
		t.Fatalf("expected exactly %d top-level response fields, got %d: %v", len(wantKeys), len(body), body)
	}
	for key := range body {
		if _, ok := wantKeys[key]; !ok {
			t.Fatalf("unexpected response field %q", key)
		}
	}
	if got := body["session_id"]; got != "session-id" {
		t.Fatalf("session_id mismatch: %#v", got)
	}
	leaseID, _ := body["lease_id"].(string)
	if leaseID == "" {
		t.Fatalf("expected non-empty lease_id")
	}
	if _, ok := h.lookupStreamLease(leaseID); !ok {
		t.Fatalf("expected lease_id to reference a held lease")
	}
	if got := body["model"]; got != "deepseek-v4-flash" {
		t.Fatalf("model mismatch: %#v", got)
	}
	finalPrompt, _ := body["final_prompt"].(string)
	if finalPrompt == "" {
		t.Fatalf("expected non-empty final_prompt")
	}
	if got, _ := body["thinking_enabled"].(bool); !got {
		t.Fatalf("expected thinking_enabled true, got %#v", body["thinking_enabled"])
	}
	if got, _ := body["search_enabled"].(bool); got {
		t.Fatalf("expected search_enabled false, got %#v", body["search_enabled"])
	}
	if _, ok := body["tool_names"].([]any); !ok {
		t.Fatalf("expected tool_names array, got %#v", body["tool_names"])
	}
	if got := body["deepseek_token"]; got != "direct-token" {
		t.Fatalf("deepseek_token mismatch: %#v", got)
	}
	if got := body["pow_header"]; got != "pow" {
		t.Fatalf("pow_header mismatch: %#v", got)
	}
	payload, _ := body["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("expected payload object, got %#v", body["payload"])
	}
	wantPayloadKeys := map[string]bool{
		"chat_session_id": false, "parent_message_id": false, "model_type": false, "prompt": false,
		"ref_file_ids": false, "thinking_enabled": false, "search_enabled": false,
		"action": false, "preempt": false,
	}
	if len(payload) != len(wantPayloadKeys) {
		t.Fatalf("expected exactly %d payload fields, got %d: %v", len(wantPayloadKeys), len(payload), payload)
	}
	for key := range payload {
		if _, ok := wantPayloadKeys[key]; !ok {
			t.Fatalf("unexpected payload field %q", key)
		}
	}
	if got := payload["chat_session_id"]; got != "session-id" {
		t.Fatalf("payload chat_session_id mismatch: %#v", got)
	}
	if got := payload["parent_message_id"]; got != nil {
		t.Fatalf("payload parent_message_id mismatch: %#v", got)
	}
	if got, _ := payload["prompt"].(string); got != finalPrompt {
		t.Fatalf("payload prompt should mirror final_prompt, got %q want %q", got, finalPrompt)
	}
	if got, _ := payload["thinking_enabled"].(bool); !got {
		t.Fatalf("payload thinking_enabled mismatch: %#v", payload["thinking_enabled"])
	}
	if _, ok := payload["ref_file_ids"].([]any); !ok {
		t.Fatalf("expected payload ref_file_ids array, got %#v", payload["ref_file_ids"])
	}
	if got := ds.events; strings.Join(got, ",") != "create_session,get_pow" {
		t.Fatalf("expected prepare to only create session and fetch pow, got %v", got)
	}
}

func TestHandleVercelStreamPrepareFlowsThroughSharedPrepareSequence(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	ds := &prepareProbeDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{currentInputEnabled: true},
		Auth:  streamStatusAuthStub{},
		DS:    ds,
	}
	req := vercelPrepareTestRequest(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   true,
	})
	rec := httptest.NewRecorder()

	h.handleVercelStreamPrepare(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := ds.events; strings.Join(got, ",") != "upload:DS2API_HISTORY.txt,create_session,get_pow" {
		t.Fatalf("expected current input upload before session creation through the shared path, got %v", got)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	payload, _ := body["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("expected payload object, got %#v", body["payload"])
	}
	refIDs, _ := payload["ref_file_ids"].([]any)
	if len(refIDs) != 1 || refIDs[0] != "file-probe-1" {
		t.Fatalf("expected uploaded current input file in payload ref_file_ids, got %#v", payload["ref_file_ids"])
	}
}

func TestHandleVercelStreamPrepareCreateSessionFailureKeeps401ErrorShape(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	for _, tc := range []struct {
		label       string
		authStub    shared.AuthResolver
		wantMessage string
	}{
		{
			label:       "direct token",
			authStub:    streamStatusAuthStub{},
			wantMessage: "Invalid token. If this should be a DS2API key, add it to config.keys first.",
		},
		{
			label:       "managed account",
			authStub:    streamStatusManagedAuthStub{},
			wantMessage: "Account token is invalid. Please re-login the account in admin.",
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			ds := &prepareProbeDSStub{createSessionErr: errors.New("session boom")}
			h := &Handler{
				Store: mockOpenAIConfig{},
				Auth:  tc.authStub,
				DS:    ds,
			}
			req := vercelPrepareTestRequest(map[string]any{
				"model":    "deepseek-v4-flash",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"stream":   true,
			})
			rec := httptest.NewRecorder()

			h.handleVercelStreamPrepare(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
			}
			var errBody map[string]any
			if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			errObj, _ := errBody["error"].(map[string]any)
			if errObj == nil {
				t.Fatalf("expected error object, got %s", rec.Body.String())
			}
			if len(errObj) != 4 {
				t.Fatalf("expected error object with 4 fields, got %v", errObj)
			}
			if errObj["message"] != tc.wantMessage {
				t.Fatalf("error message mismatch: %#v", errObj["message"])
			}
			if errObj["type"] != "authentication_error" {
				t.Fatalf("error type mismatch: %#v", errObj["type"])
			}
			if errObj["code"] != "authentication_failed" {
				t.Fatalf("error code mismatch: %#v", errObj["code"])
			}
			if errObj["param"] != nil {
				t.Fatalf("error param mismatch: %#v", errObj["param"])
			}
			createCalls := 0
			for _, event := range ds.events {
				if event == "create_session" {
					createCalls++
				}
			}
			if createCalls != 1 {
				t.Fatalf("expected single create session attempt without account fallback, got %d (events=%v)", createCalls, ds.events)
			}
		})
	}
}

func TestHandleVercelStreamPrepareGetPowFailureKeeps401ErrorShape(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	ds := &prepareProbeDSStub{getPowErr: errors.New("pow boom")}
	h := &Handler{
		Store: mockOpenAIConfig{},
		Auth:  streamStatusAuthStub{},
		DS:    ds,
	}
	req := vercelPrepareTestRequest(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"stream":   true,
	})
	rec := httptest.NewRecorder()

	h.handleVercelStreamPrepare(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	var errBody map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	errObj, _ := errBody["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("expected error object, got %s", rec.Body.String())
	}
	if errObj["message"] != "Failed to get PoW (invalid token or unknown error)." {
		t.Fatalf("error message mismatch: %#v", errObj["message"])
	}
	if errObj["type"] != "authentication_error" || errObj["code"] != "authentication_failed" {
		t.Fatalf("error shape mismatch: %#v", errObj)
	}
	if got := ds.events; strings.Join(got, ",") != "create_session,get_pow" {
		t.Fatalf("expected session creation before pow failure, got %v", got)
	}
}

func TestHandleVercelStreamPrepareStillAppliesThinkingInjection(t *testing.T) {
	t.Setenv("VERCEL", "1")
	t.Setenv("DS2API_VERCEL_INTERNAL_SECRET", "stream-secret")

	injectionEnabled := true
	h := &Handler{
		Store: mockOpenAIConfig{thinkingInjection: &injectionEnabled, thinkingPrompt: "THINK-HARDER-MARKER"},
		Auth:  streamStatusAuthStub{},
		DS:    &prepareProbeDSStub{},
	}
	req := vercelPrepareTestRequest(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"stream":   true,
		"thinking": true,
	})
	rec := httptest.NewRecorder()

	h.handleVercelStreamPrepare(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	finalPrompt, _ := body["final_prompt"].(string)
	if !strings.Contains(finalPrompt, "THINK-HARDER-MARKER") {
		t.Fatalf("expected thinking injection marker in final_prompt, got %q", finalPrompt)
	}
	payload, _ := body["payload"].(map[string]any)
	payloadPrompt, _ := payload["prompt"].(string)
	if !strings.Contains(payloadPrompt, "THINK-HARDER-MARKER") {
		t.Fatalf("expected thinking injection marker in payload prompt, got %q", payloadPrompt)
	}
}
