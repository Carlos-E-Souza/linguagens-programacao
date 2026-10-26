// Package auth issues and validates the authentication credentials of
// the server: it hashes and verifies user passwords with bcrypt and
// generates and validates the JWT access tokens (HS256).
//
// The package is deliberately free of HTTP and database concerns. The
// REST handlers decide when to call it and the middleware decides how
// the resulting token travels in the Authorization header, which keeps
// the security rules in one place and easy to test.
package auth
