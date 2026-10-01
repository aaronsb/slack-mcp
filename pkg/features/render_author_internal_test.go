package features

import (
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

func TestAuthorByIDNumbersUnnamedExternalsStably(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)
	r := newMessageRenderer(ap)

	a := r.AuthorByID("U0777EXTL")
	b := r.AuthorByID("U0888EXTM")
	again := r.AuthorByID("U0777EXTL")

	if a == b {
		t.Fatalf("two different externals share the label %q", a)
	}
	if a != again {
		t.Fatalf("the same external got %q then %q", a, again)
	}
	for _, label := range []string{a, b} {
		if !strings.HasPrefix(label, "external user ") || slackID.MatchString(label) {
			t.Fatalf("label %q is not an ID-free external label", label)
		}
	}

	// A message's own username is a real name; it beats numbering.
	got := r.authorName(slack.Message{Msg: slack.Msg{User: "U0999EXTN", Username: "Pat Partner"}})
	if got != "Pat Partner" {
		t.Fatalf("username hint ignored: %q", got)
	}
	// Known users are unaffected.
	if name := r.AuthorByID("U2"); name != "Sarah Chen" {
		t.Fatalf("known user = %q", name)
	}
}

func TestUnresolvedDMRendersByKindNotByID(t *testing.T) {
	labels := map[string]convInfo{
		"D0123ABCD": {Label: unresolvedDM, IsIM: true, Counterpart: "U0123ABCD", Unresolved: true},
	}
	if got := labelFor(labels, "D0123ABCD"); got != "DM (unresolved user)" {
		t.Fatalf("label = %q", got)
	}
	if _, ok := readTarget(labels, "D0123ABCD"); ok {
		t.Fatal("an unresolved DM offered a read target")
	}
	if got := labelFor(labels, "C0NOPE0001"); got != "unnamed conversation" {
		t.Fatalf("missing conversation label = %q", got)
	}
}

func TestFindDiscussionParticipantsAndUnreadNamesCarryNoIDs(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)
	r := newMessageRenderer(ap)

	msgs := []slack.Message{
		{Msg: slack.Msg{User: "U2"}},
		{Msg: slack.Msg{User: "U0777EXTL"}},
		{Msg: slack.Msg{User: "U0888EXTM"}},
		{Msg: slack.Msg{User: "U0777EXTL"}},
		{Msg: slack.Msg{Username: "Deploy Bot"}},
	}
	got := getUniqueParticipants(msgs, r)
	if len(got) != 4 {
		t.Fatalf("participants = %v, want 4 distinct", got)
	}
	for _, name := range got {
		if slackID.MatchString(name) {
			t.Errorf("participant %q carries a Slack ID", name)
		}
	}
	if got[1] == got[2] {
		t.Errorf("two externals collapsed: %v", got)
	}

	users := ap.ProvideUsersMap()
	x := getUserName("U0777EXTL", users, &r.ext)
	y := getUserName("U0888EXTM", users, &r.ext)
	if x == y || slackID.MatchString(x) || slackID.MatchString(y) {
		t.Errorf("getUserName = %q, %q", x, y)
	}
}

func TestNilExternalNamerIsAProgrammingError(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a nil namer labelled an external instead of panicking")
		}
	}()
	var ext *externalNamer
	ext.label("U0777EXTL")
}
