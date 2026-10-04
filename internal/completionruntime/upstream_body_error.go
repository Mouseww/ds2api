package completionruntime

import (
	"io"
	"net/http"

	"ds2api/internal/config"
	"ds2api/internal/sse"
)

// sniffedBody replays the bytes consumed by the JSON-envelope sniff and
// still closes the original response body.
type sniffedBody struct {
	replay io.Reader
	orig   io.Closer
}

func (b *sniffedBody) Read(p []byte) (int, error) { return b.replay.Read(p) }

func (b *sniffedBody) Close() error { return b.orig.Close() }

// sniffUpstreamBodyError checks a completion response for the plain-JSON
// error envelope DeepSeek sometimes returns instead of an SSE stream
// (INVALID_POW_RESPONSE, "user is muted", ...). Those envelopes arrive with
// HTTP 200, so the status check cannot see them and the SSE layer would
// report "no output".
//
// The sniff is gated on the JSON content type (verified live: envelopes are
// served as application/json, healthy completions as text/event-stream) so it
// never reads ahead of a healthy SSE stream — a blocking first-line read
// there would swallow the keep-alive window before the first ping tick.
//
// On a healthy SSE body the response body is rewrapped with the sniffed
// prefix replayed and nil is returned; the consume path proceeds unchanged
// (closing the rewrapped body still closes the original). On a typed
// envelope the body was fully consumed, the original body is closed here,
// and the typed error is returned for the retry loops to act on.
func sniffUpstreamBodyError(resp *http.Response) *sse.UpstreamBodyError {
	if resp == nil || resp.Body == nil || resp.StatusCode != http.StatusOK || !sse.JSONEnvelopeContentType(resp.Header.Get("Content-Type")) {
		return nil
	}
	orig := resp.Body
	replay, bodyErr := sse.SniffJSONErrorBody(orig)
	if bodyErr != nil {
		if err := orig.Close(); err != nil {
			config.Logger.Warn("[completion_runtime] closing upstream error body failed", "error", err)
		}
		resp.Body = http.NoBody
		return bodyErr
	}
	resp.Body = &sniffedBody{replay: replay, orig: orig}
	return nil
}
