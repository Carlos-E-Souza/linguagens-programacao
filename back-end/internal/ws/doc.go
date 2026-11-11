// Package ws defines the WebSocket wire contract between the server and
// the desktop client: the envelope that wraps every frame and the payload
// types carried inside it.
//
// The names here are a public contract, shared with the client, and they
// match the JSON tags, the REST request fields and the database columns.
// A value therefore keeps the same name all the way from the SQL row to
// the JavaScript object, which is the point of the project's naming
// convention: there is exactly one spelling for each concept.
//
// Two design choices are worth knowing when changing this package:
//
//   - The envelope carries its payload as json.RawMessage. A frame cannot
//     be decoded into a concrete payload type until its Type is known, so
//     the payload is decoded in a second step by the hub.
//   - The client-to-server and server-to-client constants are declared
//     separately, so each direction lists exactly the types it accepts.
//
// Client-to-server: message.send, message.delivered, message.read, sync.
//
// Server-to-client: message.ack, message.new, message.status, presence,
// conversation.updated, sync.end, error, server.shutdown.
package ws
