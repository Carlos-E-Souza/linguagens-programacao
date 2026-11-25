//go:build loadtest

// Teste de integração contra um servidor real. Rode com:
//
//	make load-server   # outro terminal
//	cd teste-carga && LOAD_TEST_SERVER_URL=http://localhost:8080 LOAD_TEST_PPROF_URL=http://localhost:6060 \
//	  go test -race -tags loadtest -run TestRunAgainstServer -v .
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunAgainstServer(t *testing.T) {
	server := os.Getenv("LOAD_TEST_SERVER_URL")
	if server == "" {
		t.Skip("defina LOAD_TEST_SERVER_URL com o endereço do servidor")
	}
	cfg := runConfig{
		Prefix:         "cargait",
		ServerURL:      server,
		PprofURL:       os.Getenv("LOAD_TEST_PPROF_URL"),
		Users:          6,
		DirectPerUser:  1,
		GroupSizes:     []int{3, 6},
		Rate:           5,
		Duration:       2 * time.Second,
		Ramp:           200 * time.Millisecond,
		Drain:          2 * time.Second,
		Receipts:       true,
		Seed:           1,
		SampleInterval: 500 * time.Millisecond,
		OutDir:         t.TempDir(),
	}

	for attempt := 1; attempt <= 2; attempt++ {
		res, err := run(context.Background(), cfg)
		if err != nil {
			t.Fatalf("execução %d: %v", attempt, err)
		}
		s := res.Summary
		if s.Sent == 0 || s.MissingAcks != 0 || s.MissingDeliveries != 0 || s.Duplicates != 0 || s.OrderViolations != 0 || len(s.Errors) != 0 || s.Disconnects != 0 {
			t.Fatalf("execução %d com problemas: %+v", attempt, s)
		}
		// A segunda execução precisa reaproveitar usuários e grupos.
		if attempt == 2 && (res.Config.UsersCreated != 0 || res.Config.GroupsCreated != 0) {
			t.Fatalf("segunda execução criou %d usuários e %d grupos", res.Config.UsersCreated, res.Config.GroupsCreated)
		}
		if cfg.PprofURL != "" && (!res.PprofAvailable || len(res.Server) == 0) {
			t.Fatalf("pprof não coletado: disponível=%v amostras=%d", res.PprofAvailable, len(res.Server))
		}
		dir := filepath.Join(cfg.OutDir, res.RunID)
		for _, f := range []string{"report.json", "report.md"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				t.Fatalf("execução %d sem %s: %v", attempt, f, err)
			}
		}
		time.Sleep(time.Second) // RunID tem resolução de segundo
	}
}
