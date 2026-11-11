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

// shutdownTimeout bounds how long we wait for in-flight requests before
// giving up and closing the process anyway.
const shutdownTimeout = 15 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
}

// run holds the real logic so that every failure path can return an
// error instead of calling os.Exit, which would skip deferred cleanup.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// JSON logs by default: one line per event, easy to grep and to feed
	// into a log collector.
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

	// The persistence pool and the hub are started before the HTTP server
	// so that a request arriving immediately already has both available.
	//
	// The pool is not tied to ctx on purpose: ctx is cancelled by the
	// signal, while the hub may still accept messages until it is stopped
	// below. The pool only stops through pool.Shutdown, which drains it.
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

	// Order matters: Recover is outermost so it also catches a panic
	// raised inside Logging itself.
	handler := middleware.Recover(middleware.Logging(api.Routes()))

	// The profiler runs on its own address so the public port never
	// exposes it. config.Load already refuses a PPROF_ADDRESS outside dev
	// mode, which is why no extra guard is needed here.
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

	// Stop accepting new requests and wait for the in-flight ones. The
	// context is fresh on purpose: ctx is already cancelled at this
	// point, so reusing it would abort the drain immediately.
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

	// Order matters. The hub stops first so no new message is accepted,
	// then the pool drains everything already accepted, and only then is
	// the database closed underneath it.
	stopHub()
	background.Wait()

	if err := pool.Shutdown(shutdownCtx); err != nil {
		return err
	}

	slog.Info("server stopped cleanly")
	return nil
}

// startDebugServer serves net/http/pprof on its own address when
// PPROF_ADDRESS is set, and returns nil when it is not.
//
// The endpoints are registered on a dedicated mux rather than on the main
// one so that a public deployment cannot reach them even by guessing the
// path, and so the profiler can be reached without authentication while
// the API stays protected.
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
