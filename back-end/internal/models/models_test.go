package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestUserJSONNeverExposesPasswordHash é o caso importante: um hash de
// senha vazado não tem volta, então conferimos que a saída serializada
// não o contém de jeito nenhum.
func TestUserJSONNeverExposesPasswordHash(t *testing.T) {
	user := User{
		ID:           1,
		Username:     "gabriel",
		DisplayName:  "Gabriel",
		PasswordHash: "$2a$10$super-secret-hash",
		CreatedAt:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	raw, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("Marshal() failed: %v", err)
	}

	if strings.Contains(string(raw), "super-secret-hash") {
		t.Fatalf("password hash leaked into JSON: %s", raw)
	}
	// O campo precisa estar ausente, não apenas vazio.
	if strings.Contains(string(raw), "password_hash") {
		t.Fatalf("password_hash key present in JSON: %s", raw)
	}
}

// TestJSONFieldNames trava o contrato de transmissão. Toda chave aqui
// também é nome de coluna em migrations/0001_initial.sql, que é o sentido
// da convenção de nomes: um só nome do SQL ao JavaScript.
func TestJSONFieldNames(t *testing.T) {
	msg := Message{
		ID:             7,
		ConversationID: 3,
		Sequence:       42,
		SenderID:       1,
		ClientID:       "abc-123",
		Body:           "hello",
		CreatedAt:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal() failed: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() failed: %v", err)
	}

	want := []string{
		"id", "conversation_id", "sequence", "sender_id",
		"client_id", "body", "created_at",
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("missing JSON key %q in %s", key, raw)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected key count: got %d, want %d (%s)", len(got), len(want), raw)
	}
}

// TestConversationOmitsEmptyName confere o comportamento de omitempty: uma
// conversa individual não tem nome, e o cliente não deve receber um campo
// null que precise tratar como caso especial.
func TestConversationOmitsEmptyName(t *testing.T) {
	conv := Conversation{
		ID:        1,
		Type:      ConversationDirect,
		CreatedBy: 1,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	raw, err := json.Marshal(conv)
	if err != nil {
		t.Fatalf("Marshal() failed: %v", err)
	}
	if strings.Contains(string(raw), `"name"`) {
		t.Errorf("empty name should be omitted, got: %s", raw)
	}
}

// TestConstantValues fixa as strings gravadas no banco. Mudar uma delas em
// silêncio é uma migração de dados, então o teste torna isso visível.
func TestConstantValues(t *testing.T) {
	cases := map[string]string{
		"direct":    ConversationDirect,
		"group":     ConversationGroup,
		"admin":     RoleAdmin,
		"member":    RoleMember,
		"pending":   StatusPending,
		"sent":      StatusSent,
		"delivered": StatusDelivered,
		"read":      StatusRead,
		"failed":    StatusFailed,
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}
