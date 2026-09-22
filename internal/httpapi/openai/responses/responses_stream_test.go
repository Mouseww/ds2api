package responses

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
)

// responsesFixtureDS is the upstream fake used by the production-path tests
// below: every CallCompletion attempt replays the same SSE fixture body so the
// empty-output retry observes a stable outcome.
type responsesFixtureDS struct {
	body  string
	calls int
}

func (d *responsesFixtureDS) CreateSession(context.Context, *auth.RequestAuth, int) (string, error) {
	return "session-id", nil
}

func (d *responsesFixtureDS) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	return "pow", nil
}

func (d *responsesFixtureDS) UploadFile(context.Context, *auth.RequestAuth, dsclient.UploadFileRequest, int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id"}, nil
}

func (d *responsesFixtureDS) CallCompletion(context.Context, *auth.RequestAuth, map[string]any, string, int) (*http.Response, error) {
	d.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(d.body)),
	}, nil
}

func (d *responsesFixtureDS) DeleteSessionForToken(context.Context, string, string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (d *responsesFixtureDS) DeleteAllSessionsForToken(context.Context, string) error {
	return nil
}

// newResponsesProductionHandler wires the dependencies the real h.Responses
// entry point needs: a config store (model resolution + response-store TTL), a
// direct-token auth resolver and the upstream SSE fixture fake.
func newResponsesProductionHandler(t *testing.T, sseBody string) (*Handler, *responsesFixtureDS) {
	t.Helper()
	store, resolver := newDirectTokenResolver(t)
	ds := &responsesFixtureDS{body: sseBody}
	return &Handler{Store: store, Auth: resolver, DS: ds}, ds
}

// postResponsesJSON drives the production entry point h.Responses with a
// Responses-shaped body and a direct-token Authorization header.
func postResponsesJSON(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-a")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)
	return rec
}

// responsesRequestJSON builds a Responses request body. "thinking" is set
// explicitly because the production path derives the thinking flag from the
// request and GetModelConfig defaults it to true for every supported model.
func responsesRequestJSON(t *testing.T, model, input string, stream, thinking bool, tools []string, toolChoice any) string {
	t.Helper()
	req := map[string]any{
		"model":    model,
		"input":    input,
		"stream":   stream,
		"thinking": thinking,
	}
	if len(tools) > 0 {
		rawTools := make([]any, 0, len(tools))
		for _, name := range tools {
			rawTools = append(rawTools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":       name,
					"parameters": map[string]any{"type": "object"},
				},
			})
		}
		req["tools"] = rawTools
	}
	if toolChoice != nil {
		req["tool_choice"] = toolChoice
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal responses request: %v", err)
	}
	return string(raw)
}

func TestHandleResponsesStreamDoesNotEmitReasoningTextCompatEvents(t *testing.T) {
	b, _ := json.Marshal(map[string]any{
		"p": "response/thinking_content",
		"v": "thought",
	})
	streamBody := "data: " + string(b) + "\n" + "data: [DONE]\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-pro", "hi", true, true, nil, nil))

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.reasoning.delta") {
		t.Fatalf("expected response.reasoning.delta event, body=%s", body)
	}
	if strings.Contains(body, "event: response.reasoning_text.delta") || strings.Contains(body, "event: response.reasoning_text.done") {
		t.Fatalf("did not expect response.reasoning_text.* compatibility events, body=%s", body)
	}
}

func TestHandleResponsesStreamEmitsOutputTextDoneBeforeContentPartDone(t *testing.T) {
	sseLine := func(v string) string {
		b, _ := json.Marshal(map[string]any{
			"p": "response/content",
			"v": v,
		})
		return "data: " + string(b) + "\n"
	}

	streamBody := sseLine("hello") + "data: [DONE]\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", true, false, nil, nil))
	body := rec.Body.String()
	if !strings.Contains(body, "event: response.output_text.done") {
		t.Fatalf("expected response.output_text.done payload, body=%s", body)
	}
	textDoneIdx := strings.Index(body, "event: response.output_text.done")
	partDoneIdx := strings.Index(body, "event: response.content_part.done")
	if textDoneIdx < 0 || partDoneIdx < 0 {
		t.Fatalf("expected output_text.done + content_part.done, body=%s", body)
	}
	if textDoneIdx > partDoneIdx {
		t.Fatalf("expected output_text.done before content_part.done, body=%s", body)
	}
}

