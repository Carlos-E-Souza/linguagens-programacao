// Command loadgen measures the throughput and latency of the chat server
// under many concurrent WebSocket connections.
//
// It registers its own users against a running server, opens one
// connection per user, has them send into a shared group, and reports the
// distribution of the acknowledgement round trips together with the
// goroutine count and heap in use.
//
// It is a development tool, not part of the server binary: it exists so
// that the claims in the plan about concurrency can be checked with real
// numbers instead of being asserted.
//
// Usage:
//
//	go run ./cmd/loadgen -server http://localhost:8080 -clients 200 -messages 20
//
// The flags default to a small run so the tool is safe to point at a
// local server without thinking about it. A client that cannot keep up
// with delivery is disconnected by the server as backpressure, so a small
// number of failures on a long run is expected rather than a bug.
package main
