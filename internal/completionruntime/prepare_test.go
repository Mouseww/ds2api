package completionruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/promptcompat"
)

type prepareProbeCaller struct {
	sessionID        string
	createSessionErr error
	getPowErr        error
	uploadErr        error
	uploads          []dsclient.UploadFileRequest
	payloads         []map[string]any
	createAttempts   []int
	powAttempts      []int
}

func (f *prepareProbeCaller) CreateSession(_ context.Context, _ *auth.RequestAuth, maxAttempts int) (string, error) {
	f.createAttempts = append(f.createAttempts, maxAttempts)
	if f.createSessionErr != nil {
		return "", f.createSessionErr
	}
	if strings.TrimSpace(f.sessionID) == "" {
		return "session-prepare-1", nil
	}
	return f.sessionID, nil
}

func (f *prepareProbeCaller) GetPow(_ context.Context, _ *auth.RequestAuth, maxAttempts int) (string, error) {
	f.powAttempts = append(f.powAttempts, maxAttempts)
	if f.getPowErr != nil {
		return "", f.getPowErr
	}
	return "pow-prepare", nil
}

func (f *prepareProbeCaller) UploadFile(_ context.Context, _ *auth.RequestAuth, req dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	f.uploads = append(f.uploads, req)
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	return &dsclient.UploadFileResult{ID: "file-prepare-1", Filename: req.Filename, Bytes: int64(len(req.Data)), Status: "uploaded"}, nil
}

func (f *prepareProbeCaller) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	f.payloads = append(f.payloads, payload)
	return sseHTTPResponse(http.StatusOK, `data: {"p":"response/content","v":"should-not-happen"}`), nil
}

func prepareTestRequest() promptcompat.StandardRequest {
	return promptcompat.StandardRequest{
		Surface:         "test_adapter",
		RequestedModel:  "deepseek-v4-flash",
		ResolvedModel:   "deepseek-v4-flash",
		ResponseModel:   "deepseek-v4-flash",
		PromptTokenText: "large current input",
		FinalPrompt:     "large current input",
		Messages: []any{
			map[string]any{"role": "user", "content": "large current input"},
		},
	}
}

func TestPrepareCompletionRunsSharedPrepareSequenceWithoutCallingCompletion(t *testing.T) {
	ds := &prepareProbeCaller{}

	start, outErr := PrepareCompletion(context.Background(), ds, &auth.RequestAuth{DeepSeekToken: "token"}, prepareTestRequest(), Options{
		CurrentInputFile: currentInputRuntimeConfig{},
	})
	if outErr != nil {
		t.Fatalf("unexpected output error: %#v", outErr)
	}
	if len(ds.uploads) != 1 {
		t.Fatalf("expected current input upload through shared path, got %d", len(ds.uploads))
	}
	if got := ds.uploads[0].Filename; got != "DS2API_HISTORY.txt" {
		t.Fatalf("upload filename=%q want DS2API_HISTORY.txt", got)
	}
	if start.SessionID != "session-prepare-1" {
		t.Fatalf("session mismatch: %q", start.SessionID)
	}
	if start.Pow != "pow-prepare" {
		t.Fatalf("pow mismatch: %q", start.Pow)
	}
	if start.Response != nil {
		t.Fatalf("expected nil Response for prepare-only start, got %#v", start.Response)
	}
	if len(ds.payloads) != 0 {
		t.Fatalf("expected no completion call during prepare, got %d", len(ds.payloads))
	}
	if start.Payload == nil {
		t.Fatalf("expected completion payload to be assembled")
	}
	if got := start.Payload["chat_session_id"]; got != "session-prepare-1" {
		t.Fatalf("payload chat_session_id mismatch: %#v", got)
	}
	refIDs, _ := start.Payload["ref_file_ids"].([]any)
	if len(refIDs) != 1 || refIDs[0] != "file-prepare-1" {
		t.Fatalf("expected uploaded file id in payload ref_file_ids, got %#v", start.Payload["ref_file_ids"])
	}
	if prompt, _ := start.Payload["prompt"].(string); !strings.Contains(prompt, "Continue from the latest state in the attached DS2API_HISTORY.txt context.") {
		t.Fatalf("expected continuation prompt, got %q", prompt)
	}
	if !start.Request.CurrentInputFileApplied {
		t.Fatalf("expected prepared request to carry current input file state, got %#v", start.Request)
	}
	if len(ds.createAttempts) != 1 || ds.createAttempts[0] != 3 {
		t.Fatalf("expected create session with default max attempts 3, got %v", ds.createAttempts)
	}
	if len(ds.powAttempts) != 1 || ds.powAttempts[0] != 3 {
		t.Fatalf("expected get pow with default max attempts 3, got %v", ds.powAttempts)
	}
}

