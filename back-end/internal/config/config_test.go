package config

import (
	"testing"
	"time"
)

// TestLoadDefaults checks that every field falls back to the
// documented default when the environment is completely empty.
// t.Setenv temporarily sets an environment variable and restores the
// previous value automatically when the test ends; it also marks the
// test as non-parallel-safe, which is fine here because only one
// goroutine touches the environment at a time.
func TestLoadDefaults(t *testing.T) {
	// Point the loader at an empty, controlled environment.
	t.Setenv("SERVER_ADDRESS", "")
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("PERSISTENCE_WORKERS", "")
	t.Setenv("DEV_MODE", "true") // tolerate the empty secret in this test

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	// got/want pairs: the idiomatic mismatch report, so the failure
	// line shows both values at once.
	if got, want := cfg.ServerAddress, ":8080"; got != want {
		t.Errorf("ServerAddress: got %q, want %q", got, want)
	}
	if got, want := cfg.DatabasePath, "./chat.db"; got != want {
		t.Errorf("DatabasePath: got %q, want %q", got, want)
	}
	if got, want := cfg.PersistenceWorkers, 4; got != want {
		t.Errorf("PersistenceWorkers: got %d, want %d", got, want)
	}
	if got, want := cfg.PingInterval, 30*time.Second; got != want {
		t.Errorf("PingInterval: got %v, want %v", got, want)
	}
	if got, want := cfg.PongTimeout, 60*time.Second; got != want {
		t.Errorf("PongTimeout: got %v, want %v", got, want)
	}
	if got, want := cfg.TokenExpiry, 24*time.Hour; got != want {
		t.Errorf("TokenExpiry: got %v, want %v", got, want)
	}
	if got, want := cfg.ClientQueueSize, 256; got != want {
		t.Errorf("ClientQueueSize: got %d, want %d", got, want)
	}
	if got, want := cfg.PersistenceQueueSize, 4096; got != want {
		t.Errorf("PersistenceQueueSize: got %d, want %d", got, want)
	}
}

// TestLoadRejectsEmptySecretOutsideDevMode guards the fail-fast rule:
// without DEV_MODE, an empty JWT_SECRET must stop the server from
// starting with an unusable configuration.
func TestLoadRejectsEmptySecretOutsideDevMode(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("DEV_MODE", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with empty JWT_SECRET and DEV_MODE=false must return an error")
	}
}

// TestLoadInvalidIntervals exercises the ping/pong validation branch
// with a table of bad combinations. Table-driven testing keeps the
// cases as data: adding one is a single line instead of another
// copy-pasted test body.
func TestLoadInvalidIntervals(t *testing.T) {
	cases := []struct {
		name         string
		pingInterval string
		pongTimeout  string
		expectError  bool
	}{
		{"zero ping", "0s", "60s", true},
		{"pong before ping", "30s", "20s", true},
		{"unparseable ping", "not-a-duration", "60s", true},
		{"valid pair", "10s", "20s", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PING_INTERVAL", tc.pingInterval)
			t.Setenv("PONG_TIMEOUT", tc.pongTimeout)
			t.Setenv("JWT_SECRET", "test-secret") // bypass the dev-mode path
			t.Setenv("DEV_MODE", "false")

			_, err := Load()
			if (err != nil) != tc.expectError {
				t.Errorf("Load() error = %v, want error: %v", err, tc.expectError)
			}
		})
	}
}
