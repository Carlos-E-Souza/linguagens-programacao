package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/config"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/httputil"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/hub"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/middleware"
	storepkg "github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/store"
)

// shutdownTimeout limita quanto tempo esperamos pelas requisições em
// andamento antes de desistir e encerrar o processo mesmo assim.
const shutdownTimeout = 15 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
}

// run contém a lógica real para que todo caminho de falha possa devolver
// um erro em vez de chamar os.Exit, que pularia a limpeza adiada (defer).
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Logs em JSON por padrão: uma linha por evento, fácil de filtrar com
	// grep e de enviar a um coletor de logs.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	handle, err := db.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer handle.Close()

	if err := db.Migrate(ctx, handle); err != nil {
		return err
	}

	store := db.NewStore(handle)

	issuer, err := auth.NewTokenIssuer(cfg.JWTSecret, cfg.TokenExpiry)
	if err != nil {
		return err
	}

	// O pool de persistência e o hub sobem antes do servidor HTTP para que
	// uma requisição que chegue de imediato já encontre os dois prontos.
	//
	// O pool não fica preso a ctx de propósito: ctx é cancelado pelo sinal,
	// enquanto o hub ainda pode aceitar mensagens até ser parado mais
	// abaixo. O pool só para por pool.Shutdown, que o drena.
	pool := storepkg.NewPool(store, storepkg.DefaultOptions(cfg.PersistenceQueueSize, cfg.PersistenceWorkers))

	realtime := hub.New(cfg, store, pool)

	hubCtx, stopHub := context.WithCancel(context.Background())
	defer stopHub()

	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		realtime.Run(hubCtx)
	}()

	api := httputil.NewServer(cfg, store, auth.DefaultHasher(), issuer)
	api.SetHub(realtime)

	// A ordem importa: Recover fica por fora para capturar também um panic
	// disparado dentro do próprio Logging.
	handler := middleware.Recover(middleware.Logging(api.Routes()))

	// O profiler roda em endereço próprio para que a porta pública nunca o
	// exponha. config.Load já recusa PPROF_ADDRESS fora do modo dev, por
	// isso nenhuma proteção extra é necessária aqui.
	debugServer, err := startDebugServer(cfg)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.ServerAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "address", cfg.ServerAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err

	case <-ctx.Done():
		slog.Info("shutdown signal received, draining connections")
	}

	// Para de aceitar novas requisições e espera as que estão em andamento.
	// O contexto é novo de propósito: ctx já está cancelado neste ponto,
	// então reutilizá-lo abortaria a drenagem na hora.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}

	if debugServer != nil {
		if err := debugServer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("debug server did not stop cleanly", "error", err)
		}
	}

	// A ordem importa. O hub para primeiro para que nenhuma mensagem nova
	// seja aceita, depois o pool drena tudo o que já foi aceito, e só então
	// o banco é fechado por baixo dele.
	stopHub()
	background.Wait()

	if err := pool.Shutdown(shutdownCtx); err != nil {
		return err
	}

	slog.Info("server stopped cleanly")
	return nil
}

// startDebugServer serve net/http/pprof em endereço próprio quando
// PPROF_ADDRESS está definido, e devolve nil quando não está.
//
// Os endpoints são registrados num mux dedicado em vez do principal para
// que uma instalação pública não os alcance nem adivinhando o caminho, e
// para que o profiler fique acessível sem autenticação enquanto a API
// continua protegida.
func startDebugServer(cfg config.Config) (*http.Server, error) {
	if cfg.PprofAddress == "" {
		return nil, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	debugServer := &http.Server{
		Addr:              cfg.PprofAddress,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Warn("pprof debug server listening (development only)", "address", cfg.PprofAddress)
		if err := debugServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("pprof server stopped", "error", err)
		}
	}()

	return debugServer, nil
}
