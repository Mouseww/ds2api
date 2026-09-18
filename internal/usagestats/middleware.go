package usagestats

import (
	"net/http"
	"time"
)

// Middleware records exactly one usage event for every counted API request.
//
// Handlers annotate token counts through Annotate/AnnotateUsage; the surface,
// status code and latency are derived here. Requests that ClassifyRequest
// rejects (admin traffic, preflights, probes, static assets) pass through
// untouched.
func Middleware(store *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if next == nil {
			next = http.NotFoundHandler()
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				next.ServeHTTP(w, r)
				return
			}
			surface, ok := ClassifyRequest(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			startedAt := time.Now()
			recorder := &Recorder{}
			writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(writer, r.WithContext(WithRecorder(r.Context(), recorder)))

			usage := recorder.snapshot()
			store.Record(Event{
				Time:             startedAt,
				Surface:          surface,
				Method:           r.Method,
				Path:             r.URL.Path,
				Model:            usage.Model,
				AccountID:        usage.AccountID,
				CallerID:         usage.CallerID,
				StatusCode:       writer.statusCode(),
				Stream:           usage.Stream,
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				ReasoningTokens:  usage.ReasoningTokens,
				TotalTokens:      usage.TotalTokens,
				ElapsedMs:        time.Since(startedAt).Milliseconds(),
			})
		})
	}
}

// statusWriter captures the response status while preserving streaming support.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

// WriteHeader records the first status code that reaches the client.
func (w *statusWriter) WriteHeader(status int) {
	if w == nil {
		return
	}
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write records an implicit 200 before forwarding.
func (w *statusWriter) Write(body []byte) (int, error) {
	if w == nil {
		return 0, http.ErrBodyNotAllowed
	}
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(body)
}

// Flush keeps Server-Sent Events streaming through the wrapper.
func (w *statusWriter) Flush() {
	if w == nil {
		return
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

// statusCode reports the captured status, defaulting to 200.
func (w *statusWriter) statusCode() int {
	if w == nil || w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
