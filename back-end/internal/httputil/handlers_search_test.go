package httputil

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

func TestNormalizeSearchTerm(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr error
	}{
		{"plain", "reunião", "reunião", nil},
		{"trims edges", "  oi mundo  ", "oi mundo", nil},
		{"collapses inner whitespace", "oi \t\n  mundo", "oi mundo", nil},
		{"nbsp counts as whitespace", "oi  mundo", "oi mundo", nil},
		{"empty", "", "", errSearchTermTooShort},
		{"only spaces", "   \t ", "", errSearchTermTooShort},
		{"one rune is too short", "a", "", errSearchTermTooShort},
		{"one multibyte rune is too short", "ã", "", errSearchTermTooShort},
		{"two runes is the minimum", "ão", "ão", nil},
		{"limit counts runes, not bytes", strings.Repeat("ã", maxSearchTermRunes), strings.Repeat("ã", maxSearchTermRunes), nil},
		{"one rune over the limit", strings.Repeat("ã", maxSearchTermRunes+1), "", errSearchTermTooLong},
		{"invalid utf-8", "ab\xff\xfe", "", errSearchTermInvalid},
		{"wildcards are kept literal here", "100%_ok", "100%_ok", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeSearchTerm(tc.raw)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("normalizeSearchTerm(%q) error = %v, want %v", tc.raw, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("normalizeSearchTerm(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSearchMessagesEndpoint(t *testing.T) {
	f := newFixture(t)
	alice := f.register("alice")
	bob := f.register("bob")
	f.register("eve")
	aliceToken := f.login("alice")
	eveToken := f.login("eve")

	rec := f.do(http.MethodPost, "/api/conversations", aliceToken, createConversationRequest{
		Type: models.ConversationDirect, Members: []int64{bob.ID},
	})
	var conv models.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}

	now := time.Now().UTC()
	if _, err := f.store.InsertMessages(context.Background(), []models.Message{
		{ConversationID: conv.ID, Sequence: 1, SenderID: alice.ID, ClientID: "c1", Body: "Bom dia", CreatedAt: now},
		{ConversationID: conv.ID, Sequence: 2, SenderID: bob.ID, ClientID: "c2", Body: "Reunião às 15h", CreatedAt: now},
		{ConversationID: conv.ID, Sequence: 3, SenderID: alice.ID, ClientID: "c3", Body: "reunião adiada", CreatedAt: now},
	}); err != nil {
		t.Fatalf("InsertMessages() failed: %v", err)
	}

	path := "/api/conversations/" + itoa(conv.ID) + "/messages/search"

	rec = f.do(http.MethodGet, path+"?q=reuni%C3%A3o", aliceToken, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d (%s)", got, want, rec.Body)
	}
	var found []models.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &found); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	if len(found) != 2 || found[0].Sequence != 3 || found[1].Sequence != 2 {
		t.Errorf("results = %+v, want sequences [3 2]", found)
	}

	// Resultado vazio é "[]", nunca "null": o cliente itera sobre ele.
	rec = f.do(http.MethodGet, path+"?q=zzzz", aliceToken, nil)
	if got := rec.Body.String(); got != "[]\n" && got != "[]" {
		t.Errorf("empty result body = %q, want []", got)
	}

	// Quem não é membro não pode descobrir que a conversa existe.
	rec = f.do(http.MethodGet, path+"?q=reuni%C3%A3o", eveToken, nil)
	if got, want := rec.Code, http.StatusNotFound; got != want {
		t.Errorf("non-member status: got %d, want %d", got, want)
	}

	// Entrada inválida é erro do cliente.
	for _, query := range []string{"", "?q=a", "?q=%20%20", "?q=oi&before=abc", "?q=oi&limit=-1"} {
		rec = f.do(http.MethodGet, path+query, aliceToken, nil)
		if got, want := rec.Code, http.StatusBadRequest; got != want {
			t.Errorf("query %q status: got %d, want %d (%s)", query, got, want, rec.Body)
		}
	}

	// Um limit acima do máximo é reduzido, não obedecido.
	rec = f.do(http.MethodGet, path+"?q=oi&limit=99999", aliceToken, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("clamped limit status: got %d, want %d (%s)", got, want, rec.Body)
	}
}
