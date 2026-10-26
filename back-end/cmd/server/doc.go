// Command server is the entry point of the chat back-end.
//
// It loads the environment configuration, wires the components together
// (database, repositories, HTTP API, WebSocket hub and the persistence
// worker pool) and runs the HTTP server until it receives SIGINT or
// SIGTERM. On shutdown it stops accepting connections, notifies the
// connected clients, drains the persistence queue and closes the
// database, so no message that was acknowledged is lost.
//
// Run it with:
//
//	go run ./cmd/server
package main
