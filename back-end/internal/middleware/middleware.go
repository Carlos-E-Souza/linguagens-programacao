package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/httputil"
)

// statusRecorder wraps http.ResponseWriter to remember the status code.
//
// http.ResponseWriter does not expose the status it was given, so a
// logging middleware cannot know whether a request ended in 200 or 500
// without intercepting WriteHeader itself. Embedding the interface
// means every other method is forwarded unchanged.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status and forwards it to the real writer.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Write covers handlers that never call WriteHeader explicitly: Go then
// implies 200 on the first write.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Logging records one line per request with method, path, status and
// duration. Duration is the first thing to look at when something feels
// slow, so it is measured here rather than left to guesswork.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		// A handler that wrote nothing at all still completed as 200.
		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}

// Recover turns a panic in a handler into a 500 response instead of a
// dropped connection and a crashed process.
//
// The recovery has to happen in a deferred function: recover() only
// stops a panic when called directly from a deferred call on the
// panicking goroutine. A panic in a different goroutine cannot be
// caught here, which is why the hub and the persistence workers recover
// on their own.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered",
					"error", rec,
					"method", r.Method,
					"path", r.URL.Path,
				)
				httputil.WriteError(w, http.StatusInternalServerError,
					"internal_error", "internal server error")
			}
		}()

		next.ServeHTTP(w, r)
	})
}
