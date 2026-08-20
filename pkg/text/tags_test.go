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

func TestLabelledBroadcastTagsRewrite(t *testing.T) {
	// Real payloads carry the labelled form.
	in := "<!here|@here> deploy starting, <!channel|@channel> heads up"
	got := text.ResolveTags(in, resolver)
	want := "@here deploy starting, @channel heads up"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPlainTextPassesUntouched(t *testing.T) {
	in := "no tags here, just words"
	if got := text.ResolveTags(in, resolver); got != in {
		t.Fatalf("plain text altered: %q", got)
	}
}

// Slack link syntax — <url|label>, <mailto:...>, <tel:...> — flattens in
// every body: a prose label keeps the url beside it, an elided or
// duplicate label loses to the full url, and bare mailto/tel tags shed
// their scheme.
func TestLinkTagsFlatten(t *testing.T) {
	noResolve := func(kind text.TagKind, id, label string) (string, bool) { return "", false }
	cases := []struct{ in, want string }{
		{"see <https://example.com/doc>", "see https://example.com/doc"},
		{"see <https://example.com/doc|example.com/doc>", "see https://example.com/doc"},
		{"see <https://example.com/a/b/c|example.com/…/c>", "see https://example.com/a/b/c"},
		{"read <https://example.com/x|[Announcement] The new thing>", "read [Announcement] The new thing (https://example.com/x)"},
		{"mail <mailto:a@b.com|a@b.com>", "mail a@b.com"},
		{"mail <mailto:a@b.com>", "mail a@b.com"},
		{"call <tel:+15551234567>", "call +15551234567"},
	}
	for _, c := range cases {
		got, unresolved := text.ResolveTagsReport(c.in, noResolve)
		if got != c.want {
			t.Errorf("ResolveTags(%q) = %q, want %q", c.in, got, c.want)
		}
		if len(unresolved) != 0 {
			t.Errorf("link tag reported as unresolved: %v", unresolved)
		}
	}
}

func TestLinkFlatteningLeavesOtherTagsAlone(t *testing.T) {
	resolve := func(kind text.TagKind, id, label string) (string, bool) {
		if kind == text.TagUser {
			return "@Sarah", true
		}
		return "", false
	}
	got := text.ResolveTags("ping <@U2> about <https://example.com|the doc site>", resolve)
	want := "ping @Sarah about the doc site (https://example.com)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
