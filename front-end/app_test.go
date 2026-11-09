package main

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

func seqs(values ...int64) []api.Message {
	messages := make([]api.Message, len(values))
	for i, v := range values {
		messages[i] = api.Message{Sequence: v}
	}
	return messages
}

func TestPageComplete(t *testing.T) {
	cases := []struct {
		name     string
		messages []api.Message
		upper    int64
		limit    int
		want     bool
	}{
		{"conversa vazia", nil, 1, 50, true},
		{"página cheia", seqs(8, 9, 10), 11, 3, true},
		{"chega na primeira", seqs(1, 2, 3), 4, 50, true},
		{"falta a mais recente", seqs(1, 2), 4, 50, false},
		{"buraco no meio", seqs(1, 3, 4), 5, 50, false},
		{"incompleta sem chegar na primeira", seqs(5, 6), 7, 50, false},
		{"pendente no fim é ignorada", seqs(1, 2, 0), 3, 50, true},
	}
	for _, tc := range cases {
		if got := pageComplete(tc.messages, tc.upper, tc.limit); got != tc.want {
			t.Errorf("%s: pageComplete = %v, esperava %v", tc.name, got, tc.want)
		}
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	token := func(exp int64) string {
		payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"1","exp":%d}`, exp)))
		return "h." + payload + ".s"
	}
	if tokenExpired(token(now.Unix()+60), now) {
		t.Error("token válido tratado como expirado")
	}
	if !tokenExpired(token(now.Unix()-1), now) {
		t.Error("token vencido tratado como válido")
	}
	if tokenExpired("ilegivel", now) {
		t.Error("token ilegível deve ficar para o servidor decidir")
	}
}
