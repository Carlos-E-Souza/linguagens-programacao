package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

// Os frames abaixo reproduzem literalmente o formato enviado pelo servidor
// (back-end/internal/ws). Se o protocolo mudar, este teste deve quebrar.
func TestDecodeServerFrames(t *testing.T) {
	ack := `{"type":"message.ack","data":{"client_id":"c1","id":7,"conversation_id":3,"sequence":12,"created_at":"2026-11-25T10:00:00Z"}}`
	var env Envelope
	if err := json.Unmarshal([]byte(ack), &env); err != nil {
		t.Fatalf("decodificar envelope: %v", err)
	}
	if env.Type != TypeMessageAck {
		t.Fatalf("tipo = %q, esperado %q", env.Type, TypeMessageAck)
	}
	var a AckPayload
	if err := json.Unmarshal(env.Data, &a); err != nil {
		t.Fatalf("decodificar ack: %v", err)
	}
	if a.ClientID != "c1" || a.ID != 7 || a.ConversationID != 3 || a.Sequence != 12 {
		t.Fatalf("ack inesperado: %+v", a)
	}

	newMsg := `{"type":"message.new","data":{"id":7,"conversation_id":3,"sequence":12,"sender_id":5,"client_id":"c1","body":"x","created_at":"2026-11-25T10:00:00Z"}}`
	if err := json.Unmarshal([]byte(newMsg), &env); err != nil {
		t.Fatalf("decodificar envelope: %v", err)
	}
	if env.Type != TypeMessageNew {
		t.Fatalf("tipo = %q, esperado %q", env.Type, TypeMessageNew)
	}
	var m Message
	if err := json.Unmarshal(env.Data, &m); err != nil {
		t.Fatalf("decodificar mensagem: %v", err)
	}
	want := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	if m.SenderID != 5 || m.ClientID != "c1" || m.Sequence != 12 || m.Body != "x" || !m.CreatedAt.Equal(want) {
		t.Fatalf("mensagem inesperada: %+v", m)
	}

	errFrame := `{"type":"error","data":{"code":"not_member","message":"x","client_id":"c9"}}`
	if err := json.Unmarshal([]byte(errFrame), &env); err != nil {
		t.Fatalf("decodificar envelope: %v", err)
	}
	var e ErrorPayload
	if err := json.Unmarshal(env.Data, &e); err != nil || e.Code != "not_member" || e.ClientID != "c9" {
		t.Fatalf("erro inesperado: %+v (%v)", e, err)
	}

	status := `{"type":"message.status","data":{"conversation_id":3,"user_id":4,"up_to_sequence":12,"status":"read"}}`
	if err := json.Unmarshal([]byte(status), &env); err != nil {
		t.Fatalf("decodificar envelope: %v", err)
	}
	var s StatusPayload
	if err := json.Unmarshal(env.Data, &s); err != nil || s.Status != "read" || s.UpToSequence != 12 || s.UserID != 4 {
		t.Fatalf("status inesperado: %+v (%v)", s, err)
	}
}

func TestEncodeSend(t *testing.T) {
	env, err := NewEnvelope(TypeMessageSend, SendPayload{ClientID: "c1", ConversationID: 3, Body: "oi"})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	got, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"type":"message.send","data":{"client_id":"c1","conversation_id":3,"body":"oi"}}`
	if string(got) != want {
		t.Fatalf("frame = %s\nesperado %s", got, want)
	}
}

func TestEncodeReceipt(t *testing.T) {
	env, err := NewEnvelope(TypeMessageRead, ReceiptPayload{ConversationID: 3, UpToSequence: 12})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	got, _ := json.Marshal(env)
	want := `{"type":"message.read","data":{"conversation_id":3,"up_to_sequence":12}}`
	if string(got) != want {
		t.Fatalf("frame = %s\nesperado %s", got, want)
	}
}
