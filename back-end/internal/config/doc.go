// Package config loads and validates the server configuration from
// environment variables.
//
// Load reads every setting once and returns an immutable Config, so the
// rest of the program never touches os.Getenv and there is a single
// place to look when a value is wrong. It fails fast on configurations
// that would be unsafe or nonsensical at runtime: an empty JWT_SECRET
// outside dev mode, fewer than one persistence worker, or an invalid
// ping/pong pair. Durations require an explicit unit (30s, 24h).
package config
