package profiler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseGoroutines(t *testing.T) {
	n, err := parseGoroutines(strings.NewReader(string(fixture(t, "goroutine.txt"))))
	if err != nil || n != 14 {
		t.Fatalf("parseGoroutines = %d, %v; esperado 14", n, err)
	}
	if _, err := parseGoroutines(strings.NewReader("outra coisa\n")); err == nil {
		t.Fatal("esperava erro sem a linha de total")
	}
}

func TestParseHeap(t *testing.T) {
	alloc, inuse, err := parseHeap(strings.NewReader(string(fixture(t, "heap.txt"))))
	if err != nil || alloc != 1524488 || inuse != 2842624 {
		t.Fatalf("parseHeap = %d, %d, %v", alloc, inuse, err)
	}
	if _, _, err := parseHeap(strings.NewReader("# HeapAlloc = 10\n")); err == nil {
		t.Fatal("esperava erro sem HeapInuse")
	}
}

// fakePprof serve as fixtures nos caminhos do net/http/pprof.
func fakePprof(t *testing.T) *httptest.Server {
	t.Helper()
	goroutines, heap := fixture(t, "goroutine.txt"), fixture(t, "heap.txt")
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("index")) })
	mux.HandleFunc("/debug/pprof/goroutine", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(goroutines) })
	mux.HandleFunc("/debug/pprof/heap", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("debug") == "1" {
			_, _ = w.Write(heap)
			return
		}
		_, _ = w.Write([]byte("heap-binario"))
	})
	mux.HandleFunc("/debug/pprof/profile", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("cpu-" + r.URL.Query().Get("seconds")))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSamplerRun(t *testing.T) {
	srv := fakePprof(t)
	s := &Sampler{BaseURL: srv.URL, Interval: 10 * time.Millisecond}
	if !s.Available(context.Background()) {
		t.Fatal("Available = false")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Millisecond)
	defer cancel()
	samples := s.Run(ctx, time.Now())
	if len(samples) < 3 {
		t.Fatalf("%d amostras, esperado >= 3", len(samples))
	}
	if samples[0].Goroutines != 14 || samples[0].HeapAllocBytes != 1524488 || samples[0].HeapInuseBytes != 2842624 {
		t.Fatalf("amostra inesperada: %+v", samples[0])
	}
}

func TestSamplerCapture(t *testing.T) {
	srv := fakePprof(t)
	s := &Sampler{BaseURL: srv.URL}
	dir := t.TempDir()
	cpu, heap := filepath.Join(dir, "cpu.pprof"), filepath.Join(dir, "heap.pprof")
	if err := s.CaptureCPU(context.Background(), 3, cpu); err != nil {
		t.Fatalf("CaptureCPU: %v", err)
	}
	if err := s.CaptureHeap(context.Background(), heap); err != nil {
		t.Fatalf("CaptureHeap: %v", err)
	}
	if got, _ := os.ReadFile(cpu); string(got) != "cpu-3" {
		t.Fatalf("cpu.pprof = %q", got)
	}
	if got, _ := os.ReadFile(heap); string(got) != "heap-binario" {
		t.Fatalf("heap.pprof = %q", got)
	}
}

func TestSamplerUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	s := &Sampler{BaseURL: url, Interval: 5 * time.Millisecond}
	if s.Available(context.Background()) {
		t.Fatal("Available = true com servidor fechado")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if samples := s.Run(ctx, time.Now()); len(samples) != 0 {
		t.Fatalf("amostras com servidor fechado: %d", len(samples))
	}
	if err := s.CaptureHeap(context.Background(), filepath.Join(t.TempDir(), "h")); err == nil {
		t.Fatal("CaptureHeap deveria falhar")
	}
}
