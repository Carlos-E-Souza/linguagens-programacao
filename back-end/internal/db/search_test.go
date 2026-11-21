package db

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

func TestSearchMessages(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	ana := mustCreateUser(t, s, "ana")
	bob := mustCreateUser(t, s, "bob")
	eve := mustCreateUser(t, s, "eve")
	conv := mustCreateDirect(t, s, ana, bob)
	other := mustCreateDirect(t, s, ana, eve)

	now := time.Now().UTC()
	bodies := []string{"Bom dia", "Reunião às 15h", "preço: 100% garantido", "arquivo_final.pdf", "reunião adiada"}
	var batch []models.Message
	for i, body := range bodies {
		batch = append(batch, models.Message{
			ConversationID: conv.ID, Sequence: int64(i + 1), SenderID: ana.ID,
			ClientID: fmt.Sprintf("c%d", i+1), Body: body, CreatedAt: now,
		})
	}
	batch = append(batch, models.Message{
		ConversationID: other.ID, Sequence: 1, SenderID: ana.ID,
		ClientID: "other-1", Body: "reunião secreta", CreatedAt: now,
	})
	if _, err := s.InsertMessages(ctx, batch); err != nil {
		t.Fatalf("InsertMessages() failed: %v", err)
	}

	cases := []struct {
		name   string
		term   string
		before int64
		limit  int
		want   []int64
	}{
		{"substring, newest first", "reunião", 0, 20, []int64{5, 2}},
		{"ASCII is case-insensitive", "BOM", 0, 20, []int64{1}},
		{"percent is literal", "100%", 0, 20, []int64{3}},
		{"underscore is literal, not a wildcard", "arq_ivo", 0, 20, nil},
		{"never reads another conversation", "secreta", 0, 20, nil},
		{"before is an exclusive cursor", "reunião", 5, 20, []int64{2}},
		{"limit", "reunião", 0, 1, []int64{5}},
		{"no match", "zzzz", 0, 20, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.SearchMessages(ctx, conv.ID, tc.term, tc.before, tc.limit)
			if err != nil {
				t.Fatalf("SearchMessages() failed: %v", err)
			}
			var sequences []int64
			for _, m := range got {
				sequences = append(sequences, m.Sequence)
			}
			if !slices.Equal(sequences, tc.want) {
				t.Errorf("sequences = %v, want %v", sequences, tc.want)
			}
		})
	}
}
