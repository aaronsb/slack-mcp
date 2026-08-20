package text_test

import (
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/text"
)

func resolver(kind text.TagKind, id, label string) (string, bool) {
	switch {
	case kind == text.TagUser && id == "U01AAAAA1":
		return "@Dana Okafor", true
	case kind == text.TagChannel && id == "C01AAAAA1":
		return "#eng", true
	}
	return "", false
}

func TestTagsRewriteThroughTheResolver(t *testing.T) {
	in := "ping <@U01AAAAA1> in <#C01AAAAA1|eng-old> and <!here> — thanks <!subteam^S123|@platform-team>"
	got := text.ResolveTags(in, resolver)
	want := "ping @Dana Okafor in #eng and @here — thanks @platform-team"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAnUnresolvableTagKeepsItsRawForm(t *testing.T) {
	in := "who is <@U0UNKNOWN9>?"
	if got := text.ResolveTags(in, resolver); got != in {
		t.Fatalf("unresolved tag was altered: %q", got)
	}
}

func TestPlainTextPassesUntouched(t *testing.T) {
	in := "no tags here, just words"
	if got := text.ResolveTags(in, resolver); got != in {
		t.Fatalf("plain text altered: %q", got)
	}
}
