// Package models defines the domain types shared by the whole server:
// User, Conversation, Member and Message.
//
// The struct tags are part of the public contract. They are in English
// and identical across every layer — the same field is called
// conversation_id in the SQL column, in the REST payload and in the
// WebSocket envelope — so a value never has to be renamed while it
// travels through the system.
package models
