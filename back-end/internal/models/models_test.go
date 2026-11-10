package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestUserJSONNeverExposesPasswordHash is the important one: a leaked
// password hash cannot be undone, so we assert the marshalled output
// does not contain it at all.
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
	// The field must be absent, not merely empty.
	if strings.Contains(string(raw), "password_hash") {
		t.Fatalf("password_hash key present in JSON: %s", raw)
	}
}

// TestJSONFieldNames locks the wire contract. Every key here is also a
// column name in migrations/0001_initial.sql, which is the point of the
// naming convention: one name from SQL to JavaScript.
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

// TestConversationOmitsEmptyName checks the omitempty behaviour: a direct
// conversation has no name, and the client should not receive a null
// field it has to special-case.
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

// TestConstantValues pins the strings written to the database. Changing
// one of these silently is a data migration, so the test makes it loud.
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