func TestHandleResponsesStreamOutputTextDeltaCarriesItemIndexes(t *testing.T) {
	sseLine := func(v string) string {
		b, _ := json.Marshal(map[string]any{
			"p": "response/content",
			"v": v,
		})
		return "data: " + string(b) + "\n"
	}

	streamBody := sseLine("hello") + "data: [DONE]\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", true, false, nil, nil))
	body := rec.Body.String()

	deltaPayload, ok := extractSSEEventPayload(body, "response.output_text.delta")
	if !ok {
		t.Fatalf("expected response.output_text.delta payload, body=%s", body)
	}
	if strings.TrimSpace(asString(deltaPayload["item_id"])) == "" {
		t.Fatalf("expected non-empty item_id in output_text.delta, payload=%#v", deltaPayload)
	}
	if _, ok := deltaPayload["output_index"]; !ok {
		t.Fatalf("expected output_index in output_text.delta, payload=%#v", deltaPayload)
	}
	if _, ok := deltaPayload["content_index"]; !ok {
		t.Fatalf("expected content_index in output_text.delta, payload=%#v", deltaPayload)
	}
}

func TestHandleResponsesStreamCoalescesSmallOutputTextDeltas(t *testing.T) {
	var streamBody strings.Builder
	for i := 0; i < 100; i++ {
		b, _ := json.Marshal(map[string]any{
			"p": "response/content",
			"v": "字",
		})
		streamBody.WriteString("data: ")
		streamBody.WriteString(string(b))
		streamBody.WriteString("\n")
	}
	streamBody.WriteString("data: [DONE]\n")
	h, _ := newResponsesProductionHandler(t, streamBody.String())

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", true, false, nil, nil))

	payloads := extractSSEEventPayloads(rec.Body.String(), "response.output_text.delta")
	if len(payloads) == 0 {
		t.Fatalf("expected response.output_text.delta payloads, body=%s", rec.Body.String())
	}
	var content strings.Builder
	for _, payload := range payloads {
		content.WriteString(asString(payload["delta"]))
	}
	if got, want := content.String(), strings.Repeat("字", 100); got != want {
		t.Fatalf("coalesced response content mismatch: got %q want %q body=%s", got, want, rec.Body.String())
	}
	if len(payloads) >= 100 {
		t.Fatalf("expected coalescing to reduce 100 tiny text deltas, got %d body=%s", len(payloads), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: response.completed") {
		t.Fatalf("expected completed event, body=%s", rec.Body.String())
	}
}

func TestHandleResponsesStreamEmitsDistinctToolCallIDsAcrossSeparateToolBlocks(t *testing.T) {
	sseLine := func(v string) string {
		b, _ := json.Marshal(map[string]any{
			"p": "response/content",
			"v": v,
		})
		return "data: " + string(b) + "\n"
	}

	streamBody := sseLine("前置文本\n<tool_calls>\n  <invoke name=\"read_file\">\n    <parameter name=\"path\">README.MD</parameter>\n  </invoke>\n</tool_calls>") +
		sseLine("中间文本\n<tool_calls>\n  <invoke name=\"search\">\n    <parameter name=\"q\">golang</parameter>\n  </invoke>\n</tool_calls>") +
		"data: [DONE]\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", true, false, []string{"read_file", "search"}, nil))

	body := rec.Body.String()
	doneEvents := extractSSEEventPayloads(body, "response.function_call_arguments.done")
	if len(doneEvents) < 2 {
		t.Fatalf("expected at least two function call done events, got %d body=%s", len(doneEvents), body)
	}

	ids := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, payload := range doneEvents {
		callID := asString(payload["call_id"])
		if callID == "" {
			continue
		}
		if _, ok := seen[callID]; ok {
			continue
		}
		seen[callID] = struct{}{}
		ids = append(ids, callID)
	}

	if len(ids) != 2 {
		t.Fatalf("expected two distinct call ids, got %#v body=%s", ids, body)
	}
	if ids[0] == ids[1] {
		t.Fatalf("expected distinct call ids across blocks, got %#v body=%s", ids, body)
	}
}

