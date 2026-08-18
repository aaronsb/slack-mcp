// Package handle encodes references that tools hand out and take back.
//
// ADR-003 decided that an agent never composes an identifier. Tools return
// handles and accept handles, which is what retires the two incompatible thread
// ID formats and the caller-supplied cursors and message timestamps.
//
// Handles are encoded rather than stored. A registry would tie a handle's
// lifetime to one server process, and this server is spawned per host session
// and sleeps between calls — a handle from a poll would stop resolving as soon
// as the host restarted. Encoding makes them durable without any state.
//
// They are opaque, not secret. Anyone can decode one; the point is that the
// model has nothing to assemble and no format to get wrong.
package handle

import (
	"encoding/base64"
	"errors"
	"strings"
)

// Kind says what a handle points at, so read can dispatch without guessing.
type Kind string

const (
	// KindMessage is one message in a conversation.
	KindMessage Kind = "m"
	// KindThread is a thread, identified by its root message.
	KindThread Kind = "t"
	// KindConversation is a whole channel, DM, or group DM.
	KindConversation Kind = "c"
)

// Ref is what a handle decodes back to.
type Ref struct {
	Kind Kind
	// Channel is the Slack conversation ID.
	Channel string
	// TS is the message or thread-root timestamp. Empty for KindConversation.
	TS string
}

const prefix = "ev_"

var (
	// ErrMalformed means the handle was not produced by Encode.
	ErrMalformed = errors.New("handle: malformed")
	// ErrIncomplete means the handle decoded but lacked a required field.
	ErrIncomplete = errors.New("handle: missing channel or timestamp")
)

var encoding = base64.RawURLEncoding

// Encode builds a handle for a reference.
func Encode(ref Ref) string {
	parts := string(ref.Kind) + "\x00" + ref.Channel + "\x00" + ref.TS
	return prefix + encoding.EncodeToString([]byte(parts))
}

// Message builds a handle for one message.
func Message(channel, ts string) string {
	return Encode(Ref{Kind: KindMessage, Channel: channel, TS: ts})
}

// Thread builds a handle for a thread, given its root timestamp.
func Thread(channel, threadTS string) string {
	return Encode(Ref{Kind: KindThread, Channel: channel, TS: threadTS})
}

// Conversation builds a handle for a whole conversation.
func Conversation(channel string) string {
	return Encode(Ref{Kind: KindConversation, Channel: channel})
}

// Decode turns a handle back into a reference.
func Decode(h string) (Ref, error) {
	rest, ok := strings.CutPrefix(h, prefix)
	if !ok {
		return Ref{}, ErrMalformed
	}

	raw, err := encoding.DecodeString(rest)
	if err != nil {
		return Ref{}, ErrMalformed
	}

	fields := strings.Split(string(raw), "\x00")
	if len(fields) != 3 {
		return Ref{}, ErrMalformed
	}

	ref := Ref{Kind: Kind(fields[0]), Channel: fields[1], TS: fields[2]}
	switch ref.Kind {
	case KindMessage, KindThread:
		if ref.Channel == "" || ref.TS == "" {
			return Ref{}, ErrIncomplete
		}
	case KindConversation:
		if ref.Channel == "" {
			return Ref{}, ErrIncomplete
		}
	default:
		return Ref{}, ErrMalformed
	}
	return ref, nil
}

// Is reports whether a string looks like a handle. Callers accepting either a
// handle or a description use this to tell them apart.
func Is(s string) bool {
	_, err := Decode(s)
	return err == nil
}
