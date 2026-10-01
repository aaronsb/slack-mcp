// Package safety holds the outbound-safety state of ADR-013 and the account
// identity and safety posture of ADR-014: the quarantine file and its strike
// lock, the trusted-destinations file, the pending-request file, and the
// signed request state that carries an elicitation answer across a retry.
//
// The three files are append-only JSON lines, one set per workspace, keyed
// by team ID beside the estate ledger. Every reader follows the same rules
// (journal.go), so a CLI clear, an approval, or a block written by another
// server process applies at a running server's next read.
//
// No record holds a matched value or message content, with one exception the
// ADR makes: a pending request for an external destination holds the text
// and file names being sent, so the operator can read them, until it
// expires. Everything else is recorded as a SHA-256, a pattern class, and a
// location.
package safety

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"
)

// KeyKind is what a quarantine or trust key names.
type KeyKind string

const (
	// KeyPerson keys on a user ID. A person key covers every DM and group
	// DM with that person.
	KeyPerson KeyKind = "person"
	// KeyConversation keys on a conversation ID: that conversation only.
	KeyConversation KeyKind = "conversation"
	// KeyStrikes is the strike count, as a target of a clear.
	KeyStrikes KeyKind = "strikes"
)

// Key names a person or a conversation by ID, with the name it had when
// recorded. Name is display only; the ID decides.
type Key struct {
	Kind KeyKind `json:"kind"`
	ID   string  `json:"id,omitempty"`
	Name string  `json:"name,omitempty"`
}

// Person returns a person key.
func Person(userID, handle string) Key { return Key{Kind: KeyPerson, ID: userID, Name: handle} }

// Conversation returns a conversation key.
func Conversation(convID, name string) Key {
	return Key{Kind: KeyConversation, ID: convID, Name: name}
}

// StrikesKey is the clear target for the strike count.
var StrikesKey = Key{Kind: KeyStrikes}

// DestKind classifies a write's destination. The caller classifies by the
// conversation's is_im and is_mpim flags, never by ID prefix (ADR-013).
type DestKind string

const (
	// DestSelf is the account's own DM: never quarantined, never external.
	DestSelf DestKind = "self"
	// DestPerson is a person with no DM yet.
	DestPerson DestKind = "person"
	// DestDM is a one-to-one DM.
	DestDM DestKind = "dm"
	// DestGroupDM is a multi-person DM.
	DestGroupDM DestKind = "group-dm"
	// DestChannel is any other conversation.
	DestChannel DestKind = "channel"
)

// Destination is where a gated write was going.
type Destination struct {
	Kind DestKind `json:"kind"`
	// ConversationID is empty for DestPerson.
	ConversationID string `json:"conversation_id,omitempty"`
	// Name is the destination as named at the time (#general, @dana).
	Name string `json:"name,omitempty"`
	// Members are the other people in it: the other member of a DM or the
	// person of DestPerson, every member other than the account's own user
	// of a group DM. Empty for a channel and the self-DM.
	Members []Key `json:"members,omitempty"`
}

// ID is the conversation ID, or for a person with no DM the person's ID.
func (d Destination) ID() string {
	if d.ConversationID != "" {
		return d.ConversationID
	}
	if len(d.Members) > 0 {
		return d.Members[0].ID
	}
	return ""
}

// QuarantineKeys are the keys a block on d closes: the person of a DM, each
// other member of a group DM, the conversation of a channel, and nothing for
// the self-DM.
func (d Destination) QuarantineKeys() []Key {
	switch d.Kind {
	case DestSelf:
		return nil
	case DestPerson, DestDM, DestGroupDM:
		out := make([]Key, 0, len(d.Members))
		for _, m := range d.Members {
			out = append(out, Person(m.ID, m.Name))
		}
		return out
	default:
		return []Key{Conversation(d.ConversationID, d.Name)}
	}
}

// Case is an approval-gate case (ADR-013, The approval gate).
type Case string

const (
	// CaseExternal: a destination outside the organization (case 1).
	CaseExternal Case = "external"
	// CaseCrossConversation: a downloaded file moving between
	// conversations (case 2).
	CaseCrossConversation Case = "cross-conversation"
	// CaseLift: lifting a quarantine or the strike lock (case 3). Never
	// trusted, never elicited.
	CaseLift Case = "lift"
)

// normCases returns cases sorted and deduplicated, so a set compares equal
// however it was built.
func normCases(cs []Case) []Case {
	seen := map[Case]bool{}
	out := make([]Case, 0, len(cs))
	for _, c := range cs {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameCases(a, b []Case) bool {
	a, b = normCases(a), normCases(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasCase(cs []Case, c Case) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

// CasesString joins cases for a log line or listing.
func CasesString(cs []Case) string {
	parts := make([]string, 0, len(cs))
	for _, c := range normCases(cs) {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ",")
}

// HashText is the SHA-256 of s, hex: how text and file names are recorded.
func HashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// HashContent hashes a call's content as one value: each part
// length-prefixed, so ("ab","c") and ("a","bc") differ. A pending request
// and a request state bind to this hash.
func HashContent(parts ...[]byte) string {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CallRecord is a tool call as the quarantine file records it: its
// non-content parameters, and the text and file names only as SHA-256.
type CallRecord struct {
	Tool        string       `json:"tool"`
	Destination string       `json:"destination,omitempty"`
	Thread      string       `json:"thread,omitempty"`
	TextSHA256  string       `json:"text_sha256,omitempty"`
	Files       []FileRecord `json:"files,omitempty"`
}

// FileRecord is one attached file: its name's hash and its size.
type FileRecord struct {
	NameSHA256 string `json:"name_sha256"`
	Size       int64  `json:"size"`
}

// MatchRecord is where a block matched: the pattern class, the location,
// and the decode chain. It has no field for the matched value.
type MatchRecord struct {
	Class string `json:"class"`
	// Location: text, reaction, upload-title, file-name, or file-bytes.
	Location string `json:"location"`
	// File is the 1-based position of the file, for file locations.
	File   int      `json:"file,omitempty"`
	Offset int64    `json:"offset"`
	Chain  []string `json:"chain,omitempty"`
}
