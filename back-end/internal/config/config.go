// Package config loads the server environment configuration with
// safe defaults and validates what is required to run.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config groups everything the server needs to start.
// Fields are exported (capitalized) so package main can read them.
type Config struct {
	ServerAddress        string        // HTTP listen address, e.g. ":8080"
	DatabasePath         string        // SQLite database file path
	JWTSecret            string        // HS256 key used to sign tokens
	PersistenceWorkers   int           // number of goroutines writing messages
	PingInterval         time.Duration // WebSocket heartbeat cadence
	PongTimeout          time.Duration // max time to wait for a ping acknowledgement
	TokenExpiry          time.Duration // JWT validity window
	ClientQueueSize      int           // per-connection outgoing channel buffer
	PersistenceQueueSize int           // persistence queue buffer
}

// isDevMode reports whether DEV_MODE is enabled, marking a local run
// without a .env file where an empty secret is tolerated. We decide it
// via a dedicated variable instead of guessing from other signals.
//
// It is a function rather than a package-level variable on purpose: a
// variable would be evaluated once at process start, so a test using
// t.Setenv("DEV_MODE", ...) would keep reading the stale value.
func isDevMode() bool {
	return os.Getenv("DEV_MODE") == "true"
}

// Load reads the environment and returns a ready-to-use Config.
// An empty secret is rejected outside dev mode: failing fast is
// better than starting a server nobody can authenticate against.
func Load() (Config, error) {
	cfg := Config{
		ServerAddress:        envOr("SERVER_ADDRESS", ":8080"),
		DatabasePath:         envOr("DATABASE_PATH", "./chat.db"),
		JWTSecret:            envOr("JWT_SECRET", ""),
		PersistenceWorkers:   envIntOr("PERSISTENCE_WORKERS", 4),
		ClientQueueSize:      envIntOr("CLIENT_QUEUE_SIZE", 256),
		PersistenceQueueSize: envIntOr("PERSISTENCE_QUEUE_SIZE", 4096),
	}

	// Durations are parsed strictly: a typo such as PING_INTERVAL=30
	// (no unit) must stop startup instead of silently running with a
	// value the operator never chose.
	var err error
	if cfg.PingInterval, err = envDurationOr("PING_INTERVAL", 30*time.Second); err != nil {
		return cfg, err
	}
	if cfg.PongTimeout, err = envDurationOr("PONG_TIMEOUT", 60*time.Second); err != nil {
		return cfg, err
	}
	if cfg.TokenExpiry, err = envDurationOr("TOKEN_EXPIRY", 24*time.Hour); err != nil {
		return cfg, err
	}

	if cfg.JWTSecret == "" && !isDevMode() {
		return cfg, fmt.Errorf("JWT_SECRET is required (or set DEV_MODE=true for development)")
	}
	if cfg.PersistenceWorkers < 1 {
		return cfg, fmt.Errorf("PERSISTENCE_WORKERS must be at least 1")
	}
	if cfg.PingInterval <= 0 || cfg.PongTimeout < cfg.PingInterval {
		return cfg, fmt.Errorf("invalid ping/pong intervals")
	}

	return cfg, nil
}

// envOr returns the value of the named variable or a fallback when it
// is unset (or empty). Empty is treated as unset because an empty
// string is never a meaningful value for any setting here.
func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// envIntOr parses an integer variable. A malformed value falls back to
// the default rather than killing startup, but the problem is logged
// so misconfiguration stays visible in the server logs.
func envIntOr(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s=%q is not a valid integer, using %d\n", name, value, fallback)
		return fallback
	}
	return parsed
}

// envDurationOr parses a duration variable such as "30s" or "24h",
// which is exactly what time.ParseDuration understands. A unit-less
// number (like "30") is rejected with an error, teaching users to
// always add a unit.
func envDurationOr(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback, fmt.Errorf("%s=%q is not a valid duration (use a unit, e.g. 30s or 24h)", name, value)
	}
	return parsed, nil
}
