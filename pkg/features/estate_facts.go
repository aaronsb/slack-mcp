package features

// Estate facts in payloads (ADR-007): a tombstone surfaces as a dated
// fact, absence is asserted only under a completed sweep, and otherwise
// the response says it cannot know. These helpers build the coverage
// block and the dated-fact entries list-users and list-channels carry.

import (
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// estateCoverage builds the coverage block every listing carries. A null
// lastFullSweep means absence cannot be asserted, and callers phrase their
// empty results accordingly.
func estateCoverage(ap *provider.ApiProvider) map[string]interface{} {
	last := ap.EstateLastFullSweep()
	if last.IsZero() {
		return map[string]interface{}{
			"estate": map[string]interface{}{"lastFullSweep": nil, "swept": false},
		}
	}
	return map[string]interface{}{
		"estate": map[string]interface{}{"lastFullSweep": last.Format(time.RFC3339), "swept": true},
	}
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

// tombstonedChannelMatches renders the fold's gone channels matching a
// search, for list-channels' includeDeleted flag.
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
