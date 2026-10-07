package provider

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
)

// Channel acquisition on a name miss (ADR-015): one quick-switcher search
// for a channel name the cache does not hold. Only a unique exact name
// resolves, and it resolves through conversations.info, so the switcher's
// thin hits never reach the cache or the estate. No sweep calls this.

const (
	// switcherCount is how many hits one lookup asks for: Slack's largest
	// honored page (ADR-003), so the exact-name scan sees as much as one
	// call can return.
	switcherCount = 100
	// switcherMissTTL is how long a name that found nothing is not asked
	// again, so an agent retrying a typo does not repeat the call.
	switcherMissTTL = 10 * time.Minute
	// switcherDefaultPause applies when a rate-limit answer names no wait.
	switcherDefaultPause = 30 * time.Second
	// switcherMemoCap bounds the miss memo: past it, expired entries are
	// pruned, then the oldest goes.
	switcherMemoCap = 256
)

// ChannelLookup is what Slack's channel search said about a name.
type ChannelLookup struct {
	// Channel is the unique exact-name hit's full record, now cached and
	// observed into the estate; nil when no hit is named exactly so.
	Channel *slack.Channel
	// Hits are the matches in Slack's order when none resolved, for the
	// caller to offer as candidates; Total counts them before the page cap,
	// so Total > len(Hits) means the exact-name scan saw one page only.
	Hits  []SwitcherChannel
	Total int
	// Unavailable says why the search could not answer; empty when it did.
	Unavailable string
}

// switcherState serializes lookups and holds the miss memo and the
// rate-limit pause. The one-slot semaphore is held across the call, so at
// most one switcher request is in flight, and a waiter whose request ends
// gives up its place.
type switcherState struct {
	once        sync.Once
	sem         chan struct{}
	misses      map[string]time.Time
	pausedUntil time.Time
}

func (sw *switcherState) acquire(ctx context.Context) error {
	sw.once.Do(func() { sw.sem = make(chan struct{}, 1) })
	select {
	case sw.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sw *switcherState) release() { <-sw.sem }

// LookupChannelRemote asks Slack's quick switcher for a channel name the
// cache does not hold. A leading '#' is ignored.
func (ap *ApiProvider) LookupChannelRemote(ctx context.Context, name string) ChannelLookup {
	name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "#"))
	key := strings.ToLower(name)
	if key == "" {
		return ChannelLookup{}
	}
	sw := &ap.switcher
	if err := sw.acquire(ctx); err != nil {
		return ChannelLookup{Unavailable: "Slack's channel search was not asked: the request ended first."}
	}
	defer sw.release()

	// A lookup that waited may find the name cached by the one before it.
	if ch, ok := ap.LookupChannelName(key); ok && ch.ID != "" {
		return ChannelLookup{Channel: &ch}
	}

	now := time.Now()
	if now.Before(sw.pausedUntil) {
		return ChannelLookup{Unavailable: fmt.Sprintf("Slack's channel search is rate-limited for another %s.", sw.pausedUntil.Sub(now).Round(time.Second))}
	}
	if at, ok := sw.misses[key]; ok && now.Sub(at) < switcherMissTTL {
		return ChannelLookup{}
	}
	if ap.internalClient == nil {
		return ChannelLookup{Unavailable: "Slack's channel search could not be reached."}
	}

	res, err := ap.internalClient.SearchChannels(ctx, name, switcherCount)
	if err != nil {
		var rl *slack.RateLimitedError
		if errors.As(err, &rl) {
			wait := rl.RetryAfter
			if wait <= 0 {
				wait = switcherDefaultPause
			}
			sw.pausedUntil = time.Now().Add(wait)
			log.Printf("channel lookup: switcher rate-limited for %s", wait)
			return ChannelLookup{Unavailable: fmt.Sprintf("Slack's channel search is rate-limited for another %s.", wait.Round(time.Second))}
		}
		log.Printf("channel lookup: switcher failed: %v", err)
		return ChannelLookup{Unavailable: "Slack's channel search could not be reached."}
	}

	if len(res.Items) == 0 {
		sw.remember(key, now)
		return ChannelLookup{}
	}

	var exact []SwitcherChannel
	for _, it := range res.Items {
		if strings.EqualFold(it.Name, name) {
			exact = append(exact, it)
		}
	}
	if len(exact) == 1 {
		ch, err := ap.fetchAndCacheChannel(ctx, exact[0].ID)
		if err != nil {
			log.Printf("channel lookup: conversations.info for a switcher hit failed: %v", err)
			return ChannelLookup{Unavailable: fmt.Sprintf("Slack's channel search found #%s, but reading it failed.", exact[0].Name)}
		}
		return ChannelLookup{Channel: ch}
	}

	total := res.Pagination.TotalCount
	if total < len(res.Items) {
		total = len(res.Items)
	}
	return ChannelLookup{Hits: res.Items, Total: total}
}

// remember records a name that found nothing, pruning expired entries once
// the memo passes its cap.
func (sw *switcherState) remember(key string, at time.Time) {
	if sw.misses == nil {
		sw.misses = make(map[string]time.Time)
	}
	if len(sw.misses) >= switcherMemoCap {
		oldest, oldestAt := "", at
		for k, t := range sw.misses {
			if at.Sub(t) >= switcherMissTTL {
				delete(sw.misses, k)
			} else if t.Before(oldestAt) {
				oldest, oldestAt = k, t
			}
		}
		if len(sw.misses) >= switcherMemoCap {
			delete(sw.misses, oldest)
		}
	}
	sw.misses[key] = at
}
