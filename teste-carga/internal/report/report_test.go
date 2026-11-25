package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/metrics"
	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/profiler"
)

func sampleResult() Result {
	ms := time.Millisecond
	return Result{
		RunID:     "20261125-100000",
		StartedAt: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
		Config: Config{
			Prefix: "carga", UsersCreated: 0, GroupsCreated: 0,
			ServerURL: "http://localhost:8080", Users: 100, DirectConversations: 300, Groups: 6,
			GroupSizes: []int{5, 5, 10, 10, 20, 50}, Rate: 1, Duration: 10 * time.Second,
			Ramp: time.Second, Drain: time.Second, Receipts: true, Seed: 1,
		},
		Summary: metrics.Summary{
			Sent: 1000, Acked: 1000, ExpectedDeliveries: 3000, Delivered: 3000,
			Ack:            metrics.Latency{Count: 1000, P50: 2 * ms, P90: 4 * ms, P95: 5 * ms, P99: 9 * ms, Max: 20 * ms, Mean: 3 * ms},
			DeliveryDirect: metrics.Latency{Count: 1500, P50: 3 * ms, P99: 12500 * time.Microsecond, Max: 30 * ms},
			DeliveryGroup:  metrics.Latency{Count: 1500, P50: 4 * ms, P99: 15 * ms, Max: 40 * ms},
			Errors:         map[string]int{"not_member": 2},
			Statuses:       map[string]int{"read": 10, "delivered": 12},
			Timeline: []metrics.Bucket{
				{Second: 0, Sent: 100, Acked: 99, Delivered: 300},
				{Second: 6, Sent: 100, Acked: 100, Delivered: 300},
			},
		},
		Server: []profiler.Sample{
			{At: time.Second, Goroutines: 220, HeapAllocBytes: 8 << 20, HeapInuseBytes: 10 << 20},
			{At: 2 * time.Second, Goroutines: 240, HeapAllocBytes: 12 << 20, HeapInuseBytes: 14 << 20},
		},
		PprofAvailable: true,
		ProfileFiles:   []string{"cpu.pprof", "heap.pprof"},
	}
}

func TestMarkdownSections(t *testing.T) {
	md := Markdown(sampleResult())
	titles := []string{
		"# Relatório de teste de carga — 20261125-100000",
		"## Resumo",
		"## Latência",
		"## Erros e status",
		"## Servidor (pprof)",
		"## Linha do tempo",
	}
	pos := -1
	for _, title := range titles {
		i := strings.Index(md, title)
		if i < 0 || i < pos {
			t.Fatalf("título %q ausente ou fora de ordem\n%s", title, md)
		}
		pos = i
	}
	for _, want := range []string{
		"p99", "100.0%", "100.0 msg/s", "12.5", "Entrega (direta)", "Entrega (grupo)",
		"not_member", "240", "12.0 MiB", "go tool pprof -top cpu.pprof", "reaproveitados",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("relatório sem %q\n%s", want, md)
		}
	}
	// segundos 0 e 6 caem nas janelas 0–5 s e 5–10 s
	if !strings.Contains(md, "| 0–5 s | 100 | 99 | 300 |") || !strings.Contains(md, "| 5–10 s | 100 | 100 | 300 |") {
		t.Fatalf("linha do tempo inesperada\n%s", md)
	}
}

func TestMarkdownNoPprof(t *testing.T) {
	r := sampleResult()
	r.PprofAvailable, r.Server, r.ProfileFiles = false, nil, nil
	if md := Markdown(r); !strings.Contains(md, "pprof indisponível") {
		t.Fatalf("faltou aviso de pprof\n%s", md)
	}
}

func TestMarkdownZeroDivision(t *testing.T) {
	md := Markdown(Result{RunID: "x"})
	if strings.Contains(md, "NaN") || strings.Contains(md, "Inf") {
		t.Fatalf("divisão por zero no relatório\n%s", md)
	}
}

func TestWriteFiles(t *testing.T) {
	dir := t.TempDir()
	want := sampleResult()
	if err := Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("report.json inválido: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip diferente:\n%+v\n%+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "report.md")); err != nil {
		t.Fatalf("report.md ausente: %v", err)
	}
}

func TestMarkdownInterrupted(t *testing.T) {
	r := sampleResult()
	r.Interrupted = true
	if md := Markdown(r); !strings.Contains(md, "Execução interrompida") {
		t.Fatalf("faltou aviso de interrupção\n%s", md)
	}
	if md := Markdown(sampleResult()); strings.Contains(md, "Execução interrompida") {
		t.Fatal("aviso de interrupção em execução completa")
	}
}
