package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/slack-go/slack"
)

// The compiled executor (ADR-008): when a view's question exceeds the
// fold's coverage, the server deterministically constructs a bounded set of
// Slack search queries — `from:@handle after:date` — and records what they
// return as encounters in the attention ledger. Only author, conversation,
// and timestamp are taken from each match; the joins then run in the fold
// exactly as if the activity had been observed on a render path. Agents
// hand-composing Slack query grammar fail silently, so the server owns the
// grammar end to end (the resolution ladder's rule, scaled to plans).

const compiledPageSize = 100

// compiledPace spaces search calls under Slack's search rate tier.
var compiledPace = 1200 * time.Millisecond

// CompiledStats reports what a compiled pass cost and where it was cut
// short; views render this so a ranking says which executor is speaking.
type CompiledStats struct {
	Calls     int
	Elapsed   time.Duration
	Matches   int
	Truncated map[string]int // handle -> matches beyond the page cap
	Failed    map[string]string
}

// CompileActivity runs one bounded search per person (paged), observing
// every match into the attention ledger. people maps user ID -> handle; the
// queries are built from the handle, the encounters recorded under the ID.
func (ap *ApiProvider) CompileActivity(ctx context.Context, people map[string]string, days, maxPages int) CompiledStats {
	stats := CompiledStats{Truncated: map[string]int{}, Failed: map[string]string{}}
	api, err := ap.Provide()
	if err != nil {
		stats.Failed["*"] = err.Error()
		return stats
	}
	start := time.Now()
	after := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	first := true
	for id, handle := range people {
		if handle == "" || id == "" {
			continue
		}
		query := fmt.Sprintf("from:@%s after:%s", handle, after)
		page := 1
		for {
			if !first {
				select {
				case <-time.After(compiledPace):
				case <-ctx.Done():
					stats.Failed[handle] = ctx.Err().Error()
					stats.Elapsed = time.Since(start)
					return stats
				}
			}
			first = false

			params := slack.NewSearchParameters()
			params.Count = compiledPageSize
			params.Page = page
			params.Sort = "timestamp"
			msgs, err := api.SearchMessagesContext(ctx, query, params)
			stats.Calls++
			if err != nil {
				stats.Failed[handle] = err.Error()
				break
			}

			byConv := map[string][]EncounterSample{}
			for _, m := range msgs.Matches {
				conv := m.Channel.ID
				if conv == "" {
					continue
				}
				byConv[conv] = append(byConv[conv], EncounterSample{User: id, TS: m.Timestamp})
			}
			for conv, samples := range byConv {
				ap.ObserveEncounters(conv, samples)
			}
			stats.Matches += len(msgs.Matches)

			if page >= maxPages || page*compiledPageSize >= msgs.Total {
				if msgs.Total > page*compiledPageSize {
					stats.Truncated[handle] = msgs.Total - page*compiledPageSize
				}
				break
			}
			page++
		}
	}
	stats.Elapsed = time.Since(start)
	return stats
}

// UserEncounters returns one user's encounters by conversation from the
// attention fold; nil when no ledger is open.
func (ap *ApiProvider) UserEncounters(id string) map[string][]estate.Encounter {
	att := ap.attn()
	if att == nil {
		return nil
	}
	return att.Encounters(id)
}

// AttentionByConversation returns the activity plane inverted:
// conversation -> day -> users. Nil when no ledger is open.
func (ap *ApiProvider) AttentionByConversation() map[string]map[string][]string {
	att := ap.attn()
	if att == nil {
		return nil
	}
	return att.ByConversation()
}

// SelfUserID exposes the session's own user ID for the views' asymmetric
// treatment of the operator.
func (ap *ApiProvider) SelfUserID() string {
	return ap.selfUserID
}
