package text

// Outbound formatting: agents habitually write GitHub-style Markdown
// (**bold**, [text](url), ## headers) which Slack renders literally.
// NormalizeMrkdwn rewrites the common cases to Slack mrkdwn, leaving code
// spans and already-correct mrkdwn untouched.
//
// Ported from Clayton Chancey's slack-stealth-mcp (MIT License,
// https://github.com/forayconsulting/slack-stealth-mcp, commit 6c28687,
// packages/python/src/slack_stealth_mcp/mrkdwn.py). Its test suite is
// ported case for case in mrkdwn_test.go.

import "regexp"

var (
	// Fenced code blocks (```...```) and inline code (`...`) are never rewritten.
	mdCodeRE = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")

	mdBoldRE           = regexp.MustCompile(`(?s)\*\*(.+?)\*\*`)
	mdBoldUnderscoreRE = regexp.MustCompile(`(?s)__(.+?)__`)
	// Only real links convert; <url|text> output cannot re-match.
	mdLinkRE   = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	mdHeaderRE = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
)

func convertMarkdownSegment(s string) string {
	s = mdBoldRE.ReplaceAllString(s, "*${1}*")
	s = mdBoldUnderscoreRE.ReplaceAllString(s, "*${1}*")
	s = mdLinkRE.ReplaceAllString(s, "<${2}|${1}>")
	s = mdHeaderRE.ReplaceAllString(s, "*${1}*")
	return s
}

// NormalizeMrkdwn converts common Markdown to Slack mrkdwn: **x** and __x__
// become *x*, [t](http…) becomes <url|t>, and a # heading line becomes a
// bold line. Code blocks and inline code spans are preserved verbatim. The
// conversion is idempotent.
func NormalizeMrkdwn(s string) string {
	var out []byte
	last := 0
	for _, m := range mdCodeRE.FindAllStringIndex(s, -1) {
		out = append(out, convertMarkdownSegment(s[last:m[0]])...)
		out = append(out, s[m[0]:m[1]]...)
		last = m[1]
	}
	out = append(out, convertMarkdownSegment(s[last:])...)
	return string(out)
}
