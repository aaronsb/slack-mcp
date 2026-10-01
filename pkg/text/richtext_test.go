package text

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/slack-go/slack"
)

// The cases marked "ported" are slack-stealth-mcp's
// packages/python/tests/test_rich_text.py (MIT, commit 6c28687), case for
// case, asserting the same JSON his dicts describe. Comparison goes through
// JSON with "border" and "offset" dropped: slack-go's RichTextList and
// RichTextPreformatted always emit them (no omitempty) and the Python
// original never sets them.

// generic marshals v and decodes it back to plain maps and slices, minus the
// keys slack-go adds unconditionally.
func generic(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return stripKeys(out, "border", "offset")
}

func stripKeys(v any, keys ...string) any {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range keys {
			delete(x, k)
		}
		for k, child := range x {
			x[k] = stripKeys(child, keys...)
		}
	case []any:
		for i, child := range x {
			x[i] = stripKeys(child, keys...)
		}
	}
	return v
}

func decode(t *testing.T, literal string) any {
	t.Helper()
	var out any
	if err := json.Unmarshal([]byte(literal), &out); err != nil {
		t.Fatalf("bad expected JSON %s: %v", literal, err)
	}
	return out
}

// elementsOf mirrors the Python _elements helper: one rich_text block.
func elementsOf(t *testing.T, in string) []slack.RichTextElement {
	t.Helper()
	b := ToRichText(in)
	if b.Type != slack.MBTRichText {
		t.Fatalf("block type %q", b.Type)
	}
	return b.Elements
}

