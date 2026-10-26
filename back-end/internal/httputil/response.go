// Package httputil implements the HTTP surface of the server: the REST
// handlers, the /ws upgrade endpoint and the small JSON helpers shared
// by all of them.
package httputil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// maxRequestBody caps how much of a request body we are willing to read.
// Without a limit, a client could stream gigabytes into memory and take
// the server down, so every decoded body goes through MaxBytesReader.
const maxRequestBody = 1 << 20 // 1 MiB

// ErrorBody is the JSON shape of every error response. Using one shape
// for all failures means the client has a single branch to write.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON serializes v as JSON with the given status code.
//
// The body is marshalled before any header is written on purpose: if
// marshalling failed after WriteHeader, we would have already committed
// a 200 and could only send a broken body.
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
		// The client hung up mid-write. Nothing useful is left to do:
		// the status is already sent, so we only log it.
		slog.Debug("failed to write response body", "error", err)
	}
}

// WriteError writes an error response in the standard ErrorBody shape.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Code: code, Message: message})
}

// ReadJSON decodes the request body into dst.
//
// It is strict by design: the body is capped in size, unknown fields are
// rejected (so a client typo fails loudly instead of being ignored) and
// trailing data after the JSON value is treated as an error.
func ReadJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}

	// A well-formed request has exactly one JSON value. Anything after
	// it means the client sent something we do not understand.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON value")
	}

	return nil
}
