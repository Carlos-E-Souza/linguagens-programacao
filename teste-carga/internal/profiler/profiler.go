// Pacote profiler coleta métricas do servidor pelo net/http/pprof durante o
// teste: amostras periódicas de goroutines e heap (formato texto debug=1) e
// os perfis binários de CPU e heap para análise com `go tool pprof`.
//
// O pprof do servidor só existe com DEV_MODE=true e PPROF_ADDRESS definido;
// sem ele o teste segue e o relatório marca "pprof indisponível".
package profiler

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Sample é uma leitura do estado do servidor; At é o tempo desde o início.
type Sample struct {
	At             time.Duration `json:"at_ns"`
	Goroutines     int           `json:"goroutines"`
	HeapAllocBytes uint64        `json:"heap_alloc_bytes"`
	HeapInuseBytes uint64        `json:"heap_inuse_bytes"`
}

// Sampler lê o pprof exposto em BaseURL (ex.: http://localhost:6060).
type Sampler struct {
	BaseURL  string
	Interval time.Duration
	HTTP     *http.Client
}

func (s *Sampler) get(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("pprof %s respondeu %d", path, resp.StatusCode)
	}
	return resp.Body, nil
}

// Available diz se o índice do pprof responde.
func (s *Sampler) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, err := s.get(ctx, "/debug/pprof/")
	if err != nil {
		return false
	}
	body.Close()
	return true
}

// Run amostra na hora e depois a cada Interval até ctx acabar. Leituras que
// falham são puladas.
func (s *Sampler) Run(ctx context.Context, start time.Time) []Sample {
	var samples []Sample
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		if sample, err := s.sample(ctx, start); err == nil {
			samples = append(samples, sample)
		}
		select {
		case <-ctx.Done():
			return samples
		case <-ticker.C:
		}
	}
}

func (s *Sampler) sample(ctx context.Context, start time.Time) (Sample, error) {
	at := time.Since(start)
	body, err := s.get(ctx, "/debug/pprof/goroutine?debug=1")
	if err != nil {
		return Sample{}, err
	}
	goroutines, err := parseGoroutines(body)
	body.Close()
	if err != nil {
		return Sample{}, err
	}
	body, err = s.get(ctx, "/debug/pprof/heap?debug=1")
	if err != nil {
		return Sample{}, err
	}
	defer body.Close()
	alloc, inuse, err := parseHeap(body)
	if err != nil {
		return Sample{}, err
	}
	return Sample{At: at, Goroutines: goroutines, HeapAllocBytes: alloc, HeapInuseBytes: inuse}, nil
}

// CaptureCPU grava um perfil de CPU de seconds segundos em path.
func (s *Sampler) CaptureCPU(ctx context.Context, seconds int, path string) error {
	return s.save(ctx, fmt.Sprintf("/debug/pprof/profile?seconds=%d", seconds), path)
}

// CaptureHeap grava o perfil de heap atual em path.
func (s *Sampler) CaptureHeap(ctx context.Context, path string) error {
	return s.save(ctx, "/debug/pprof/heap", path)
}

func (s *Sampler) save(ctx context.Context, url, path string) error {
	body, err := s.get(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// parseGoroutines lê "goroutine profile: total N" da primeira linha.
func parseGoroutines(r io.Reader) (int, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return 0, err
	}
	const prefix = "goroutine profile: total "
	if !strings.HasPrefix(line, prefix) {
		return 0, errors.New("linha de total de goroutines não encontrada")
	}
	return strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
}

// parseHeap lê as linhas "# HeapAlloc = N" e "# HeapInuse = N".
func parseHeap(r io.Reader) (alloc, inuse uint64, err error) {
	var gotAlloc, gotInuse bool
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if v, ok := strings.CutPrefix(line, "# HeapAlloc = "); ok {
			if alloc, err = strconv.ParseUint(strings.TrimSpace(v), 10, 64); err != nil {
				return 0, 0, err
			}
			gotAlloc = true
		} else if v, ok := strings.CutPrefix(line, "# HeapInuse = "); ok {
			if inuse, err = strconv.ParseUint(strings.TrimSpace(v), 10, 64); err != nil {
				return 0, 0, err
			}
			gotInuse = true
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if !gotAlloc || !gotInuse {
		return 0, 0, errors.New("HeapAlloc/HeapInuse não encontrados")
	}
	return alloc, inuse, nil
}
