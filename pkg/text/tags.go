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
	bangTag    = regexp.MustCompile(`<!(here|channel|everyone)>`)
	groupTag   = regexp.MustCompile(`<!subteam\^([A-Z0-9]+)(?:\|@?([^>]*))?>`)
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
	if !strings.ContainsRune(s, '<') {
		return s
	}

	s = userTag.ReplaceAllStringFunc(s, func(m string) string {
		parts := userTag.FindStringSubmatch(m)
		if out, ok := resolve(TagUser, parts[1], parts[2]); ok {
			return out
		}
		return m
	})
	s = channelTag.ReplaceAllStringFunc(s, func(m string) string {
		parts := channelTag.FindStringSubmatch(m)
		if out, ok := resolve(TagChannel, parts[1], parts[2]); ok {
			return out
		}
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
		return m
	})
	s = bangTag.ReplaceAllString(s, "@$1")
	return s
}
