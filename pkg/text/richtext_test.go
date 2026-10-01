package text

import (
	"encoding/json"
	"reflect"
	"strings"
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
		{"entities", "<!here> <@U0123ABCD> <#C0123ABCD|general> <!subteam^S0123ABCD> <@W0123ABCD> <https://a.io>", `[
			{"type":"broadcast","range":"here"},
			{"type":"text","text":" "},
			{"type":"user","user_id":"U0123ABCD"},
			{"type":"text","text":" "},
			{"type":"channel","channel_id":"C0123ABCD"},
			{"type":"text","text":" "},
			{"type":"usergroup","usergroup_id":"S0123ABCD"},
			{"type":"text","text":" "},
			{"type":"user","user_id":"W0123ABCD"},
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
		// review of #117: only Slack's three entities decode (B1)
		{"legacy entities in a labelled link URL survive", "<https://x.io/?orgId=1&region=us-east&notify=1&param=2&timestamp=1|dash>",
			`[{"type":"link","url":"https://x.io/?orgId=1&region=us-east&notify=1&param=2&timestamp=1","text":"dash"}]`},
		{"legacy entities in an unlabelled link URL survive", "<https://x.io/?a=1&amp;region=us&notify=1>",
			`[{"type":"link","url":"https://x.io/?a=1&region=us&notify=1"}]`},
		{"legacy entities in a bare URL survive", "https://x.io/?a=1&region=us&param=2",
			`[{"type":"link","url":"https://x.io/?a=1&region=us&param=2"}]`},
		{"legacy entities in text survive", "AT&T &copy; &region=us &notify &para",
			`[{"type":"text","text":"AT&T &copy; &region=us &notify &para"}]`},
		{"only Slack's entities decode", "&lt;b&gt; &amp;amp; &quot;",
			`[{"type":"text","text":"<b> &amp; &quot;"}]`},

		// bare URLs become links (W3)
		{"bare URL", "see https://x.io/a now", `[
			{"type":"text","text":"see "},
			{"type":"link","url":"https://x.io/a"},
			{"type":"text","text":" now"}]`},
		{"bare URL drops trailing punctuation", "go to http://x.io/a.", `[
			{"type":"text","text":"go to "},
			{"type":"link","url":"http://x.io/a"},
			{"type":"text","text":"."}]`},
		{"bare URL keeps balanced parens, drops unmatched", "(https://x.io/a_(b))!", `[
			{"type":"text","text":"("},
			{"type":"link","url":"https://x.io/a_(b)"},
			{"type":"text","text":")!"}]`},
		{"bare URL inside bold keeps the style", "*https://x.io*",
			`[{"type":"link","url":"https://x.io","style":{"bold":true}}]`},
		{"bare URL in a code span stays code", "`https://x.io`",
			`[{"type":"text","text":"https://x.io","style":{"code":true}}]`},
		{"URL glued to a word is not a link", "xhttps://x.io and https://",
			`[{"type":"text","text":"xhttps://x.io and https://"}]`},

		// entities only for ID-shaped targets; date commands literal (W4)
		{"name-shaped mentions stay literal", "<@bob> <#general> <!subteam^eng>",
			`[{"type":"text","text":"<@bob> <#general> <!subteam^eng>"}]`},
		{"uppercase words and short IDs stay literal", "<#GENERAL> <@USER> <@U01> <#C100> <!subteam^SABCDEFGHI>",
			`[{"type":"text","text":"<#GENERAL> <@USER> <@U01> <#C100> <!subteam^SABCDEFGHI>"}]`},
		{"bare URL drops trailing quotes and unmatched brackets", `"https://x.io/a" [https://x.io/b] {https://x.io/c}`, `[
			{"type":"text","text":"\""},
			{"type":"link","url":"https://x.io/a"},
			{"type":"text","text":"\" ["},
			{"type":"link","url":"https://x.io/b"},
			{"type":"text","text":"] {"},
			{"type":"link","url":"https://x.io/c"},
			{"type":"text","text":"}"}]`},
		{"bare URL keeps balanced brackets", "https://x.io/a[1]{2}",
			`[{"type":"link","url":"https://x.io/a[1]{2}"}]`},
		{"bare URL keeps a semicolon that ends an entity", "https://x.io/?a=1&amp;",
			`[{"type":"link","url":"https://x.io/?a=1&"}]`},
		{"bare URL drops a plain trailing semicolon", "https://x.io/a;",
			`[{"type":"link","url":"https://x.io/a"},{"type":"text","text":";"}]`},
		{"date command stays literal", "<!date^1392734382^{date}|Feb 18>",
			`[{"type":"text","text":"<!date^1392734382^{date}|Feb 18>"}]`},

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
	in := "Hi <@U0123ABCD>\n- a\n  - b\n> q"
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
	els := elementsOf(t, "                        - deep\n1) one\n* star")
	if l := els[0].(*slack.RichTextList); l.Indent != maxListIndent {
		t.Fatalf("indent not capped: %d", l.Indent)
	}
	if l := els[1].(*slack.RichTextList); l.Style != "ordered" {
		t.Fatalf("1) not ordered: %s", mustJSON(l))
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

func orderedLists(els []slack.RichTextElement) []*slack.RichTextList {
	var out []*slack.RichTextList
	for _, e := range els {
		if l, ok := e.(*slack.RichTextList); ok && l.Style == slack.RTEListOrdered {
			out = append(out, l)
		}
	}
	return out
}

// W1: blank lines between items keep one list.
func TestToRichTextOrderedListSurvivesBlankLines(t *testing.T) {
	els := elementsOf(t, "1. a\n\n2. b\n\n\n3. c\n\nafter")
	if len(els) != 2 {
		t.Fatalf("want list + section, got %s", mustJSON(els))
	}
	l, ok := els[0].(*slack.RichTextList)
	if !ok || len(l.Elements) != 3 || l.Offset != 0 {
		t.Fatalf("want one ordered list of 3 at offset 0, got %s", mustJSON(els[0]))
	}
	if _, ok := els[1].(*slack.RichTextSection); !ok {
		t.Fatalf("trailing paragraph: %s", mustJSON(els[1]))
	}
}

// W1: a sub-list splits the ordered list; the continuation keeps its number.
func TestToRichTextOrderedListResumesAfterSubList(t *testing.T) {
	els := elementsOf(t, "1. a\n  - sub\n\n2. b\n3. c")
	lists := orderedLists(els)
	if len(els) != 3 || len(lists) != 2 {
		t.Fatalf("want ordered, bullet, ordered; got %s", mustJSON(els))
	}
	if lists[0].Offset != 0 || lists[1].Offset != 1 || len(lists[1].Elements) != 2 {
		t.Fatalf("offsets %d/%d, second list %d items", lists[0].Offset, lists[1].Offset, len(lists[1].Elements))
	}
	if raw := mustJSON(lists[1]); !strings.Contains(raw, `"offset":1`) {
		t.Fatalf("offset not on the wire: %s", raw)
	}
}

// W1: numbered sentences are not lists.
func TestToRichTextNumberedSentencesAreText(t *testing.T) {
	for _, in := range []string{"2024. was a year", "3. is my lucky number", "1000. too many"} {
		els := elementsOf(t, in)
		if _, ok := els[0].(*slack.RichTextSection); !ok || len(els) != 1 {
			t.Fatalf("%q read as a list: %s", in, mustJSON(els))
		}
	}
}

// W4: an item that parses to nothing is dropped, not sent empty.
func TestToRichTextDropsEmptyListItems(t *testing.T) {
	els := elementsOf(t, "- a\n- \n- b")
	l, ok := els[0].(*slack.RichTextList)
	if !ok || len(l.Elements) != 2 {
		t.Fatalf("want 2 items, got %s", mustJSON(els))
	}
	if els := elementsOf(t, "- "); len(els) != 0 {
		t.Fatalf("an all-empty list was kept: %s", mustJSON(els))
	}
}

// The offset follows the first item emitted, not the first line.
func TestToRichTextOffsetFollowsFirstEmittedItem(t *testing.T) {
	els := elementsOf(t, "1. \n2. b\n3. c")
	l, ok := els[0].(*slack.RichTextList)
	if !ok || len(els) != 1 || len(l.Elements) != 2 || l.Offset != 1 {
		t.Fatalf("want one list of 2 at offset 1, got %s", mustJSON(els))
	}
}

// A number that does not climb restarts the list.
func TestToRichTextOrderedListRestarts(t *testing.T) {
	els := elementsOf(t, "1) a\n2) b\n\n1. restart\n2. again")
	lists := orderedLists(els)
	if len(els) != 2 || len(lists) != 2 || len(lists[0].Elements) != 2 || len(lists[1].Elements) != 2 || lists[1].Offset != 0 {
		t.Fatalf("want two ordered lists of 2, got %s", mustJSON(els))
	}
}

// Blank lines around a list do not leak into the next paragraph.
func TestToRichTextSectionAfterListHasNoLeadingBlank(t *testing.T) {
	els := elementsOf(t, "- a\n\nafter\n\nmore")
	if len(els) != 2 {
		t.Fatalf("want list + section, got %s", mustJSON(els))
	}
	got := generic(t, els[1].(*slack.RichTextSection).Elements)
	if !reflect.DeepEqual(got, decode(t, `[{"type":"text","text":"after\n\nmore"}]`)) {
		t.Fatalf("section: %s", mustJSON(got))
	}
}
