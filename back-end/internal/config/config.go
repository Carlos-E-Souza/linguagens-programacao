// Package config loads the server environment configuration with
// safe defaults and validates what is required to run.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// devJWTSecret is the placeholder key used when DEV_MODE is on and no
// JWT_SECRET was provided. It is intentionally obvious so it can never be
// mistaken for a real secret.
const devJWTSecret = "insecure-dev-secret-do-not-use-in-production"

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
	MessageLimitChars    int           // maximum length of a message body
	TypingCooldown       time.Duration // minimum gap between typing events per user
	PprofAddress         string        // separate debug listen address; empty disables it
	DevMode              bool          // whether DEV_MODE is on
}

// isDevMode reports whether DEV_MODE is enabled, marking a local run
// without a .env file where an empty secret is tolerated. We decide it
// via a dedicated variable instead of guessing from other signals.
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
		MessageLimitChars:    envIntOr("MESSAGE_LIMIT_CHARS", 4000),
		PprofAddress:         envOr("PPROF_ADDRESS", ""),
		DevMode:              isDevMode(),
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
	if cfg.TypingCooldown, err = envDurationOr("TYPING_COOLDOWN", 2*time.Second); err != nil {
		return cfg, err
	}

	if cfg.JWTSecret == "" {
		if !isDevMode() {
			return cfg, fmt.Errorf("JWT_SECRET is required (or set DEV_MODE=true for development)")
		}
		// Dev mode still needs a non-empty key, because the token issuer
		// refuses to sign with an empty secret. A fixed placeholder keeps
		// local runs working without pretending to be secure.
		cfg.JWTSecret = devJWTSecret
		slog.Warn("DEV_MODE is enabled: using an insecure development JWT secret; tokens are not verifiable across restarts")
	}
	if cfg.PersistenceWorkers < 1 {
		return cfg, fmt.Errorf("PERSISTENCE_WORKERS must be at least 1")
	}
	if cfg.PingInterval <= 0 || cfg.PongTimeout < cfg.PingInterval {
		return cfg, fmt.Errorf("invalid ping/pong intervals")
	}
	if cfg.MessageLimitChars < 1 {
		return cfg, fmt.Errorf("MESSAGE_LIMIT_CHARS must be at least 1")
	}
	if cfg.TypingCooldown < 0 {
		return cfg, fmt.Errorf("TYPING_COOLDOWN must not be negative")
	}
	// The profiler endpoints expose internals and have no authentication,
	// so they are refused outside dev mode rather than silently enabled.
	if cfg.PprofAddress != "" && !cfg.DevMode {
		return cfg, fmt.Errorf("PPROF_ADDRESS requires DEV_MODE=true")
	}

	return cfg, nil
}

// envOr returns the value of the named variable or a fallback when it
// is unset (or empty). Empty is treated as unset because an empty
// string is never a meaningful value for any setting here.
func envOr(name string, fallback string) string {
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
