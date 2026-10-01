package features

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// searchFilters are the structured filters of messages query=, validated and
// resolved before any network call. Everything here is names: the rendered
// Slack query and the echo never carry an ID.
type searchFilters struct {
	// channels are in: tokens without the "in:" prefix: "#name" or "@handle".
	channels    []string
	inResolved  []map[string]interface{}
	after       string // YYYY-MM-DD, "" when not passed
	before      string // YYYY-MM-DD, "" when not passed
	has         []string
	thread      bool
	rawDatesSet bool // raw query text carries its own date modifier
}

// hasEmojiRe is Slack's documented reaction form, has::emoji:.
var hasEmojiRe = regexp.MustCompile(`^:[a-z0-9_+\-']+:$`)

// searchHasValues are the has: values this server offers: link (the form the
// reference implementation verified) and pin (documented by Slack), plus a
// reaction as :emoji: (Slack's documented has::emoji:). The bare has:reaction,
// has:file and has:star forms are unverified and not offered.
const searchHasValues = "link, pin, or an :emoji: for a reaction"

// Raw-text modifiers that collide with a structured parameter.
var rawModifier = map[string]*regexp.Regexp{
	"in":     regexp.MustCompile(`(?i)(^|\s)-?in:`),
	"from":   regexp.MustCompile(`(?i)(^|\s)-?from:`),
	"after":  regexp.MustCompile(`(?i)(^|\s)after:`),
	"before": regexp.MustCompile(`(?i)(^|\s)before:`),
	"has":    regexp.MustCompile(`(?i)(^|\s)-?has:`),
	"is":     regexp.MustCompile(`(?i)(^|\s)-?is:thread`),
}
var rawAnyDate = regexp.MustCompile(`(?i)(^|\s)(after|before|on|during):`)

func filterFailure(msg, guidance string) *FeatureResult {
	return &FeatureResult{Success: false, Message: msg, Guidance: guidance}
}

// parseDateParam accepts YYYY-MM-DD or a timeframe shorthand (3d, 2w) that
// converts to an absolute date.
func parseDateParam(name, v string) (string, *FeatureResult) {
	v = strings.TrimSpace(v)
	if _, err := time.Parse("2006-01-02", v); err == nil {
		return v, nil
	}
	if len(v) >= 2 && strings.ContainsRune("dwmy", rune(v[len(v)-1])) {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return timeframeStart(v).Format("2006-01-02"), nil
		}
	}
	return "", filterFailure(fmt.Sprintf("%s=%q is not a date.", name, v),
		fmt.Sprintf("Pass %s as YYYY-MM-DD, or a shorthand like 3d or 2w.", name))
}

// parseSearchFilters validates and resolves the filters. A non-nil result is a
// refusal: no search has run.
func parseSearchFilters(ctx context.Context, p *provider.ApiProvider, query string, params map[string]interface{}) (*searchFilters, *FeatureResult) {
	f := &searchFilters{rawDatesSet: rawAnyDate.MatchString(query)}
	conflict := func(name string) *FeatureResult {
		return filterFailure(fmt.Sprintf("The query text already contains %s:, and %s= would add a second one.", name, name),
			fmt.Sprintf("Use either %s= or the raw %s: in query=, not both.", name, name))
	}

	for _, in := range stringList(params["in"]) {
		if rawModifier["in"].MatchString(query) {
			return nil, conflict("in")
		}
		trimmed := strings.TrimSpace(in)
		if provider.LooksLikeChannelID(trimmed) {
			return nil, filterFailure(fmt.Sprintf("in=%q looks like an internal ID; this server addresses places by name.", in),
				"Pass in='#channel' or in='@person'. See: estate view='channels'")
		}
		id, terr := resolveTarget(ctx, p, trimmed, provider.ReadPolicy, "in")
		if terr != nil {
			return nil, terr.result()
		}
		tok, ok := searchPlaceToken(p, id, trimmed)
		if !ok {
			return nil, filterFailure(fmt.Sprintf("in=%q is a group conversation, which Slack search cannot be narrowed to by name.", in),
				"Narrow to a channel (in='#channel') or one person (in='@person').")
		}
		f.channels = append(f.channels, tok)
		f.inResolved = append(f.inResolved, map[string]interface{}{"input": in, "resolved": tok})
	}
	if len(stringList(params["from"])) > 0 && rawModifier["from"].MatchString(query) {
		return nil, conflict("from")
	}

	var err *FeatureResult
	if s, _ := params["after"].(string); strings.TrimSpace(s) != "" {
		if rawModifier["after"].MatchString(query) {
			return nil, conflict("after")
		}
		if tf, _ := params["timeframe"].(string); strings.TrimSpace(tf) != "" {
			return nil, filterFailure("Both after= and timeframe= were passed.", "Pass after= or timeframe=, not both.")
		}
		if f.after, err = parseDateParam("after", s); err != nil {
			return nil, err
		}
	}
	if s, _ := params["before"].(string); strings.TrimSpace(s) != "" {
		if rawModifier["before"].MatchString(query) {
			return nil, conflict("before")
		}
		if f.before, err = parseDateParam("before", s); err != nil {
			return nil, err
		}
	}
	if f.after != "" && f.before != "" && f.after > f.before {
		return nil, filterFailure(fmt.Sprintf("after=%s is later than before=%s.", f.after, f.before), "Swap them, or widen the range.")
	}

	for _, h := range stringList(params["has"]) {
		h = strings.ToLower(strings.TrimSpace(h))
		if rawModifier["has"].MatchString(query) {
			return nil, conflict("has")
		}
		if h != "link" && h != "pin" && !hasEmojiRe.MatchString(h) {
			return nil, filterFailure(fmt.Sprintf("has=%q is not a supported value.", h), "has accepts: "+searchHasValues+".")
		}
		f.has = append(f.has, h)
	}
	if t, _ := params["thread"].(bool); t {
		if rawModifier["is"].MatchString(query) {
			return nil, conflict("is:thread")
		}
		f.thread = true
	}
	return f, nil
}

// searchPlaceToken names a resolved conversation the way Slack's grammar
// takes it: #channel, or @handle for a DM. Never the ID.
func searchPlaceToken(p *provider.ApiProvider, id, input string) (string, bool) {
	users := p.ProvideUsersMap()
	for _, ch := range p.GetCachedChannels() {
		if ch.ID != id {
			continue
		}
		switch {
		case ch.IsIM:
			if u, ok := users[ch.User]; ok && u.Name != "" {
				return "@" + u.Name, true
			}
		case ch.IsMpIM:
			return "", false
		case ch.Name != "":
			return "#" + ch.Name, true
		}
	}
	// A DM opened just now may not be cached yet; the person ladder names it.
	if r := p.ResolvePersonFor(input, provider.ReadPolicy); r.Resolved && r.Handle != "" {
		return "@" + r.Handle, true
	}
	return "", false
}

// explicitWindow reports whether the caller bounded the dates, by parameter or
// in the raw text: such a window is never widened.
func (f *searchFilters) explicitWindow() bool {
	return f.after != "" || f.before != "" || f.rawDatesSet
}

// digestParts feed searchDigest: every filter that decides what a page holds.
func (f *searchFilters) digestParts() string {
	return fmt.Sprintf("%q|%q|%q|%v|%q", f.channels, f.after, f.before, f.thread, f.has)
}