func TestHandleResponsesStreamRequiredToolChoiceFailure(t *testing.T) {
	sseLine := func(v string) string {
		b, _ := json.Marshal(map[string]any{
			"p": "response/content",
			"v": v,
		})
		return "data: " + string(b) + "\n"
	}

	streamBody := sseLine("plain text only") + "data: [DONE]\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", true, false, []string{"read_file"}, "required"))

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("expected response.failed event for required tool_choice violation, body=%s", body)
	}
	if strings.Contains(body, "event: response.completed") {
		t.Fatalf("did not expect response.completed after failure, body=%s", body)
	}
}

func TestHandleResponsesStreamFailsWhenUpstreamHasOnlyThinking(t *testing.T) {
	sseLine := func(path, value string) string {
		b, _ := json.Marshal(map[string]any{
			"p": path,
			"v": value,
		})
		return "data: " + string(b) + "\n"
	}

	streamBody := sseLine("response/thinking_content", "Only thinking") + "data: [DONE]\n"
	h, ds := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-pro", "hi", true, true, nil, nil))

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("expected response.failed event, body=%s", body)
	}
	if strings.Contains(body, "event: response.completed") {
		t.Fatalf("did not expect response.completed, body=%s", body)
	}
	payload, ok := extractSSEEventPayload(body, "response.failed")
	if !ok {
		t.Fatalf("expected response.failed payload, body=%s", body)
	}
	errObj, _ := payload["error"].(map[string]any)
	if asString(errObj["code"]) != "upstream_empty_output" {
		t.Fatalf("expected code=upstream_empty_output, got %#v", payload)
	}
	// The production stream path runs the shared empty-output retry: the
	// thinking-only attempt is deferred and replayed once against the same
	// fixture before the terminal failure is written.
	if ds.calls < 2 {
		t.Fatalf("expected empty-output retry to re-call upstream, calls=%d", ds.calls)
	}
}

func TestHandleResponsesStreamPromotesThinkingToolCallsOnFinalizeWithoutMidstreamIntercept(t *testing.T) {
	sseLine := func(path, value string) string {
		b, _ := json.Marshal(map[string]any{
			"p": path,
			"v": value,
		})
		return "data: " + string(b) + "\n"
	}

	streamBody := sseLine("response/thinking_content", `<tool_calls><invoke name="read_file"><parameter name="path">README.MD</parameter></invoke></tool_calls>`) + "data: [DONE]\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-pro", "hi", true, true, []string{"read_file"}, nil))

	body := rec.Body.String()
	if strings.Contains(body, "event: response.reasoning.delta") {
		t.Fatalf("did not expect leaked reasoning delta in stream body, got %s", body)
	}
	if !strings.Contains(body, "event: response.function_call_arguments.done") {
		t.Fatalf("expected finalize fallback function call event, got %s", body)
	}
	if strings.Contains(body, "event: response.failed") {
		t.Fatalf("did not expect response.failed, body=%s", body)
	}
}

