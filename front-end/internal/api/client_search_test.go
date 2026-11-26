package api

import (
	"context"
	"net/http"
	"testing"
)

func TestSearchMessagesSendsTermAndCursor(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/conversations/7/messages/search" {
			t.Errorf("requisição inesperada: %s %s", r.Method, r.URL.Path)
		}
		query := r.URL.Query()
		if got := query.Get("q"); got != "reunião às 15h" {
			t.Errorf("q = %q, esperava o termo decodificado", got)
		}
		if got := query.Get("before"); got != "40" {
			t.Errorf("before = %q, esperava 40", got)
		}
		if got := query.Get("limit"); got != "20" {
			t.Errorf("limit = %q, esperava 20", got)
		}
		w.Write([]byte(`[{"id":1,"conversation_id":7,"sequence":39,"sender_id":2,"client_id":"c","body":"reunião às 15h"}]`))
	})
	client.SetToken("tok")

	messages, err := client.SearchMessages(context.Background(), 7, "reunião às 15h", 40, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Sequence != 39 {
		t.Errorf("mensagens inesperadas: %+v", messages)
	}
}

func TestSearchMessagesOmitsCursorAndLimitWhenZero(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Has("before") || query.Has("limit") {
			t.Errorf("não deveria enviar before nem limit: %q", r.URL.RawQuery)
		}
		w.Write([]byte(`[]`))
	})

	messages, err := client.SearchMessages(context.Background(), 7, "oi", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Errorf("esperava lista vazia, veio %+v", messages)
	}
}
