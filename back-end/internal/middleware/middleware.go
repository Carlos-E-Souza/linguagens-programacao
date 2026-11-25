package middleware

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/httputil"
)

// statusRecorder envolve http.ResponseWriter para lembrar o código de
// status.
//
// http.ResponseWriter não expõe o status recebido, então um middleware de
// log não consegue saber se uma requisição terminou em 200 ou 500 sem
// interceptar o próprio WriteHeader. Embutir a interface faz todos os
// outros métodos serem repassados sem mudança.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader registra o status e o repassa ao writer real.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Write cobre handlers que nunca chamam WriteHeader explicitamente: nesse
// caso o Go assume 200 na primeira escrita.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Hijack repassa a tomada da conexão de que um upgrade WebSocket precisa.
//
// Embutir http.ResponseWriter não basta sozinho: o valor embutido é a
// *interface*, que não anuncia Hijack, então websocket.Accept não
// encontraria um http.Hijacker e falharia com 501. Repassar
// explicitamente mantém o upgrade funcionando e ainda registra o status
// das requisições comuns.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

// Flush repassa os flushes de streaming, pelo mesmo motivo de Hijack.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Logging registra uma linha por requisição com método, caminho, status e
// duração. A duração é a primeira coisa a olhar quando algo parece lento,
// então é medida aqui em vez de ficar no palpite.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		// Um handler que não escreveu nada ainda assim terminou com 200.
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

// Recover transforma um panic num handler numa resposta 500 em vez de uma
// conexão derrubada e um processo encerrado.
//
// A recuperação precisa acontecer numa função adiada (defer): recover()
// só interrompe um panic quando chamado diretamente de uma chamada adiada
// na goroutine que entrou em panic. Um panic em outra goroutine não pode
// ser capturado aqui, e por isso o hub e os workers de persistência fazem
// a própria recuperação.
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