func TestHandleResponsesStreamPromotesHiddenThinkingDSMLToolCallsOnFinalize(t *testing.T) {
	sseLine := func(path, value string) string {
		b, _ := json.Marshal(map[string]any{
			"p": path,
			"v": value,
		})
		return "data: " + string(b) + "\n"
	}

	streamBody := sseLine("response/thinking_content", `<|DSML|tool_calls><|DSML|invoke name="read_file"><|DSML|parameter name="path">README.MD</|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`) + "data: [DONE]\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-pro", "hi", true, false, []string{"read_file"}, "required"))

	body := rec.Body.String()
	if strings.Contains(body, "event: response.reasoning.delta") {
		t.Fatalf("did not expect hidden reasoning delta in stream body, got %s", body)
	}
	if !strings.Contains(body, "event: response.function_call_arguments.done") {
		t.Fatalf("expected hidden-thinking fallback function call event, got %s", body)
	}
	if strings.Contains(body, "event: response.failed") {
		t.Fatalf("did not expect response.failed, body=%s", body)
	}
}

func TestHandleResponsesNonStreamRequiredToolChoiceViolation(t *testing.T) {
	streamBody := `data: {"p":"response/content","v":"plain text only"}` + "\n" +
		`data: [DONE]` + "\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", false, false, []string{"read_file"}, "required"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for required tool_choice violation, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if asString(errObj["code"]) != "tool_choice_violation" {
		t.Fatalf("expected code=tool_choice_violation, got %#v", out)
	}
}

func TestHandleResponsesNonStreamRequiredToolChoiceIgnoresThinkingToolPayloadWhenTextExists(t *testing.T) {
	streamBody := `data: {"p":"response/thinking_content","v":"{\"tool_calls\":[{\"name\":\"read_file\",\"input\":{\"path\":\"README.MD\"}}]}"}` + "\n" +
		`data: {"p":"response/content","v":"plain text only"}` + "\n" +
		`data: [DONE]` + "\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", false, true, []string{"read_file"}, "required"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for required tool_choice violation, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if asString(errObj["code"]) != "tool_choice_violation" {
		t.Fatalf("expected code=tool_choice_violation, got %#v", out)
	}
}

func TestHandleResponsesNonStreamSingleAttemptReturns503WhenUpstreamOutputEmpty(t *testing.T) {
	streamBody := `data: {"p":"response/content","v":""}` + "\n" +
		`data: [DONE]` + "\n"
	h, ds := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", false, false, nil, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for empty upstream output, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if asString(errObj["code"]) != "upstream_unavailable" {
		t.Fatalf("expected code=upstream_unavailable, got %#v", out)
	}
	// Production enables the shared empty-output retry, so the empty first
	// attempt is replayed once (same fixture) before the terminal error.
	if ds.calls < 2 {
		t.Fatalf("expected empty-output retry to re-call upstream, calls=%d", ds.calls)
	}
}

func TestHandleResponsesNonStreamSingleAttemptReturnsContentFilterErrorWhenUpstreamFilteredWithoutOutput(t *testing.T) {
	streamBody := `data: {"code":"content_filter"}` + "\n" +
		`data: [DONE]` + "\n"
	h, ds := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-flash", "hi", false, false, nil, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for filtered empty upstream output, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if asString(errObj["code"]) != "content_filter" {
		t.Fatalf("expected code=content_filter, got %#v", out)
	}
	// Content-filtered turns are not retried by the shared empty-output retry.
	if ds.calls != 1 {
		t.Fatalf("expected no retry for content_filter, calls=%d", ds.calls)
	}
}

func TestHandleResponsesNonStreamSingleAttemptReturns429WhenUpstreamHasOnlyThinking(t *testing.T) {
	streamBody := `data: {"p":"response/thinking_content","v":"Only thinking"}` + "\n" +
		`data: [DONE]` + "\n"
	h, ds := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-pro", "hi", false, true, nil, nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for thinking-only upstream output, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if asString(errObj["code"]) != "upstream_empty_output" {
		t.Fatalf("expected code=upstream_empty_output, got %#v", out)
	}
	// Thinking-only output is retryable on the production path: the fixture is
	// replayed once (same empty outcome) before the 429 is returned.
	if ds.calls < 2 {
		t.Fatalf("expected empty-output retry to re-call upstream, calls=%d", ds.calls)
	}
}

