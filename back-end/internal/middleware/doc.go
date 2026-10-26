// Package middleware provides the HTTP middlewares applied to the
// server routes: JWT authentication and request logging, plus the
// recovery that turns a panic into a 500 response and a log entry
// instead of dropping the connection.
//
// Authenticate extracts the bearer token from the Authorization header,
// validates it and stores the user ID in the request context under an
// unexported key type, which prevents collisions with keys set by other
// packages; handlers read it back with UserFromContext.
package middleware
