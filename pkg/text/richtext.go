package text

// Outbound rich text: convert Slack mrkdwn to a Block Kit rich_text block.
//
// say posts the block alongside the mrkdwn text fallback, so a message
// renders the way Slack's own composer would send it, and scheduled send
// needs it because Slack's drafts API stores bodies only as rich_text.
//
// Handles the subset of mrkdwn agents write: *bold*, _italic_, ~strike~,
// `code`, ```code blocks```, > quotes, - / • / * / 1. lists, <url|text>
// links, <@U…> / <#C…> / <!here> / <!subteam^…> entities, and :emoji:.
// Plain @name and #name stay plain text, exactly as a text-only post leaves
// them today; the converter never resolves names to IDs.
//
// Ported from Clayton Chancey's slack-stealth-mcp (MIT License,
// https://github.com/forayconsulting/slack-stealth-mcp, commit 6c28687,
// packages/python/src/slack_stealth_mcp/rich_text.py). Its test suite is
// ported case for case in richtext_test.go.
//
// The Python original finds inline tokens with one regex built on lookbehind
// and lookahead, which Go's RE2 does not support. scanInline is a hand-written
// scanner with the same semantics: tokens are tried left to right, each
// position takes the first token that matches there, and a style marker
// counts only on a word boundary — not preceded by a word character or the
// marker itself, not followed by a space, closed by a marker not preceded by
// a space and not followed by a word character or the marker.

import (
	"html"
	"regexp"
	"strings"
	"unicode"

	"github.com/slack-go/slack"
)

var (
	fenceRE   = regexp.MustCompile("(?s)```\n?(.*?)\n?```")
	quoteRE   = regexp.MustCompile(`^(?:>|&gt;) ?(.*)$`)
	bulletRE  = regexp.MustCompile(`^( *)(?:[-•]|\*) +(.*)$`) // "\*(?= ) +" ≡ "\* +"
	orderedRE = regexp.MustCompile(`^( *)\d+[.)] +(.*)$`)
	urlRE     = regexp.MustCompile(`^(https?|mailto):`)
)

// maxListIndent caps nested list depth; Slack renders at most 8 levels.
const maxListIndent = 8

var broadcastRanges = map[string]bool{"here": true, "channel": true, "everyone": true}

// inlineStyle is the style a run of text inherits from enclosing markers.
type inlineStyle struct {
	bold, italic, strike, code bool
}

func (s inlineStyle) slack() *slack.RichTextSectionTextStyle {
	if s == (inlineStyle{}) {
		return nil
	}
	return &slack.RichTextSectionTextStyle{Bold: s.bold, Italic: s.italic, Strike: s.strike, Code: s.code}
}

func textElement(value string, style inlineStyle) *slack.RichTextSectionTextElement {
	return &slack.RichTextSectionTextElement{
		Type:  slack.RTSEText,
		Text:  html.UnescapeString(value),
		Style: style.slack(),
	}
}

// angleElement parses the inside of a <...> token, or returns nil when it is
// not an entity (the token then stays literal text).
func angleElement(body string, style inlineStyle) slack.RichTextSectionElement {
	target, label, _ := strings.Cut(body, "|")
	switch {
	case strings.HasPrefix(target, "@") && len(target) > 1:
		return &slack.RichTextSectionUserElement{Type: slack.RTSEUser, UserID: target[1:]}
	case strings.HasPrefix(target, "#") && len(target) > 1:
		return &slack.RichTextSectionChannelElement{Type: slack.RTSEChannel, ChannelID: target[1:]}
	case strings.HasPrefix(target, "!subteam^"):
		return &slack.RichTextSectionUserGroupElement{Type: slack.RTSEUserGroup, UsergroupID: strings.TrimPrefix(target, "!subteam^")}
	case strings.HasPrefix(target, "!") && broadcastRanges[target[1:]]:
		return &slack.RichTextSectionBroadcastElement{Type: slack.RTSEBroadcast, Range: target[1:]}
	case urlRE.MatchString(target):
		link := &slack.RichTextSectionLinkElement{
			Type:  slack.RTSELink,
			URL:   html.UnescapeString(target),
			Style: style.slack(),
		}
		if label != "" {
			link.Text = html.UnescapeString(label)
		}
		return link
	}
	return nil
}

