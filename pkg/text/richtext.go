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
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/slack-go/slack"
)

var (
	fenceRE   = regexp.MustCompile("(?s)```\n?(.*?)\n?```")
	quoteRE   = regexp.MustCompile(`^(?:>|&gt;) ?(.*)$`)
	bulletRE  = regexp.MustCompile(`^( *)(?:[-•]|\*) +(.*)$`) // "\*(?= ) +" ≡ "\* +"
	orderedRE = regexp.MustCompile(`^( *)(\d+)[.)] +(.*)$`)
	urlRE     = regexp.MustCompile(`^(https?|mailto):`)

	// Entity targets become user/channel/usergroup elements only when they
	// look like IDs; <@name> or <#name> would be rejected by Slack (or
	// render wrong), so they stay literal text instead.
	userIDRE      = regexp.MustCompile(`^[UW][A-Z0-9]+$`)
	channelIDRE   = regexp.MustCompile(`^[CG][A-Z0-9]+$`)
	usergroupIDRE = regexp.MustCompile(`^S[A-Z0-9]+$`)
)

// slackUnescape decodes the only three entities Slack escapes in mrkdwn.
// Deliberate divergence from the Python original's html.unescape (and Go's
// html.UnescapeString): HTML5 also decodes legacy entities without a
// semicolon, so a URL like ?a=1&region=us&notify=1 would become
// "…®ion=us¬ify=1" — corrupted text that Slack accepts, so no retry fires.
var slackUnescape = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace

// maxOrderedNumber bounds what counts as a list number, so "2024. was a
// year" stays a sentence.
const maxOrderedNumber = 999

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
		Text:  slackUnescape(value),
		Style: style.slack(),
	}
}

