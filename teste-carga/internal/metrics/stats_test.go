package metrics

import (
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	samples := make([]time.Duration, 0, 100)
	for i := 100; i >= 1; i-- { // fora de ordem de propósito
		samples = append(samples, time.Duration(i)*time.Millisecond)
	}
	got := Summarize(samples)
	if got.Count != 100 || got.P50 != 50*time.Millisecond || got.P99 != 99*time.Millisecond || got.Max != 100*time.Millisecond {
		t.Fatalf("resumo inesperado: %+v", got)
	}
	if got.Mean != 50500*time.Microsecond {
		t.Fatalf("média = %v, esperado 50.5ms", got.Mean)
	}
	if samples[0] != 100*time.Millisecond {
		t.Fatal("Summarize alterou a ordem da entrada")
	}
	if (Summarize(nil) != Latency{}) {
		t.Fatal("entrada vazia deve gerar Latency zero")
	}
}
