package features

// Estate facts in payloads (ADR-007): a tombstone surfaces as a dated
// fact, absence is asserted only under a completed sweep, and otherwise
// the response says it cannot know. These helpers build the coverage
// block and the dated-fact entries list-users and estate view='channels' carry.

import (
	"fmt"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// estateCoverage builds the coverage block every listing carries: the
// swept/lastFullSweep claim, per-class enumeration times and counts, and
// live walk progress so repeated queries show the mapping advancing.
func estateCoverage(ap *provider.ApiProvider) map[string]interface{} {
	info := ap.EstateCoverage()
	est := map[string]interface{}{
		"lastFullSweep": nil,
		"swept":         false,
	}
	if !info.Available {
		est["available"] = false
		return map[string]interface{}{"estate": est}
	}
	if !info.LastFullSweep.IsZero() {
		est["lastFullSweep"] = info.LastFullSweep.Format(time.RFC3339)
		est["swept"] = true
	}
	users := map[string]interface{}{"count": info.Users}
	if !info.UserSweep.IsZero() {
		users["lastComplete"] = info.UserSweep.Format(time.RFC3339)
	}
	channels := map[string]interface{}{"count": info.Channels}
	if !info.ChannelSweep.IsZero() {
		channels["lastComplete"] = info.ChannelSweep.Format(time.RFC3339)
	}
	if info.ChannelWalk.Active {
		channels["enumerating"] = map[string]interface{}{
			"seen":           info.ChannelWalk.Seen,
			"startedSecsAgo": int(time.Since(info.ChannelWalk.Started).Seconds()),
		}
	}
	est["users"] = users
	est["channels"] = channels
	return map[string]interface{}{"estate": est}
}

// goneInterval renders a tombstone's honesty bound: the change lies in
// (NotBefore, At], and a zero NotBefore collapses to the observation date
// rather than inventing a lower bound.
func goneInterval(g *estate.Tombstone) []string {
	from := g.NotBefore
	if from.IsZero() {
		from = g.At
	}
	return []string{from.Format(time.RFC3339), g.At.Format(time.RFC3339)}
}

// goneIntervalKey names the interval field by the tombstone's reason, the
// vocabulary ADR-007's examples fix.
func goneIntervalKey(g *estate.Tombstone) string {
	if g.Reason == estate.ReasonDeactivated {
		return "deactivatedBetween"
	}
	return "absentBetween"
}

// tombstonedUserEntry renders one tombstoned user as a dated fact.
func tombstonedUserEntry(rec estate.UserRecord) map[string]interface{} {
	displayName := rec.Props.RealName
	if displayName == "" {
		displayName = rec.Props.Name
	}
	entry := map[string]interface{}{
		"displayName":             displayName,
		"username":                rec.Props.Name,
		"id":                      rec.ID,
		"deleted":                 true,
		"reason":                  rec.Gone.Reason,
		goneIntervalKey(rec.Gone): goneInterval(rec.Gone),
	}
	if rec.Props.DisplayName != "" && rec.Props.DisplayName != rec.Props.RealName {
		entry["profileDisplayName"] = rec.Props.DisplayName
	}
	return entry
}

// tombstonedUserMatches searches the fold's tombstoned users the same way
// the live listing searches the map, so a departed person answers a query
// as a dated fact instead of vanishing from it.
func tombstonedUserMatches(ap *provider.ApiProvider, queryLower string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, rec := range ap.EstateUsers() {
		if rec.Gone == nil {
			continue
		}
		if !strings.Contains(strings.ToLower(rec.Props.RealName), queryLower) &&
			!strings.Contains(strings.ToLower(rec.Props.Name), queryLower) &&
			!strings.Contains(strings.ToLower(rec.Props.DisplayName), queryLower) {
			continue
		}
		out = append(out, tombstonedUserEntry(rec))
	}
	return out
}

// unresolvedPeopleResult answers a search whose from: filter named someone
// the ladder could not resolve. It runs no search — a guessed handle
// returns an empty result indistinguishable from silence — and instead
// hands back each miss with its candidates, so the retry needs no extra
// lookup.
func unresolvedPeopleResult(ap *provider.ApiProvider, misses []provider.PersonResolution) *FeatureResult {
	var lines []string
	data := make([]map[string]interface{}, 0, len(misses))
	for _, r := range misses {
		entry := map[string]interface{}{"input": r.Input, "reason": r.Reason}
		switch r.Reason {
		case "ambiguous", "tombstoned":
			entry["candidates"] = r.Candidates
			var opts []string
			for _, c := range r.Candidates {
				opt := fmt.Sprintf("@%s (%s", c.Handle, c.DisplayName)
				if c.Title != "" {
					opt += " — " + c.Title
				}
				if c.Deleted && len(c.GoneBetween) == 2 {
					opt += fmt.Sprintf("; %s between %s and %s", c.GoneReason, c.GoneBetween[0], c.GoneBetween[1])
				}
				opts = append(opts, opt+")")
			}
			label := "matches several people"
			if r.Reason == "tombstoned" {
				label = "matches only departed people"
			}
			lines = append(lines, fmt.Sprintf("'%s' %s: %s", r.Input, label, strings.Join(opts, ", ")))
		case "unswept":
			lines = append(lines, fmt.Sprintf("'%s' matches nobody cached, and no full sweep has completed — they may exist unswept", r.Input))
		default:
			lines = append(lines, fmt.Sprintf("'%s' matches nobody in the workspace", r.Input))
		}
		data = append(data, entry)
	}

	return &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Search not run: %d name(s) in from: did not resolve", len(misses)),
		Data: map[string]interface{}{
			"unresolved": data,
			"results":    []map[string]interface{}{},
			"coverage":   estateCoverage(ap),
		},
		Guidance:    strings.Join(lines, "\n"),
		NextActions: []string{"Retry with a listed handle: messages query='...' from=['<handle>']"},
	}
}

// tombstonedChannelMatches renders the fold's gone channels matching a
// search, for the channels view's includeDeleted flag.
func tombstonedChannelMatches(ap *provider.ApiProvider, searchLower string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, rec := range ap.EstateChannels() {
		if rec.Gone == nil {
			continue
		}
		if searchLower != "" &&
			!strings.Contains(strings.ToLower(rec.Props.Name), searchLower) &&
			!strings.Contains(strings.ToLower(rec.Props.Purpose), searchLower) {
			continue
		}
		entry := map[string]interface{}{
			"name":                    rec.Props.Name,
			"displayName":             "#" + rec.Props.Name + " (gone)",
			"deleted":                 true,
			"reason":                  rec.Gone.Reason,
			goneIntervalKey(rec.Gone): goneInterval(rec.Gone),
		}
		if rec.Props.Purpose != "" {
			entry["purpose"] = rec.Props.Purpose
		}
		out = append(out, entry)
	}
	return out
}
