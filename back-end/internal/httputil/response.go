// Pacote httputil implementa a superfície HTTP do servidor: os handlers
// REST, o endpoint de upgrade /ws e os pequenos helpers de JSON usados por
// todos eles.
package httputil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// maxRequestBody limita quanto do corpo de uma requisição aceitamos ler.
// Sem limite, um cliente poderia despejar gigabytes na memória e derrubar
// o servidor, então todo corpo decodificado passa por MaxBytesReader.
const maxRequestBody = 1 << 20 // 1 MiB

// ErrorBody é o formato JSON de toda resposta de erro. Um formato único
// para todas as falhas significa que o cliente só tem um caso a tratar.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON serializa v como JSON com o código de status informado.
//
// O corpo é serializado antes de qualquer header ser escrito de propósito:
// se a serialização falhasse depois de WriteHeader, já teríamos enviado um
// 200 e só restaria mandar um corpo quebrado.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		slog.Error("failed to marshal JSON response", "error", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "failed to encode response")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if _, err := w.Write(raw); err != nil {
		// O cliente desligou no meio da escrita. Não há mais nada útil a
		// fazer: o status já foi enviado, então só registramos no log.
		slog.Debug("failed to write response body", "error", err)
	}
}

// WriteError escreve uma resposta de erro no formato padrão ErrorBody.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Code: code, Message: message})
}

// ReadJSON decodifica o corpo da requisição em dst.
//
// É estrito por projeto: o corpo tem tamanho limitado, campos
// desconhecidos são rejeitados (um erro de digitação do cliente falha de
// forma visível em vez de ser ignorado) e dados após o valor JSON contam
// como erro.
func ReadJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}

	// Uma requisição bem formada tem exatamente um valor JSON. Qualquer
	// coisa depois dele significa que o cliente mandou algo que não
	// entendemos.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON value")
	}

	return nil
}