// angleElement parses the inside of a <...> token, or returns nil when it is
// not an entity (the token then stays literal text).
func angleElement(body string, style inlineStyle) slack.RichTextSectionElement {
	target, label, _ := strings.Cut(body, "|")
	switch {
	case strings.HasPrefix(target, "@") && userIDRE.MatchString(target[1:]):
		return &slack.RichTextSectionUserElement{Type: slack.RTSEUser, UserID: target[1:]}
	case strings.HasPrefix(target, "#") && channelIDRE.MatchString(target[1:]):
		return &slack.RichTextSectionChannelElement{Type: slack.RTSEChannel, ChannelID: target[1:]}
	case strings.HasPrefix(target, "!subteam^") && usergroupIDRE.MatchString(strings.TrimPrefix(target, "!subteam^")):
		return &slack.RichTextSectionUserGroupElement{Type: slack.RTSEUserGroup, UsergroupID: strings.TrimPrefix(target, "!subteam^")}
	// <!date^…> and other <!…> commands are not converted: they fall
	// through to nil and stay literal text (a known gap; a date element
	// needs the fallback text and format parsed out).
	case strings.HasPrefix(target, "!") && broadcastRanges[target[1:]]:
		return &slack.RichTextSectionBroadcastElement{Type: slack.RTSEBroadcast, Range: target[1:]}
	case urlRE.MatchString(target):
		link := &slack.RichTextSectionLinkElement{
			Type:  slack.RTSELink,
			URL:   slackUnescape(target),
			Style: style.slack(),
		}
		if label != "" {
			link.Text = slackUnescape(label)
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

// matchBareURL matches https?://[^\s<>]+ at i, not inside a word, minus
// trailing sentence punctuation and any unmatched closing parenthesis. It
// returns the end of the URL or -1. Code spans, fences and <…> links never
// reach here: the scanner consumes them first.
func matchBareURL(s []rune, i int) int {
	if prev := runeAt(s, i-1); prev != 0 && isWord(prev) {
		return -1
	}
	rest := string(s[i:min(i+8, len(s))])
	var scheme int
	switch {
	case strings.HasPrefix(rest, "https://"):
		scheme = 8
	case strings.HasPrefix(rest, "http://"):
		scheme = 7
	default:
		return -1
	}
	end := i + scheme
	for end < len(s) && !unicode.IsSpace(s[end]) && s[end] != '<' && s[end] != '>' {
		end++
	}
	for end > i+scheme {
		last := s[end-1]
		if strings.ContainsRune(".,;:!?", last) {
			end--
			continue
		}
		if last == ')' {
			open, closed := 0, 0
			for _, r := range s[i:end] {
				switch r {
				case '(':
					open++
				case ')':
					closed++
				}
			}
			if closed > open {
				end--
				continue
			}
		}
		break
	}
	if end == i+scheme {
		return -1
	}
	return end
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
		case 'h':
			if e := matchBareURL(runes, i); e >= 0 {
				end, items = e, []slack.RichTextSectionElement{&slack.RichTextSectionLinkElement{
					Type:  slack.RTSELink,
					URL:   slackUnescape(string(runes[i:e])),
					Style: style.slack(),
				}}
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

// listLine is one line recognized as a list item.
type listLine struct {
	style  slack.RichTextListElementType
	indent int
	number int // ordered lists only
	text   string
}

// parseListLine recognizes a bullet or ordered item. An ordered line counts
// only when its number is at most maxOrderedNumber and it either starts a
// list (0 or 1) or continues an ordered run, so a sentence like "2024. was a
// year" or "3. is my lucky number" stays text.
func parseListLine(line string, orderedRun bool) (listLine, bool) {
	if m := bulletRE.FindStringSubmatch(line); m != nil {
		return listLine{style: slack.RTEListBullet, indent: min(len(m[1])/2, maxListIndent), text: m[2]}, true
	}
	if m := orderedRE.FindStringSubmatch(line); m != nil {
		n, err := strconv.Atoi(m[2])
		if err != nil || n > maxOrderedNumber || (n > 1 && !orderedRun) {
			return listLine{}, false
		}
		return listLine{style: slack.RTEListOrdered, indent: min(len(m[1])/2, maxListIndent), number: n, text: m[3]}, true
	}
	return listLine{}, false
}

// parseLines groups non-code lines into sections, quotes, and lists.
//
// Beyond the Python original: blank lines between list items do not split
// the list (agents write "1. a\n\n2. b"), an ordered list carries its
// starting number as offset (slack-go would otherwise send 0 and Slack
// would renumber "2." after a sub-list as "1."), and items that parse to
// nothing are dropped rather than sent as empty sections Slack may reject.
func parseLines(lines []string) []slack.RichTextElement {
	var (
		blocks     []slack.RichTextElement
		sec, quote []string
		items      []string
		listStyle  slack.RichTextListElementType
		listIndent int
		listStart  int
		orderedRun bool
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
		if len(items) == 0 {
			return
		}
		list := &slack.RichTextList{Type: slack.RTEList, Style: listStyle, Indent: listIndent}
		if listStyle == slack.RTEListOrdered && listStart > 1 {
			list.Offset = listStart - 1
		}
		for _, item := range items {
			if sec := section(item); len(sec.Elements) > 0 {
				list.Elements = append(list.Elements, sec)
			}
		}
		if len(list.Elements) > 0 {
			blocks = append(blocks, list)
		}
		items = nil
	}
	// continuesList reports whether the next non-blank line after i is a
	// list item, so blank lines inside a list are skipped.
	continuesList := func(i int) bool {
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			_, ok := parseListLine(next, orderedRun)
			return ok
		}
		return false
	}

	for i, line := range lines {
		if m := quoteRE.FindStringSubmatch(line); m != nil {
			flushSection()
			flushList()
			orderedRun = false
			quote = append(quote, m[1])
			continue
		}

		if li, ok := parseListLine(line, orderedRun); ok {
			flushSection()
			flushQuote()
			if len(items) > 0 && (li.style != listStyle || li.indent != listIndent) {
				flushList()
			}
			if len(items) == 0 {
				listStart = li.number
			}
			listStyle, listIndent = li.style, li.indent
			if li.style == slack.RTEListOrdered {
				orderedRun = true
			}
			items = append(items, li.text)
			continue
		}

		if strings.TrimSpace(line) == "" && len(items) > 0 && continuesList(i) {
			continue
		}

		flushQuote()
		flushList()
		if strings.TrimSpace(line) != "" {
			orderedRun = false
		}
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
					&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: slackUnescape(code)},
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