func TestPrepareCompletionSkipsCurrentInputFileWhenConfigAbsent(t *testing.T) {
	ds := &prepareProbeCaller{}

	start, outErr := PrepareCompletion(context.Background(), ds, &auth.RequestAuth{DeepSeekToken: "token"}, prepareTestRequest(), Options{})
	if outErr != nil {
		t.Fatalf("unexpected output error: %#v", outErr)
	}
	if len(ds.uploads) != 0 {
		t.Fatalf("expected no uploads without current input config, got %d", len(ds.uploads))
	}
	if start.SessionID != "session-prepare-1" || start.Pow != "pow-prepare" || start.Payload == nil {
		t.Fatalf("expected session, pow and payload to be prepared, got %#v", start)
	}
}

func TestPrepareCompletionMapsCreateSessionFailureToAuthError(t *testing.T) {
	ds := &prepareProbeCaller{createSessionErr: errors.New("session boom")}

	start, outErr := PrepareCompletion(context.Background(), ds, &auth.RequestAuth{DeepSeekToken: "token"}, prepareTestRequest(), Options{})
	if outErr == nil {
		t.Fatalf("expected output error for direct token")
	}
	if outErr.Status != http.StatusUnauthorized {
		t.Fatalf("direct token status mismatch: %d", outErr.Status)
	}
	if outErr.Message != "Invalid token. If this should be a DS2API key, add it to config.keys first." {
		t.Fatalf("direct token message mismatch: %q", outErr.Message)
	}
	if start.SessionID != "" || start.Payload != nil {
		t.Fatalf("expected empty start result on create session failure, got %#v", start)
	}

	start, outErr = PrepareCompletion(context.Background(), ds, &auth.RequestAuth{UseConfigToken: true, DeepSeekToken: "token"}, prepareTestRequest(), Options{})
	if outErr == nil {
		t.Fatalf("expected output error for managed account")
	}
	if outErr.Status != http.StatusUnauthorized {
		t.Fatalf("managed account status mismatch: %d", outErr.Status)
	}
	if outErr.Message != "Account token is invalid. Please re-login the account in admin." {
		t.Fatalf("managed account message mismatch: %q", outErr.Message)
	}
	if start.SessionID != "" || start.Payload != nil {
		t.Fatalf("expected empty start result on create session failure, got %#v", start)
	}
}

func TestPrepareCompletionMapsGetPowFailure(t *testing.T) {
	ds := &prepareProbeCaller{getPowErr: errors.New("pow boom")}

	start, outErr := PrepareCompletion(context.Background(), ds, &auth.RequestAuth{DeepSeekToken: "token"}, prepareTestRequest(), Options{})
	if outErr == nil {
		t.Fatalf("expected output error")
	}
	if outErr.Status != http.StatusUnauthorized || outErr.Message != "Failed to get PoW (invalid token or unknown error)." {
		t.Fatalf("pow failure mapping mismatch: %#v", outErr)
	}
	if start.SessionID != "session-prepare-1" {
		t.Fatalf("expected session id preserved on pow failure, got %q", start.SessionID)
	}
	if start.Payload != nil {
		t.Fatalf("expected no payload on pow failure, got %#v", start.Payload)
	}
}

func TestPrepareCompletionMapsCurrentInputFileUploadFailure(t *testing.T) {
	ds := &prepareProbeCaller{uploadErr: &dsclient.RequestFailure{Op: "upload file", Kind: dsclient.FailureManagedUnauthorized, Message: "expired token"}}

	start, outErr := PrepareCompletion(context.Background(), ds, &auth.RequestAuth{UseConfigToken: true, DeepSeekToken: "token"}, prepareTestRequest(), Options{
		CurrentInputFile: currentInputRuntimeConfig{},
	})
	if outErr == nil {
		t.Fatalf("expected output error")
	}
	if outErr.Status != http.StatusUnauthorized || outErr.Message != "Account token is invalid. Please re-login the account in admin." {
		t.Fatalf("upload failure mapping mismatch: %#v", outErr)
	}
	if start.SessionID != "" || start.Payload != nil {
		t.Fatalf("expected empty start result on upload failure, got %#v", start)
	}
	if len(ds.createAttempts) != 0 {
		t.Fatalf("expected no session creation after upload failure, got %v", ds.createAttempts)
	}
}
