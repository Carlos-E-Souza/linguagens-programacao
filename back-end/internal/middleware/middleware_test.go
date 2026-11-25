package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLoggingPassesThroughAndRecordsStatus confere que o middleware não
// muda a resposta: ele só a observa.
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

// TestLoggingDefaultsToOK cobre um handler que escreve sem chamar
// WriteHeader, o que faz o Go assumir 200.
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

// TestRecoverConvertsPanicTo500 é o caso importante: um handler em panic
// não pode derrubar o processo.
func TestRecoverConvertsPanicTo500(t *testing.T) {
	// Silencia o log de panic esperado para a saída do teste ficar legível.
	slog.SetLogLoggerLevel(slog.LevelError + 100)
	t.Cleanup(func() { slog.SetLogLoggerLevel(slog.LevelInfo) })

	handler := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()

	// Se Recover não funcionasse, esta chamada entraria em panic e
	// derrubaria o teste.
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := rec.Code, http.StatusInternalServerError; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
}

// TestRecoverLeavesNormalRequestsAlone protege contra o caminho de
// recuperação disparar quando nada deu errado.
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