func TestHandleResponsesNonStreamPromotesThinkingToolCallsWhenTextEmpty(t *testing.T) {
	streamBody := `data: {"p":"response/thinking_content","v":"<tool_calls><invoke name=\"read_file\"><parameter name=\"path\">README.MD</parameter></invoke></tool_calls>"}` + "\n" +
		`data: [DONE]` + "\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-pro", "hi", false, true, []string{"read_file"}, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for thinking tool calls, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	output, _ := out["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one output item, got %#v", out["output"])
	}
	first, _ := output[0].(map[string]any)
	if got := asString(first["type"]); got != "function_call" {
		t.Fatalf("expected function_call output, got %#v", first["type"])
	}
}

func TestHandleResponsesNonStreamPromotesHiddenThinkingDSMLToolCallsWhenTextEmpty(t *testing.T) {
	streamBody := `data: {"p":"response/thinking_content","v":"<|DSML|tool_calls><|DSML|invoke name=\"read_file\"><|DSML|parameter name=\"path\">README.MD</|DSML|parameter></|DSML|invoke></|DSML|tool_calls>"}` + "\n" +
		`data: [DONE]` + "\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	rec := postResponsesJSON(t, h, responsesRequestJSON(t, "deepseek-v4-pro", "hi", false, false, []string{"read_file"}, "required"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for hidden thinking tool calls, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	output, _ := out["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one output item, got %#v", out["output"])
	}
	first, _ := output[0].(map[string]any)
	if got := asString(first["type"]); got != "function_call" {
		t.Fatalf("expected function_call output, got %#v", first["type"])
	}
	if strings.Contains(rec.Body.String(), "reasoning") {
		t.Fatalf("did not expect hidden reasoning in response body, got %s", rec.Body.String())
	}
}

func TestHandleResponsesStreamCoercesSchemaDeclaredStringArguments(t *testing.T) {
	toolsRaw := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "Write",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{"type": "string"},
						"taskId":  map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	sseLine := func(v string) string {
		b, _ := json.Marshal(map[string]any{"p": "response/content", "v": v})
		return "data: " + string(b) + "\n"
	}
	streamBody := sseLine(`<tool_calls><invoke name="Write">{"input":{"content":{"message":"hi"},"taskId":1}}</invoke></tool_calls>`) + "data: [DONE]\n"
	h, _ := newResponsesProductionHandler(t, streamBody)

	reqBody, err := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"input":    "hi",
		"stream":   true,
		"thinking": false,
		"tools":    toolsRaw,
	})
	if err != nil {
		t.Fatalf("marshal responses request: %v", err)
	}

	rec := postResponsesJSON(t, h, string(reqBody))

	payload, ok := extractSSEEventPayload(rec.Body.String(), "response.function_call_arguments.done")
	if !ok {
		t.Fatalf("expected response.function_call_arguments.done payload, body=%s", rec.Body.String())
	}
	args := map[string]any{}
	if err := json.Unmarshal([]byte(asString(payload["arguments"])), &args); err != nil {
		t.Fatalf("decode streamed response arguments failed: %v", err)
	}
	if args["content"] != `{"message":"hi"}` {
		t.Fatalf("expected response content stringified by schema, got %#v", args["content"])
	}
	if args["taskId"] != "1" {
		t.Fatalf("expected response taskId stringified by schema, got %#v", args["taskId"])
	}
}

func extractSSEEventPayload(body, targetEvent string) (map[string]any, bool) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	matched := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "event: ") {
			evt := strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			matched = evt == targetEvent
			continue
		}
		if !matched || !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, false
		}
		return payload, true
	}
	return nil, false
}

func extractSSEEventPayloads(body, targetEvent string) []map[string]any {
	scanner := bufio.NewScanner(strings.NewReader(body))
	matched := false
	out := make([]map[string]any, 0, 4)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "event: ") {
			evt := strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			matched = evt == targetEvent
			continue
		}
		if !matched || !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			continue
		}
		out = append(out, payload)
	}
	return out
}
