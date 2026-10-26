// Package hub is the central concurrent hub of the server. It keeps
// track of every connected client and routes each message to the
// members of the conversation it belongs to.
//
// A single goroutine owns the hub state and drives the event loop in
// Run, selecting over the register, unregister, incoming and notify
// channels. Because only that goroutine mutates the membership maps,
// no lock is needed on the write path; a sync.RWMutex protects the
// structures that other goroutines read (such as IsOnline called by the
// REST API) and message sequences are assigned there, which removes any
// race on ordering.
//
// Each connected user is served by two goroutines, one reading and one
// writing, over a buffered channel. A client that cannot keep up is
// disconnected instead of blocking the hub, so one slow consumer never
// stalls delivery for everyone else.
package hub