// inlineOf mirrors the Python _inline helper: exactly one section.
func inlineOf(t *testing.T, in string) any {
	t.Helper()
	els := elementsOf(t, in)
	if len(els) != 1 {
		t.Fatalf("%q: want one element, got %d: %s", in, len(els), mustJSON(els))
	}
	sec, ok := els[0].(*slack.RichTextSection)
	if !ok {
		t.Fatalf("%q: want a rich_text_section, got %T", in, els[0])
	}
	return generic(t, sec.Elements)
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestToRichTextInline(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// ported
		{"plain text is one section", "hello world",
			`[{"type":"text","text":"hello world"}]`},
		{"styles", "*b* _i_ ~s~ `c`", `[
			{"type":"text","text":"b","style":{"bold":true}},
			{"type":"text","text":" "},
			{"type":"text","text":"i","style":{"italic":true}},
			{"type":"text","text":" "},
			{"type":"text","text":"s","style":{"strike":true}},
			{"type":"text","text":" "},
			{"type":"text","text":"c","style":{"code":true}}]`},
		{"nested styles", "*_both_*",
			`[{"type":"text","text":"both","style":{"bold":true,"italic":true}}]`},
		{"markers inside words are literal", "snake_case_name and 2*3*4",
			`[{"type":"text","text":"snake_case_name and 2*3*4"}]`},
		{"standard markdown is normalized first", "**bold** [docs](https://x.com)", `[
			{"type":"text","text":"bold","style":{"bold":true}},
			{"type":"text","text":" "},
			{"type":"link","url":"https://x.com","text":"docs"}]`},
		{"entities", "<!here> <@U01> <#C100|general> <!subteam^S1> <https://a.io>", `[
			{"type":"broadcast","range":"here"},
			{"type":"text","text":" "},
			{"type":"user","user_id":"U01"},
			{"type":"text","text":" "},
			{"type":"channel","channel_id":"C100"},
			{"type":"text","text":" "},
			{"type":"usergroup","usergroup_id":"S1"},
			{"type":"text","text":" "},
			{"type":"link","url":"https://a.io"}]`},
		{"html entities are unescaped", "a &amp; b &lt;3 <https://x.com/?a=1&amp;b=2|q>", `[
			{"type":"text","text":"a & b <3 "},
			{"type":"link","url":"https://x.com/?a=1&b=2","text":"q"}]`},
		{"emoji and times", "at 10:30:45 :thumbsup::skin-tone-3:", `[
			{"type":"text","text":"at 10:30:45 "},
			{"type":"emoji","name":"thumbsup","skin_tone":3}]`},
		{"multiline paragraph keeps newlines", "line one\n\nline two",
			`[{"type":"text","text":"line one\n\nline two"}]`},

		// beyond the ported suite (issue #98)
		{"lone angle bracket stays literal", "a < b and c > d",
			`[{"type":"text","text":"a < b and c > d"}]`},
		{"unknown angle token stays literal and is not rescanned", "<foo *bar*> *baz*", `[
			{"type":"text","text":"<foo *bar*> "},
			{"type":"text","text":"baz","style":{"bold":true}}]`},
		{"asterisk inside a URL", "<https://x.io/a*b*c|q> *x*", `[
			{"type":"link","url":"https://x.io/a*b*c","text":"q"},
			{"type":"text","text":" "},
			{"type":"text","text":"x","style":{"bold":true}}]`},
		{"emoji next to punctuation", ":tada:! (:ok:)", `[
			{"type":"emoji","name":"tada"},
			{"type":"text","text":"! ("},
			{"type":"emoji","name":"ok"},
			{"type":"text","text":")"}]`},
		{"bad skin tone is no emoji", ":x::skin-tone-9:",
			`[{"type":"text","text":":x::skin-tone-9:"}]`},
		{"space-padded markers are literal", "a * not bold * and _ nor _",
			`[{"type":"text","text":"a * not bold * and _ nor _"}]`},
		{"styled link", "*see <https://a.io|docs>*", `[
			{"type":"text","text":"see ","style":{"bold":true}},
			{"type":"link","url":"https://a.io","text":"docs","style":{"bold":true}}]`},
		{"unicode word boundary", "naïve_x_ é*b*",
			`[{"type":"text","text":"naïve_x_ é*b*"}]`},
		{"CRLF input is LF", "one\r\ntwo",
			`[{"type":"text","text":"one\ntwo"}]`},
		{"unterminated fence stays literal", "```code without end",
			`[{"type":"text","text":"` + "```" + `code without end"}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inlineOf(t, c.in)
			want := decode(t, c.want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ToRichText(%q)\n got %s\nwant %s", c.in, mustJSON(got), mustJSON(want))
			}
		})
	}
}

// ported: test_lists_quotes_and_code
func TestToRichTextListsQuotesAndCode(t *testing.T) {
	els := elementsOf(t, "Intro\n- one\n- *two*\n  - nested\n1. first\n> quoted\n```\nx = *1*\n```\nend")

	var kinds []string
	for _, e := range els {
		kinds = append(kinds, generic(t, e).(map[string]any)["type"].(string))
	}
	want := []string{
		"rich_text_section", "rich_text_list", "rich_text_list", "rich_text_list",
		"rich_text_quote", "rich_text_preformatted", "rich_text_section",
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("element types\n got %v\nwant %v", kinds, want)
	}

	bullets, nested, ordered := els[1].(*slack.RichTextList), els[2].(*slack.RichTextList), els[3].(*slack.RichTextList)
	if bullets.Style != "bullet" || bullets.Indent != 0 || len(bullets.Elements) != 2 {
		t.Fatalf("bullets: %s", mustJSON(bullets))
	}
	if nested.Style != "bullet" || nested.Indent != 1 {
		t.Fatalf("nested: %s", mustJSON(nested))
	}
	if ordered.Style != "ordered" {
		t.Fatalf("ordered: %s", mustJSON(ordered))
	}
	// Code is verbatim, not styled.
	pre := generic(t, els[5]).(map[string]any)["elements"]
	if !reflect.DeepEqual(pre, decode(t, `[{"type":"text","text":"x = *1*"}]`)) {
		t.Fatalf("preformatted: %s", mustJSON(pre))
	}
}

// ported: test_bold_line_is_not_a_bullet
func TestToRichTextBoldLineIsNotABullet(t *testing.T) {
	if _, ok := elementsOf(t, "*Status*\nall good")[0].(*slack.RichTextSection); !ok {
		t.Fatal("a bold line was read as a bullet")
	}
}

// ported: test_plain_preview_round_trip
func TestRichTextToPlainRoundTrip(t *testing.T) {
	in := "Hi <@U01>\n- a\n  - b\n> q"
	if got := RichTextToPlain([]slack.Block{ToRichText(in)}); got != in {
		t.Fatalf("round trip\n got %q\nwant %q", got, in)
	}
}

// The full block, list keys included, so the border/offset exemption is the
// only liberty the comparisons take.
func TestToRichTextFullBlock(t *testing.T) {
	got := generic(t, ToRichText("*Plan*\n- a\n  - b\n> q"))
	want := decode(t, `{"type":"rich_text","elements":[
		{"type":"rich_text_section","elements":[{"type":"text","text":"Plan","style":{"bold":true}}]},
		{"type":"rich_text_list","style":"bullet","indent":0,"elements":[
			{"type":"rich_text_section","elements":[{"type":"text","text":"a"}]}]},
		{"type":"rich_text_list","style":"bullet","indent":1,"elements":[
			{"type":"rich_text_section","elements":[{"type":"text","text":"b"}]}]},
		{"type":"rich_text_quote","elements":[{"type":"text","text":"q"}]}]}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("block\n got %s\nwant %s", mustJSON(got), mustJSON(want))
	}
}

func TestToRichTextListEdges(t *testing.T) {
	// Indent caps at 8 levels; ordered markers accept "1)"; "*" bullets need a space.
	els := elementsOf(t, "                        - deep\n2) two\n* star")
	if l := els[0].(*slack.RichTextList); l.Indent != maxListIndent {
		t.Fatalf("indent not capped: %d", l.Indent)
	}
	if l := els[1].(*slack.RichTextList); l.Style != "ordered" {
		t.Fatalf("2) not ordered: %s", mustJSON(l))
	}
	if l := els[2].(*slack.RichTextList); l.Style != "bullet" {
		t.Fatalf("* not a bullet: %s", mustJSON(l))
	}
}

func TestToRichTextBlankMessageHasNoElements(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n", "``````"} {
		if els := ToRichText(in).Elements; len(els) != 0 {
			t.Fatalf("%q: want no elements, got %s", in, mustJSON(els))
		}
	}
}
