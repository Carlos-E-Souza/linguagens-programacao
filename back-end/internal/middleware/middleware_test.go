package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLoggingPassesThroughAndRecordsStatus checks the middleware does
// not change the response: it only observes it.
func TestLoggingPassesThroughAndRecordsStatus(t *testing.T) {
	handler := Logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short and stout"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if got, want := rec.Code, http.StatusTeapot; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
	if got, want := rec.Body.String(), "short and stout"; got != want {
		t.Errorf("body: got %q, want %q", got, want)
	}
}

// TestLoggingDefaultsToOK covers a handler that writes without calling
// WriteHeader, which makes Go imply 200.
func TestLoggingDefaultsToOK(t *testing.T) {
	handler := Logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
}

// TestRecoverConvertsPanicTo500 is the important one: a panicking
// handler must not take down the process.
func TestRecoverConvertsPanicTo500(t *testing.T) {
	// Silence the expected panic log so the test output stays readable.
	slog.SetLogLoggerLevel(slog.LevelError + 100)
	t.Cleanup(func() { slog.SetLogLoggerLevel(slog.LevelInfo) })

	handler := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()

	// If Recover did not work, this call would panic and fail the test.
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := rec.Code, http.StatusInternalServerError; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
}

// TestRecoverLeavesNormalRequestsAlone guards against the recovery path
// firing when nothing went wrong.
func TestRecoverLeavesNormalRequestsAlone(t *testing.T) {
	handler := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := rec.Code, http.StatusNoContent; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
}
