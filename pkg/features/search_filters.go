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
	afterRaw    string // as passed (3d or a date); the digest hashes this
	beforeRaw   string
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

// Raw-text modifiers that collide with a structured parameter. Each needs a
// value-shaped token right after the colon (Slack modifiers take no space), so
// prose like "check in: error" or "blocked on: deploy" is not a modifier.
// Quoted phrases are ignored.
const dateValue = `("?)(\d|today|yesterday|jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec|spring|summer|fall|autumn|winter)`

var rawModifier = map[string]*regexp.Regexp{
	"in":     regexp.MustCompile(`(?i)(^|\s)-?in:\S`),
	"from":   regexp.MustCompile(`(?i)(^|\s)-?from:\S`),
	"after":  regexp.MustCompile(`(?i)(^|\s)after:` + dateValue),
	"before": regexp.MustCompile(`(?i)(^|\s)before:` + dateValue),
	"has":    regexp.MustCompile(`(?i)(^|\s)-?has:(link|pin|star|files?|reaction|image|:)`),
	"is":     regexp.MustCompile(`(?i)(^|\s)-?is:thread`),
}
var rawOnDuring = regexp.MustCompile(`(?i)(^|\s)(on|during):` + dateValue)
var rawAnyDate = regexp.MustCompile(`(?i)(^|\s)(after|before|on|during):` + dateValue)
var quotedPhrase = regexp.MustCompile(`"[^"]*"`)

// rawText is the query without quoted phrases, for modifier detection.
func rawText(q string) string { return quotedPhrase.ReplaceAllString(q, " ") }

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
	query = rawText(query)
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
	afterRaw, _ := params["after"].(string)
	beforeRaw, _ := params["before"].(string)
	afterRaw, beforeRaw = strings.TrimSpace(afterRaw), strings.TrimSpace(beforeRaw)
	timeframe, _ := params["timeframe"].(string)
	if strings.TrimSpace(timeframe) != "" {
		switch {
		case afterRaw != "":
			return nil, filterFailure("Both after= and timeframe= were passed.", "Pass after= or timeframe=, not both.")
		case beforeRaw != "":
			return nil, filterFailure("Both before= and timeframe= were passed.", "Pass timeframe= for the window start, or after=/before= for an explicit range, not both.")
		case f.rawDatesSet:
			return nil, filterFailure("timeframe= was passed with a date operator in the query text.", "Drop timeframe=: the date operator in query= already sets the window.")
		}
	}
	if afterRaw != "" || beforeRaw != "" {
		if rawOnDuring.MatchString(query) {
			return nil, filterFailure("The query text already contains on: or during:, and after=/before= would add a second date bound.",
				"Use the raw on:/during: in query=, or after=/before=, not both.")
		}
	}
	if afterRaw != "" {
		if rawModifier["after"].MatchString(query) {
			return nil, conflict("after")
		}
		if f.after, err = parseDateParam("after", afterRaw); err != nil {
			return nil, err
		}
		f.afterRaw = afterRaw
	}
	if beforeRaw != "" {
		if rawModifier["before"].MatchString(query) {
			return nil, conflict("before")
		}
		if f.before, err = parseDateParam("before", beforeRaw); err != nil {
			return nil, err
		}
		f.beforeRaw = beforeRaw
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
	// The raw strings, not the resolved dates: a relative "3d" resolves to a
	// different date after midnight, and a cursor must survive that.
	return fmt.Sprintf("%q|%q|%q|%v|%q", f.channels, f.afterRaw, f.beforeRaw, f.thread, f.has)
}

// hasFilter reports whether any structured search filter is present.
func hasFilter(params map[string]interface{}) bool {
	for _, k := range []string{"in", "from", "has"} {
		if len(stringList(params[k])) > 0 {
			return true
		}
	}
	for _, k := range []string{"after", "before"} {
		if s, _ := params[k].(string); strings.TrimSpace(s) != "" {
			return true
		}
	}
	t, _ := params["thread"].(bool)
	return t
}
