package metrics

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/protocol"
)

var t0 = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

func msg(clientID string, conv, sender, seq int64) protocol.Message {
	return protocol.Message{ClientID: clientID, ConversationID: conv, SenderID: sender, Sequence: seq}
}

func TestRecorderHappyPath(t *testing.T) {
	r := NewRecorder(t0)
	r.Sent("c1", 1, "group", 1, []int64{2, 3}, t0)
	r.Acked("c1", t0.Add(5*time.Millisecond))
	r.Received(2, msg("c1", 1, 1, 1), t0.Add(10*time.Millisecond))
	r.Received(3, msg("c1", 1, 1, 1), t0.Add(20*time.Millisecond))

	s := r.Finalize()
	if s.Sent != 1 || s.Acked != 1 || s.Delivered != 2 || s.ExpectedDeliveries != 2 || s.MissingDeliveries != 0 || s.MissingAcks != 0 {
		t.Fatalf("contagens inesperadas: %+v", s)
	}
	if s.Ack.P50 != 5*time.Millisecond {
		t.Fatalf("ACK p50 = %v", s.Ack.P50)
	}
	if s.DeliveryGroup.Max != 20*time.Millisecond || s.DeliveryGroup.Count != 2 {
		t.Fatalf("entrega em grupo inesperada: %+v", s.DeliveryGroup)
	}
	if s.DeliveryDirect.Count != 0 {
		t.Fatalf("entrega direta inesperada: %+v", s.DeliveryDirect)
	}
}

func TestRecorderSeparatesKinds(t *testing.T) {
	r := NewRecorder(t0)
	r.Sent("d1", 5, "direct", 1, []int64{2}, t0)
	r.Received(2, msg("d1", 5, 1, 1), t0.Add(7*time.Millisecond))
	s := r.Finalize()
	if s.DeliveryDirect.Count != 1 || s.DeliveryDirect.Max != 7*time.Millisecond || s.DeliveryGroup.Count != 0 {
		t.Fatalf("separação por tipo falhou: direct=%+v group=%+v", s.DeliveryDirect, s.DeliveryGroup)
	}
}

func TestRecorderIgnoresSenderEcho(t *testing.T) {
	r := NewRecorder(t0)
	r.Sent("c1", 1, "group", 1, []int64{2}, t0)
	r.Received(1, msg("c1", 1, 1, 1), t0.Add(time.Millisecond))
	r.Received(1, msg("c1", 1, 1, 1), t0.Add(2*time.Millisecond))
	s := r.Finalize()
	if s.Delivered != 0 || s.Duplicates != 0 || s.OrderViolations != 0 {
		t.Fatalf("eco do remetente contado: %+v", s)
	}
}

func TestRecorderIgnoresUnknownClientID(t *testing.T) {
	r := NewRecorder(t0)
	r.Received(2, msg("de-outra-execucao", 1, 9, 1), t0)
	r.Acked("de-outra-execucao", t0)
	s := r.Finalize()
	if s.Delivered != 0 || s.Acked != 0 || s.Duplicates != 0 {
		t.Fatalf("client_id desconhecido contado: %+v", s)
	}
}

func TestRecorderDuplicateAndOrder(t *testing.T) {
	r := NewRecorder(t0)
	r.Sent("a", 1, "group", 1, []int64{2}, t0)
	r.Sent("b", 1, "group", 1, []int64{2}, t0)
	r.Received(2, msg("a", 1, 1, 5), t0.Add(time.Millisecond))
	r.Received(2, msg("a", 1, 1, 5), t0.Add(2*time.Millisecond))
	r.Received(2, msg("b", 1, 1, 4), t0.Add(3*time.Millisecond))
	s := r.Finalize()
	if s.Duplicates != 1 {
		t.Fatalf("duplicatas = %d, esperado 1", s.Duplicates)
	}
	if s.OrderViolations != 1 {
		t.Fatalf("violações de ordem = %d, esperado 1", s.OrderViolations)
	}
	if s.Delivered != 2 {
		t.Fatalf("entregues = %d, esperado 2", s.Delivered)
	}
}

func TestRecorderFinalizeCountsMissing(t *testing.T) {
	r := NewRecorder(t0)
	r.Sent("c1", 1, "group", 1, []int64{2, 3}, t0)
	r.Received(2, msg("c1", 1, 1, 1), t0.Add(time.Millisecond))
	s := r.Finalize()
	if s.MissingAcks != 1 || s.MissingDeliveries != 1 || s.ExpectedDeliveries != 2 {
		t.Fatalf("faltantes inesperados: %+v", s)
	}
}

func TestRecorderCounters(t *testing.T) {
	r := NewRecorder(t0)
	r.Error("not_member")
	r.Error("not_member")
	r.Status("read")
	r.Disconnected(4)
	s := r.Finalize()
	if s.Errors["not_member"] != 2 || s.Statuses["read"] != 1 || s.Disconnects != 1 {
		t.Fatalf("contadores inesperados: %+v", s)
	}
}

func TestRecorderTimeline(t *testing.T) {
	r := NewRecorder(t0)
	r.Sent("a", 1, "direct", 1, []int64{2}, t0.Add(500*time.Millisecond))
	r.Sent("b", 1, "direct", 1, []int64{2}, t0.Add(1500*time.Millisecond))
	r.Acked("b", t0.Add(1600*time.Millisecond))
	r.Received(2, msg("b", 1, 1, 1), t0.Add(2100*time.Millisecond))
	s := r.Finalize()
	if len(s.Timeline) != 3 {
		t.Fatalf("timeline com %d baldes, esperado 3: %+v", len(s.Timeline), s.Timeline)
	}
	if s.Timeline[0].Sent != 1 || s.Timeline[1].Sent != 1 || s.Timeline[1].Acked != 1 || s.Timeline[2].Delivered != 1 {
		t.Fatalf("timeline inesperada: %+v", s.Timeline)
	}
	for i, b := range s.Timeline {
		if b.Second != i {
			t.Fatalf("balde %d com Second=%d", i, b.Second)
		}
	}
}

func TestRecorderConcurrent(t *testing.T) {
	r := NewRecorder(t0)
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				id := fmt.Sprintf("%d-%d", g, i)
				at := t0.Add(time.Duration(i) * time.Millisecond)
				r.Sent(id, int64(g), "direct", int64(g), []int64{1000}, at)
				r.Acked(id, at)
				r.Received(1000, msg(id, int64(g), int64(g), int64(i+1)), at)
			}
		}(g)
	}
	wg.Wait()
	s := r.Finalize()
	if s.Sent != 5000 || s.Acked != 5000 || s.Delivered != 5000 || s.OrderViolations != 0 {
		t.Fatalf("contagens concorrentes inesperadas: %+v", s)
	}
}
