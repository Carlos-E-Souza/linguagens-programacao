package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// httptest.NewRecorder is the Go way to test a handler without a real
// network: it implements http.ResponseWriter and records what was
// written, so we can assert on status, headers and body.
func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteJSON(rec, http.StatusCreated, map[string]string{"username": "gabriel"})

	if got, want := rec.Code, http.StatusCreated; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Errorf("Content-Type: got %q, want %q", got, want)
	}
	if got, want := rec.Body.String(), `{"username":"gabriel"}`; got != want {
		t.Errorf("body: got %q, want %q", got, want)
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteError(rec, http.StatusForbidden, "not_a_member", "you are not a member of this conversation")

	if got, want := rec.Code, http.StatusForbidden; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}

	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if got, want := body.Code, "not_a_member"; got != want {
		t.Errorf("code: got %q, want %q", got, want)
	}
	if body.Message == "" {
		t.Error("message must not be empty")
	}
}

func TestReadJSON(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"valid", `{"username":"gabriel","password":"secret123"}`, false},
		{"unknown field", `{"username":"gabriel","typo":"x"}`, true},
		{"malformed json", `{"username":`, true},
		{"two values", `{"username":"a"} {"username":"b"}`, true},
		{"empty body", ``, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			var dst struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			err := ReadJSON(rec, req, &dst)

			if (err != nil) != tc.wantErr {
				t.Errorf("ReadJSON() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestReadJSONRejectsOversizedBody proves the MaxBytesReader cap works.
func TestReadJSONRejectsOversizedBody(t *testing.T) {
	huge := `{"username":"` + strings.Repeat("a", maxRequestBody+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(huge))
	rec := httptest.NewRecorder()

	var dst struct {
		Username string `json:"username"`
	}
	if err := ReadJSON(rec, req, &dst); err == nil {
		t.Fatal("expected an error for a body larger than the limit")
	}
}
