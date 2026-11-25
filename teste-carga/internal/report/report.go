// Pacote report grava o resultado de uma execução em report.json (dados
// brutos) e report.md (resumo legível em português).
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/metrics"
	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/profiler"
)

// timelineWindow agrega a linha do tempo no relatório em Markdown.
const timelineWindow = 5

// Config é a configuração usada na execução, como aparece no relatório.
type Config struct {
	Prefix              string        `json:"prefix"`
	UsersCreated        int           `json:"users_created"`
	GroupsCreated       int           `json:"groups_created"`
	ServerURL           string        `json:"server_url"`
	Users               int           `json:"users"`
	DirectConversations int           `json:"direct_conversations"`
	Groups              int           `json:"groups"`
	GroupSizes          []int         `json:"group_sizes"`
	Rate                float64       `json:"rate"`
	Duration            time.Duration `json:"duration_ns"`
	Ramp                time.Duration `json:"ramp_ns"`
	Drain               time.Duration `json:"drain_ns"`
	Receipts            bool          `json:"receipts"`
	Seed                int64         `json:"seed"`
}

// Result é tudo o que uma execução produziu.
type Result struct {
	RunID          string            `json:"run_id"`
	StartedAt      time.Time         `json:"started_at"`
	Config         Config            `json:"config"`
	Summary        metrics.Summary   `json:"summary"`
	Server         []profiler.Sample `json:"server"`
	PprofAvailable bool              `json:"pprof_available"`
	Interrupted    bool              `json:"interrupted"`
	ProfileFiles   []string          `json:"profile_files"`
}

// Write grava report.json e report.md em dir (que já deve existir).
func Write(dir string, res Result) error {
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(Markdown(res)), 0o644)
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond))
}

func percent(part, total int) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(part)/float64(total))
}

func mib(b uint64) string {
	return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
}

// Markdown monta o relatório legível.
func Markdown(res Result) string {
	var b strings.Builder
	c, s := res.Config, res.Summary
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	line("# Relatório de teste de carga — %s", res.RunID)
	line("")
	if res.Interrupted {
		line("> **Execução interrompida:** a carga foi cancelada antes do fim e a drenagem foi pulada; mensagens recentes aparecem como sem ACK ou perdidas.")
		line("")
	}
	line("- Início: %s", res.StartedAt.Format("2006-01-02 15:04:05 MST"))
	line("- Servidor: %s", c.ServerURL)
	line("- Usuários: %d (prefixo `%s`) · conversas diretas: %d · grupos: %d %v", c.Users, c.Prefix, c.DirectConversations, c.Groups, c.GroupSizes)
	reuse := "reaproveitados"
	if c.UsersCreated > 0 || c.GroupsCreated > 0 {
		reuse = fmt.Sprintf("%d usuários e %d grupos criados nesta execução", c.UsersCreated, c.GroupsCreated)
	}
	line("- Usuários e grupos: %s", reuse)
	line("- Taxa: %.2f msg/s por usuário · duração %s · rampa %s · drenagem %s · recibos %v · semente %d",
		c.Rate, c.Duration, c.Ramp, c.Drain, c.Receipts, c.Seed)
	line("")

	line("## Resumo")
	line("")
	throughput := "—"
	if c.Duration > 0 {
		throughput = fmt.Sprintf("%.1f msg/s", float64(s.Sent)/c.Duration.Seconds())
	}
	line("| Métrica | Valor |")
	line("|---|---|")
	line("| Mensagens enviadas | %d |", s.Sent)
	line("| Confirmadas (ACK) | %d (%s) |", s.Acked, percent(s.Acked, s.Sent))
	line("| Sem ACK | %d |", s.MissingAcks)
	line("| Vazão média de envio | %s |", throughput)
	line("| Entregas esperadas | %d |", s.ExpectedDeliveries)
	line("| Entregas realizadas | %d |", s.Delivered)
	line("| Taxa de entrega | %s |", percent(s.Delivered, s.ExpectedDeliveries))
	line("| Entregas perdidas | %d |", s.MissingDeliveries)
	line("| Duplicatas | %d |", s.Duplicates)
	line("| Violações de ordem | %d |", s.OrderViolations)
	line("| Desconexões | %d |", s.Disconnects)
	line("")

	line("## Latência")
	line("")
	line("Valores em ms. ACK: envio → `message.ack`. Entrega: envio → `message.new` no destinatário.")
	line("")
	line("| Medida | n | p50 | p90 | p95 | p99 | máx |")
	line("|---|---|---|---|---|---|---|")
	for _, row := range []struct {
		name string
		l    metrics.Latency
	}{{"ACK", s.Ack}, {"Entrega (direta)", s.DeliveryDirect}, {"Entrega (grupo)", s.DeliveryGroup}} {
		line("| %s | %d | %s | %s | %s | %s | %s |", row.name, row.l.Count, ms(row.l.P50), ms(row.l.P90), ms(row.l.P95), ms(row.l.P99), ms(row.l.Max))
	}
	line("")

	line("## Erros e status")
	line("")
	writeCounts(&b, "Erros", s.Errors)
	writeCounts(&b, "`message.status` recebidos", s.Statuses)

	line("## Servidor (pprof)")
	line("")
	if !res.PprofAvailable {
		line("pprof indisponível: suba o servidor com `DEV_MODE=true` e `PPROF_ADDRESS` (ver `make load-server`).")
	} else {
		if len(res.Server) > 0 {
			minG, maxG := res.Server[0].Goroutines, res.Server[0].Goroutines
			var maxAlloc, maxInuse uint64
			for _, sm := range res.Server {
				minG, maxG = min(minG, sm.Goroutines), max(maxG, sm.Goroutines)
				maxAlloc, maxInuse = max(maxAlloc, sm.HeapAllocBytes), max(maxInuse, sm.HeapInuseBytes)
			}
			line("| Métrica | Valor |")
			line("|---|---|")
			line("| Amostras | %d |", len(res.Server))
			line("| Goroutines (mín / máx) | %d / %d |", minG, maxG)
			line("| Heap alocado máx | %s |", mib(maxAlloc))
			line("| Heap em uso máx | %s |", mib(maxInuse))
			line("")
		}
		for _, f := range res.ProfileFiles {
			line("- `%s` — `go tool pprof -top %s`", f, f)
		}
	}
	line("")

	line("## Linha do tempo")
	line("")
	line("| Janela | Enviadas | Confirmadas | Entregues |")
	line("|---|---|---|---|")
	var windows []metrics.Bucket
	for _, bk := range s.Timeline {
		w := bk.Second / timelineWindow
		for len(windows) <= w {
			windows = append(windows, metrics.Bucket{Second: len(windows) * timelineWindow})
		}
		windows[w].Sent += bk.Sent
		windows[w].Acked += bk.Acked
		windows[w].Delivered += bk.Delivered
	}
	for _, w := range windows {
		line("| %d–%d s | %d | %d | %d |", w.Second, w.Second+timelineWindow, w.Sent, w.Acked, w.Delivered)
	}
	return b.String()
}

func writeCounts(b *strings.Builder, title string, counts map[string]int) {
	fmt.Fprintf(b, "**%s:** ", title)
	if len(counts) == 0 {
		b.WriteString("nenhum\n\n")
		return
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("`%s` %d", k, counts[k]))
	}
	b.WriteString(strings.Join(parts, " · ") + "\n\n")
}
