package handle_test

import (
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/handle"
)

func TestRoundTrip(t *testing.T) {
	cases := []handle.Ref{
		{Kind: handle.KindMessage, Channel: "C0B74M52CQ6", TS: "1782246118.543969"},
		{Kind: handle.KindThread, Channel: "C0B74M52CQ6", TS: "1782246118.543969"},
		{Kind: handle.KindConversation, Channel: "D0ALT99DTPG"},
	}

	for _, want := range cases {
		got, err := handle.Decode(handle.Encode(want))
		if err != nil {
			t.Fatalf("Decode(Encode(%+v)): %v", want, err)
		}
		if got != want {
			t.Errorf("round trip changed the reference: got %+v, want %+v", got, want)
		}
	}
}

// A handle must survive a server restart, so it cannot depend on process state.
// Encoding the same reference twice must give the same string.
func TestEncodingIsDeterministic(t *testing.T) {
	a := handle.Message("C1", "1782246118.543969")
	b := handle.Message("C1", "1782246118.543969")
	if a != b {
		t.Errorf("same reference encoded differently: %q vs %q", a, b)
	}
}

// The model must never be able to assemble one by accident, and read must be
// able to tell a handle from a description.
func TestIsRejectsThingsThatAreNotHandles(t *testing.T) {
	for _, s := range []string{
		"",
		"C0B74M52CQ6",
		"C0B74M52CQ6:1782246118.543969",
		"C0B74M52CQ6.1782246118.543969",
		"the thread with Sarah about the deploy",
		"ev_",
		"ev_!!!!",
		"ev_" + strings.Repeat("A", 12), // decodes, but not into three fields
	} {
		if handle.Is(s) {
			t.Errorf("Is(%q) = true; want false", s)
		}
	}
}

func TestIsAcceptsRealHandles(t *testing.T) {
	for _, h := range []string{
		handle.Message("C1", "1782246118.543969"),
		handle.Thread("C1", "1782246118.543969"),
		handle.Conversation("D1"),
	} {
		if !handle.Is(h) {
			t.Errorf("Is(%q) = false; want true", h)
		}
	}
}

func TestDecodeRejectsIncompleteReferences(t *testing.T) {
	if _, err := handle.Decode(handle.Encode(handle.Ref{Kind: handle.KindMessage, Channel: "C1"})); err == nil {
		t.Error("a message handle with no timestamp decoded successfully")
	}
	if _, err := handle.Decode(handle.Encode(handle.Ref{Kind: handle.KindConversation})); err == nil {
		t.Error("a conversation handle with no channel decoded successfully")
	}
	if _, err := handle.Decode(handle.Encode(handle.Ref{Kind: "zzz", Channel: "C1", TS: "1.0"})); err == nil {
		t.Error("an unknown kind decoded successfully")
	}
}

// Handles go into tool output and back through a model, so they must not carry
// characters that need escaping or invite reformatting.
func TestHandlesAreUrlAndJsonSafe(t *testing.T) {
	h := handle.Message("C0B74M52CQ6", "1782246118.543969")

	if !strings.HasPrefix(h, "ev_") {
		t.Errorf("handle %q lacks the ev_ prefix", h)
	}
	for _, bad := range []string{"+", "/", "=", " ", "\"", "\\", ":", "."} {
		if strings.Contains(strings.TrimPrefix(h, "ev_"), bad) {
			t.Errorf("handle %q contains %q, which needs escaping somewhere", h, bad)
		}
	}
}
