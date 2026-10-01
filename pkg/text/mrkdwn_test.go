package text

import (
	"strings"
	"testing"
)

// Ported case for case from slack-stealth-mcp's
// packages/python/tests/test_mrkdwn.py (MIT, commit 6c28687).

func TestNormalizeMrkdwn(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"double asterisk bold", "this is **bold** text", "this is *bold* text"},
		{"double underscore bold", "this is __bold__ text", "this is *bold* text"},
		{"markdown link", "see [the docs](https://example.com/docs) here", "see <https://example.com/docs|the docs> here"},
		{"header becomes bold line", "# Title\nbody", "*Title*\nbody"},
		{"deeper header becomes bold line", "### Sub Heading\nbody", "*Sub Heading*\nbody"},
		{"inline code preserved", "use `**kwargs` and **bold**", "use `**kwargs` and *bold*"},
		{
			"existing mrkdwn untouched",
			"*bold* _italic_ <@U0123456> <!here> <#C0123456> <https://a.com|link>",
			"*bold* _italic_ <@U0123456> <!here> <#C0123456> <https://a.com|link>",
		},
		// [0](1) style text that is not a link must not be rewritten.
		{"non-http brackets untouched", "array[0](see note)", "array[0](see note)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeMrkdwn(c.in); got != c.want {
				t.Fatalf("NormalizeMrkdwn(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeMrkdwnPreservesCodeFences(t *testing.T) {
	in := "before\n```\n**not bold** [x](https://a.com)\n```\nafter **bold**"
	got := NormalizeMrkdwn(in)
	if !strings.Contains(got, "```\n**not bold** [x](https://a.com)\n```") {
		t.Fatalf("fence rewritten: %q", got)
	}
	if !strings.HasSuffix(got, "after *bold*") {
		t.Fatalf("text after the fence not normalized: %q", got)
	}
}

func TestNormalizeMrkdwnIsIdempotent(t *testing.T) {
	in := "## Head\n**bold** [x](https://a.com) `code` and *done*"
	once := NormalizeMrkdwn(in)
	if twice := NormalizeMrkdwn(once); twice != once {
		t.Fatalf("not idempotent:\nonce  %q\ntwice %q", once, twice)
	}
}