// isWord matches Python's Unicode \w: letters, numbers, underscore.
func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// runeAt returns s[i], or 0 when i is out of range (string boundary).
func runeAt(s []rune, i int) rune {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

// indexAny returns the first index ≥ from whose rune is in set, or -1.
func indexAny(s []rune, from int, set string) int {
	for j := from; j < len(s); j++ {
		if strings.ContainsRune(set, s[j]) {
			return j
		}
	}
	return -1
}

// matchDelimited matches a token that opens and closes with the same rune,
// body free of that rune and newlines, at i. It returns the body's end
// (index of the closing rune) or -1.
func matchDelimited(s []rune, i int, delim rune) int {
	j := indexAny(s, i+1, string(delim)+"\n")
	if j < 0 || s[j] != delim || j == i+1 {
		return -1
	}
	return j
}

// matchStyled matches *x*, _x_ or ~x~ at i with the word-boundary rules
// (?<![\w M])M(?! )([^M\n]+?)(?<! )M(?![\w M]). Because the body cannot
// contain the marker, the only candidate close is the first marker after
// the open; if it fails the boundary checks, nothing matches here.
func matchStyled(s []rune, i int, marker rune) int {
	if prev := runeAt(s, i-1); prev != 0 && (isWord(prev) || prev == marker) {
		return -1
	}
	if runeAt(s, i+1) == ' ' {
		return -1
	}
	j := matchDelimited(s, i, marker)
	if j < 0 || s[j-1] == ' ' {
		return -1
	}
	if next := runeAt(s, j+1); next != 0 && (isWord(next) || next == marker) {
		return -1
	}
	return j
}

func isEmojiNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("_+'-", r)
}

// matchEmoji matches (?<![\w:]):name:(?::skin-tone-[2-6]:)?(?![\w:]) at i,
// returning the match end, the name and the skin tone (0 when absent).
func matchEmoji(s []rune, i int) (end int, name string, skin int) {
	if prev := runeAt(s, i-1); prev != 0 && (isWord(prev) || prev == ':') {
		return -1, "", 0
	}
	k := i + 1
	for k < len(s) && isEmojiNameRune(s[k]) {
		k++
	}
	if k == i+1 || runeAt(s, k) != ':' {
		return -1, "", 0
	}
	name = string(s[i+1 : k])
	boundary := func(at int) bool {
		next := runeAt(s, at)
		return next == 0 || !(isWord(next) || next == ':')
	}

	// The optional skin tone is greedy: try with it first, then without.
	const tone = ":skin-tone-"
	if t := k + 1 + len(tone); t+1 < len(s) && string(s[k+1:t]) == tone &&
		s[t] >= '2' && s[t] <= '6' && s[t+1] == ':' && boundary(t+2) {
		return t + 2, name, int(s[t] - '0')
	}
	if boundary(k + 1) {
		return k + 1, name, 0
	}
	return -1, "", 0
}

