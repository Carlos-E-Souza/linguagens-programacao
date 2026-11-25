// Pacote metrics registra o que acontece com cada mensagem do teste de carga
// (envio, ACK, entregas) e resume tudo em contagens e distribuições de latência.
package metrics

import (
	"slices"
	"time"
)

// Latency resume uma distribuição de latências. No JSON as durações saem em
// nanossegundos (tipo time.Duration).
type Latency struct {
	Count int           `json:"count"`
	P50   time.Duration `json:"p50_ns"`
	P90   time.Duration `json:"p90_ns"`
	P95   time.Duration `json:"p95_ns"`
	P99   time.Duration `json:"p99_ns"`
	Max   time.Duration `json:"max_ns"`
	Mean  time.Duration `json:"mean_ns"`
}

// Summarize calcula os percentis por índice sobre uma cópia ordenada.
func Summarize(samples []time.Duration) Latency {
	n := len(samples)
	if n == 0 {
		return Latency{}
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)

	var total time.Duration
	for _, d := range sorted {
		total += d
	}
	at := func(p float64) time.Duration { return sorted[int(float64(n-1)*p)] }
	return Latency{
		Count: n,
		P50:   at(0.50),
		P90:   at(0.90),
		P95:   at(0.95),
		P99:   at(0.99),
		Max:   sorted[n-1],
		Mean:  total / time.Duration(n),
	}
}
