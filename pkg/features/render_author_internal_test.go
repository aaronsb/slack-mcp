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