// parseInline parses one run of inline mrkdwn into rich_text section elements.
func parseInline(s string, style inlineStyle) []slack.RichTextSectionElement {
	runes := []rune(s)
	var out []slack.RichTextSectionElement
	pos := 0

	emitText := func(upto int) {
		if upto > pos {
			out = append(out, textElement(string(runes[pos:upto]), style))
		}
	}

	for i := 0; i < len(runes); {
		var (
			end   = -1
			items []slack.RichTextSectionElement
		)
		switch runes[i] {
		case '<':
			if j := indexAny(runes, i+1, "<>\n"); j > i+1 && runes[j] == '>' {
				if el := angleElement(string(runes[i+1:j]), style); el != nil {
					end, items = j+1, []slack.RichTextSectionElement{el}
				} else {
					// Not an entity: the whole token stays literal and is not
					// rescanned, as with the regex it replaces.
					i = j + 1
					continue
				}
			}
		case '`':
			if j := matchDelimited(runes, i, '`'); j >= 0 {
				inner := style
				inner.code = true
				end, items = j+1, []slack.RichTextSectionElement{textElement(string(runes[i+1:j]), inner)}
			}
		case '*', '_', '~':
			if j := matchStyled(runes, i, runes[i]); j >= 0 {
				inner := style
				switch runes[i] {
				case '*':
					inner.bold = true
				case '_':
					inner.italic = true
				case '~':
					inner.strike = true
				}
				end, items = j+1, parseInline(string(runes[i+1:j]), inner)
			}
		case ':':
			if e, name, skin := matchEmoji(runes, i); e >= 0 {
				end, items = e, []slack.RichTextSectionElement{
					&slack.RichTextSectionEmojiElement{Type: slack.RTSEEmoji, Name: name, SkinTone: skin},
				}
			}
		}
		if end < 0 {
			i++
			continue
		}
		emitText(i)
		out = append(out, items...)
		pos, i = end, end
	}
	emitText(len(runes))
	return mergeText(out)
}

func sameStyle(a, b *slack.RichTextSectionTextStyle) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// mergeText joins adjacent text elements that share a style.
func mergeText(elements []slack.RichTextSectionElement) []slack.RichTextSectionElement {
	merged := make([]slack.RichTextSectionElement, 0, len(elements))
	for _, el := range elements {
		cur, ok := el.(*slack.RichTextSectionTextElement)
		if ok && len(merged) > 0 {
			if prev, ok := merged[len(merged)-1].(*slack.RichTextSectionTextElement); ok && sameStyle(prev.Style, cur.Style) {
				prev.Text += cur.Text
				continue
			}
		}
		merged = append(merged, el)
	}
	return merged
}

func section(s string) *slack.RichTextSection {
	return &slack.RichTextSection{Type: slack.RTESection, Elements: parseInline(s, inlineStyle{})}
}

// parseLines groups non-code lines into sections, quotes, and lists.
func parseLines(lines []string) []slack.RichTextElement {
	var (
		blocks     []slack.RichTextElement
		sec, quote []string
		items      []string
		listStyle  slack.RichTextListElementType
		listIndent int
	)

	flushSection := func() {
		if len(sec) > 0 {
			blocks = append(blocks, section(strings.Join(sec, "\n")))
			sec = nil
		}
	}
	flushQuote := func() {
		if len(quote) > 0 {
			blocks = append(blocks, &slack.RichTextQuote{
				Type:     slack.RTEQuote,
				Elements: parseInline(strings.Join(quote, "\n"), inlineStyle{}),
			})
			quote = nil
		}
	}
	flushList := func() {
		if len(items) > 0 {
			list := &slack.RichTextList{Type: slack.RTEList, Style: listStyle, Indent: listIndent}
			for _, item := range items {
				list.Elements = append(list.Elements, section(item))
			}
			blocks = append(blocks, list)
			items = nil
		}
	}

	for _, line := range lines {
		if m := quoteRE.FindStringSubmatch(line); m != nil {
			flushSection()
			flushList()
			quote = append(quote, m[1])
			continue
		}

		style := slack.RTEListBullet
		m := bulletRE.FindStringSubmatch(line)
		if m == nil {
			style = slack.RTEListOrdered
			m = orderedRE.FindStringSubmatch(line)
		}
		if m != nil {
			flushSection()
			flushQuote()
			indent := min(len(m[1])/2, maxListIndent)
			if len(items) > 0 && (style != listStyle || indent != listIndent) {
				flushList()
			}
			listStyle, listIndent = style, indent
			items = append(items, m[2])
			continue
		}

		flushQuote()
		flushList()
		sec = append(sec, line)
	}

	flushSection()
	flushQuote()
	flushList()
	return blocks
}

func trimBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func hasChildren(el slack.RichTextElement) bool {
	switch e := el.(type) {
	case *slack.RichTextSection:
		return len(e.Elements) > 0
	case *slack.RichTextList:
		return len(e.Elements) > 0
	case *slack.RichTextQuote:
		return len(e.Elements) > 0
	case *slack.RichTextPreformatted:
		return len(e.Elements) > 0
	}
	return true
}

// ToRichText converts a mrkdwn message to exactly one rich_text block.
// Standard Markdown is normalized first (NormalizeMrkdwn), so **bold** and
// [text](url) come out the same as in the text fallback. CRLF line endings
// are treated as LF. A blank message yields a block with no elements.
func ToRichText(s string) *slack.RichTextBlock {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = NormalizeMrkdwn(s)

	var elements []slack.RichTextElement
	pos := 0
	for _, m := range fenceRE.FindAllStringSubmatchIndex(s, -1) {
		elements = append(elements, parseLines(trimBlankLines(strings.Split(s[pos:m[0]], "\n")))...)
		if code := s[m[2]:m[3]]; code != "" {
			elements = append(elements, &slack.RichTextPreformatted{
				Type: slack.RTEPreformatted,
				Elements: []slack.RichTextSectionElement{
					&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: html.UnescapeString(code)},
				},
			})
		}
		pos = m[1]
	}
	elements = append(elements, parseLines(trimBlankLines(strings.Split(s[pos:], "\n")))...)

	kept := make([]slack.RichTextElement, 0, len(elements))
	for _, el := range elements {
		if hasChildren(el) {
			kept = append(kept, el)
		}
	}
	return &slack.RichTextBlock{Type: slack.MBTRichText, Elements: kept}
}

// RichTextToPlain flattens rich_text blocks to readable text for previews.
// Entities come back as raw tag syntax (<@U…>, <#C…>); a caller showing the
// result to an agent must pass it through ResolveTags first, since internal
// IDs never reach a user-visible string.
func RichTextToPlain(blocks []slack.Block) string {
	var lines []string
	for _, b := range blocks {
		var rt *slack.RichTextBlock
		switch v := b.(type) {
		case *slack.RichTextBlock:
			rt = v
		case slack.RichTextBlock:
			rt = &v
		default:
			continue
		}
		for _, el := range rt.Elements {
			switch e := el.(type) {
			case *slack.RichTextList:
				pad := strings.Repeat("  ", e.Indent)
				for _, item := range e.Elements {
					if sec, ok := item.(*slack.RichTextSection); ok {
						lines = append(lines, pad+"- "+plainInline(sec.Elements))
					}
				}
			case *slack.RichTextQuote:
				for _, line := range strings.Split(plainInline(e.Elements), "\n") {
					lines = append(lines, "> "+line)
				}
			case *slack.RichTextPreformatted:
				lines = append(lines, "```"+plainInline(e.Elements)+"```")
			case *slack.RichTextSection:
				lines = append(lines, plainInline(e.Elements))
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func plainInline(elements []slack.RichTextSectionElement) string {
	var b strings.Builder
	for _, el := range elements {
		switch e := el.(type) {
		case *slack.RichTextSectionTextElement:
			b.WriteString(e.Text)
		case *slack.RichTextSectionLinkElement:
			if e.Text != "" {
				b.WriteString(e.Text)
			} else {
				b.WriteString(e.URL)
			}
		case *slack.RichTextSectionUserElement:
			b.WriteString("<@" + e.UserID + ">")
		case *slack.RichTextSectionChannelElement:
			b.WriteString("<#" + e.ChannelID + ">")
		case *slack.RichTextSectionBroadcastElement:
			b.WriteString("<!" + e.Range + ">")
		case *slack.RichTextSectionUserGroupElement:
			b.WriteString("<!subteam^" + e.UsergroupID + ">")
		case *slack.RichTextSectionEmojiElement:
			b.WriteString(":" + e.Name + ":")
		}
	}
	return b.String()
}
