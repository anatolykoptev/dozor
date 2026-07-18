package mcpserver

import (
	"net/http"
	"sync/atomic"
)

// responseWriter wraps http.ResponseWriter to capture the status code and
// bytes written. SSE streaming handlers may write from multiple goroutines
// (e.g. one for control messages, one for the data stream), so the counters
// are atomic to avoid data races under -race.
//
// status is captured only on the first WriteHeader call; subsequent calls are
// ignored to match net/http semantics.
type responseWriter struct {
	http.ResponseWriter
	status       atomic.Int32
	wroteHeader  atomic.Bool
	bytesWritten atomic.Int64
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader.CompareAndSwap(false, true) {
		// HTTP status codes are 100-599 — always within int32 range.
		rw.status.Store(int32(code)) //nolint:gosec // bounded HTTP status code
		rw.ResponseWriter.WriteHeader(code)
		return
	}
	// Header already written — call through so net/http logs its own warning,
	// but do not overwrite the captured status.
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.wroteHeader.CompareAndSwap(false, true) {
		rw.status.Store(int32(http.StatusOK))
		rw.ResponseWriter.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten.Add(int64(n))
	return n, err
}

// Unwrap returns the underlying http.ResponseWriter for http.ResponseController.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Flush implements http.Flusher for SSE support through middleware.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
