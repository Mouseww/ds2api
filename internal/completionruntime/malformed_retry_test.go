package completionruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ds2api/internal/assistantturn"
	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/promptcompat"
)

// malformedRetrySpecimen is the verbatim text block of a production failure:
// a coding agent's model tried to emit a grep tool call, but the DSML markup
// came out corrupted (fullwidth doubled ｜ pipes, a doubled parameter-name
// attribute, a stray CDATA close, no wrapper/invoke open tags). The sieve
// could not capture it and the response came back text-only, ending the
// agent's turn mid-task.
const malformedRetrySpecimen = "Now let me look at the turn finalization logic that decides empty-output retry.\n\n\n\n" +
	"<｜｜DSML｜｜ parameter name=\"parameter name=\"pattern\">func ShouldRetryEmptyOutput|func FinalizeTurn|func BuildTurnFromCollected]]</｜｜DSML｜｜ parameter>\n" +
	"<｜｜DSML｜｜ parameter name=\"path\">E:\\projects\\ds2api\\internal\\assistantturn</｜｜DSML｜｜ parameter>\n" +
	"</｜｜DSML｜｜ invoke>\n" +
	"</｜｜DSML｜｜ calls>"

// malformedRetryRecovery is a proper DSML tool call the model re-emits after
// the corrective retry suffix teaches the exact format.
const malformedRetryRecovery = "<|DSML|tool_calls>\n" +
	"<|DSML|invoke name=\"grep\">\n" +
	"<|DSML|parameter name=\"pattern\"><![CDATA[func ShouldRetryEmptyOutput]]></|DSML|parameter>\n" +
	"<|DSML|parameter name=\"path\"><![CDATA[E:\\projects\\ds2api\\internal\\assistantturn]]></|DSML|parameter>\n" +
	"</|DSML|invoke>\n" +
	"</|DSML|tool_calls>"

type malformedRetryCaller struct {
	attempts  []map[string]any
	responses []string
}

func (f *malformedRetryCaller) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-malformed-1", nil
}

func (f *malformedRetryCaller) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow-malformed", nil
}

func (f *malformedRetryCaller) UploadFile(_ context.Context, _ *auth.RequestAuth, req dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-malformed-1", Filename: req.Filename, Bytes: int64(len(req.Data)), Status: "uploaded"}, nil
}

func (f *malformedRetryCaller) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	f.attempts = append(f.attempts, payload)
	idx := len(f.attempts) - 1
	body := malformedRetrySpecimen
	if idx < len(f.responses) {
		body = f.responses[idx]
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return sseHTTPResponse(http.StatusOK,
		"data: {\"p\":\"response/content\",\"v\":"+string(encoded)+"}",
		"data: [DONE]"), nil
}

func (f *malformedRetryCaller) DeleteSessionForToken(_ context.Context, _ *auth.RequestAuth, _ string) error {
	return nil
}

func (f *malformedRetryCaller) DeleteAllSessionsForToken(_ context.Context, _ *auth.RequestAuth) error {
	return nil
}

func runMalformedRetry(t *testing.T, responses []string) (*malformedRetryCaller, NonStreamResult, *assistantturn.OutputError) {
	t.Helper()
	ds := &malformedRetryCaller{responses: responses}
	a := &auth.RequestAuth{DeepSeekToken: "token"}
	payload := map[string]any{"prompt": "original prompt"}
	initialResp, err := ds.CallCompletion(context.Background(), a, payload, "pow-malformed", 3)
	if err != nil {
		t.Fatalf("initial completion failed: %v", err)
	}
	result, outErr := ExecuteNonStreamStartedWithRetry(context.Background(), ds, a, StartResult{
		SessionID: "session-malformed-1",
		Payload:   payload,
		Pow:       "pow-malformed",
		Response:  initialResp,
		Request:   malformedRetryTestRequest(),
	}, Options{RetryEnabled: shared.EmptyOutputRetryEnabled(), RetryMaxAttempts: shared.EmptyOutputRetryMaxAttempts()})
	return ds, result, outErr
}

// TestExecuteNonStreamStartedWithRetryRecoversMalformedToolCall is the
// end-to-end regression for the dead-subagent failure mode: the first
// attempt returns text plus corrupted DSML markup (no parseable tool call),
// the runtime retries with the corrective suffix that teaches the exact
// format, and the second attempt's proper DSML tool call is returned as a
// structured tool call instead of a text-only response.
func TestExecuteNonStreamStartedWithRetryRecoversMalformedToolCall(t *testing.T) {
	ds, result, outErr := runMalformedRetry(t, []string{malformedRetrySpecimen, malformedRetryRecovery})

	if outErr != nil {
		t.Fatalf("expected successful retry, got error: %+v", outErr)
	}
	if len(ds.attempts) != 2 {
		t.Fatalf("expected exactly 2 completion attempts (initial + corrective retry), got %d", len(ds.attempts))
	}
	retryPrompt, _ := ds.attempts[1]["prompt"].(string)
	if !strings.Contains(retryPrompt, shared.MalformedToolCallRetrySuffix) {
		t.Fatalf("expected corrective suffix in retry prompt, got %q", retryPrompt)
	}
	if strings.Contains(retryPrompt, shared.EmptyOutputRetrySuffix) {
		t.Fatalf("retry prompt must not carry the generic empty-output suffix when the reason is a malformed tool call: %q", retryPrompt)
	}
	if len(result.Turn.ToolCalls) == 0 {
		t.Fatalf("expected the recovered tool call to be parsed, got %+v", result.Turn)
	}
	if result.Turn.ToolCalls[0].Name != "grep" {
		t.Fatalf("expected recovered tool name grep, got %q", result.Turn.ToolCalls[0].Name)
	}
}

// TestExecuteNonStreamStartedWithRetryMalformedExhaustedReturnsText pins the
// exhaustion path: when the retry also fails to produce a tool call, the
// attempt is returned as-is (sanitized visible text, no fabricated call).
func TestExecuteNonStreamStartedWithRetryMalformedExhaustedReturnsText(t *testing.T) {
	ds, result, outErr := runMalformedRetry(t, []string{malformedRetrySpecimen, malformedRetrySpecimen})

	if outErr != nil {
		t.Fatalf("exhausted malformed retry must return the turn, not an error: %+v", outErr)
	}
	if len(ds.attempts) != 2 {
		t.Fatalf("expected initial + one retry, got %d attempts", len(ds.attempts))
	}
	if len(result.Turn.ToolCalls) != 0 {
		t.Fatalf("expected no fabricated tool calls, got %+v", result.Turn.ToolCalls)
	}
	if strings.Contains(result.Turn.Text, "DSML") {
		t.Fatalf("corrupted DSML markup must not survive into visible text: %q", result.Turn.Text)
	}
	if !strings.Contains(result.Turn.Text, "Now let me look at the turn finalization logic") {
		t.Fatalf("expected the visible answer text preserved, got %q", result.Turn.Text)
	}
}

func malformedRetryTestRequest() promptcompat.StandardRequest {
	return promptcompat.StandardRequest{
		Surface:         "test_adapter",
		RequestedModel:  "deepseek-v4-flash",
		ResolvedModel:   "deepseek-v4-flash",
		ResponseModel:   "deepseek-v4-flash",
		PromptTokenText: "original prompt",
		FinalPrompt:     "original prompt",
		Messages: []any{
			map[string]any{"role": "user", "content": "original prompt"},
		},
	}
}
