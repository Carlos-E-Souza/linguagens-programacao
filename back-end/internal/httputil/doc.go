// Package httputil implements the HTTP surface of the server: the REST
// handlers for authentication, users, conversations and messages, the
// /ws upgrade endpoint that hands the connection over to the hub, and
// the small JSON helpers shared by all of them.
//
// Routing uses http.ServeMux with the Go 1.22+ method-and-wildcard
// patterns, so a route such as "GET /api/conversations/{id}/messages"
// is matched and its variables read with r.PathValue. WriteJSON,
// WriteError and ReadJSON centralise the response format and the
// request limits (MaxBytesReader plus DisallowUnknownFields), which
// keeps every endpoint answering with the same shape of body.
package httputil
