package text

// Slack message bodies carry tag syntax — <@U123>, <#C123|name>, <!here> —
// that only the official client resolves. The agent has no second window
// (ADR-004), so the render path rewrites every tag it can resolve and
// leaves the raw tag visible when it cannot: a raw ID an agent can see, it
// can ask about; a silently dropped one it cannot.

import (
	"regexp"
	"strings"
)

var (
	userTag    = regexp.MustCompile(`<@([UW][A-Z0-9]+)(?:\|([^>]*))?>`)
	channelTag = regexp.MustCompile(`<#([CDG][A-Z0-9]+)(?:\|([^>]*))?>`)
	bangTag    = regexp.MustCompile(`<!(here|channel|everyone)(?:\|@?[^>]*)?>`)
	groupTag   = regexp.MustCompile(`<!subteam\^([A-Z0-9]+)(?:\|@?([^>]*))?>`)
	linkTag    = regexp.MustCompile(`<((?:https?://|mailto:|tel:)[^>|]*)(?:\|([^>]*))?>`)
)

// TagKind names what a tag refers to, for the resolver callback.
type TagKind string

const (
	TagUser    TagKind = "user"
	TagChannel TagKind = "channel"
	TagGroup   TagKind = "group"
)

// ResolveTags rewrites Slack tag syntax through the resolver. The resolver
// returns the replacement text and whether it resolved; an unresolved tag
// keeps its original form. <!here>-style broadcasts need no resolver and
// always rewrite.
func ResolveTags(s string, resolve func(kind TagKind, id, label string) (string, bool)) string {
	out, _ := ResolveTagsReport(s, resolve)
	return out
}

// ResolveTagsReport is ResolveTags plus the repair queue: the second
// return lists every tag left raw in the output, so callers can carry
// misses as data (ADR-004's unresolved field) instead of losing them in
// the body.
func ResolveTagsReport(s string, resolve func(kind TagKind, id, label string) (string, bool)) (string, []string) {
	if !strings.ContainsRune(s, '<') {
		return s, nil
	}
	var unresolved []string

	s = userTag.ReplaceAllStringFunc(s, func(m string) string {
		parts := userTag.FindStringSubmatch(m)
		if out, ok := resolve(TagUser, parts[1], parts[2]); ok {
			return out
		}
		unresolved = append(unresolved, m)
		return m
	})
	s = channelTag.ReplaceAllStringFunc(s, func(m string) string {
		parts := channelTag.FindStringSubmatch(m)
		if out, ok := resolve(TagChannel, parts[1], parts[2]); ok {
			return out
		}
		unresolved = append(unresolved, m)
		return m
	})
	s = groupTag.ReplaceAllStringFunc(s, func(m string) string {
		parts := groupTag.FindStringSubmatch(m)
		if out, ok := resolve(TagGroup, parts[1], parts[2]); ok {
			return out
		}
		if parts[2] != "" {
			return "@" + parts[2]
		}
		unresolved = append(unresolved, m)
		return m
	})
	s = bangTag.ReplaceAllString(s, "@$1")
	s = linkTag.ReplaceAllStringFunc(s, func(m string) string {
		parts := linkTag.FindStringSubmatch(m)
		return flattenLink(parts[1], parts[2])
	})
	return s, unresolved
}

// flattenLink renders Slack's <url|label> form. A label that is just the
// url again — verbatim or elided — loses to the full url; any other label
// is the author's words and keeps the url beside it.
func flattenLink(url, label string) string {
	bare := strings.TrimPrefix(strings.TrimPrefix(url, "mailto:"), "tel:")
	if label == "" || labelElidesURL(label, bare) {
		return bare
	}
	return label + " (" + bare + ")"
}

// labelElidesURL reports whether the label is the url in elided form:
// every fragment between ellipsis markers appears in the url, in order.
func labelElidesURL(label, url string) bool {
	l := strings.NewReplacer("[…]", "…", "...", "…").Replace(strings.TrimSpace(label))
	rest := url
	for _, frag := range strings.Split(l, "…") {
		frag = strings.TrimSpace(frag)
		if frag == "" {
			continue
		}
		i := strings.Index(rest, frag)
		if i < 0 {
			return false
		}
		rest = rest[i+len(frag):]
	}
	return true
}
