package provider

// Estate wiring: the provider owns one estate.Store per process, opened at
// boot and fed by every path that enumerates or patches users and channels.
// ADR-007 is the contract.
//
// Locking rule: no provider mutex is ever held across a call into
// pkg/estate, and no estate call is made with fold results while holding a
// provider mutex. Every Observe* call takes fetched slices or map copies.

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/slack-go/slack"
)

const (
	defaultEstateSweepInterval = 24 * time.Hour
	// The scheduler wakes hourly to compare the fold's sweep watermark
	// against the interval, so a server bounced hourly does not sweep
	// hourly and a long-running one sweeps on time.
	estateSweepCheckInterval = time.Hour
	// First check waits out member-load and backfill so the sweep never
	// competes with boot traffic for rate limit headroom.
	estateSweepBootDelay = time.Minute
	// How long the scheduler waits for the boot backfill before sweeping
	// anyway. A backfill that errors leaves backfillDone false forever, and
	// the sweep must not inherit that silence.
	estateSweepBackfillWait = 20 * time.Minute
)

// openEstate opens the workspace's estate ledger. Failure degrades to a
// nil store — the same posture as a nil cache.Store — and every call site
// nil-checks, so the server keeps serving without history rather than
// refusing to start.
// est returns the estate store. Guarded: the background boot goroutine
// assigns it while MCP handler goroutines read it, and an unsynchronized
// pointer write/read across goroutines is a memory-model violation.
func (ap *ApiProvider) est() *estate.Store {
	ap.estateMu.RLock()
	defer ap.estateMu.RUnlock()
	return ap.estate
}

func (ap *ApiProvider) openEstate() {
	if ap.selfTeamID == "" {
		log.Printf("Estate ledger disabled: no team identity captured")
		return
	}
	st, err := estate.Open(ap.selfTeamID)
	if err != nil {
		log.Printf("Estate ledger unavailable: %v", err)
		return
	}
	ap.estateMu.Lock()
	ap.estate = st
	ap.estateMu.Unlock()
	if st.ReadOnly() {
		log.Printf("Estate ledger: read-only — another instance holds the writer lock")
	}

	s := st.Stats()
	log.Printf("Estate ledger: %d lines, %d bytes, %d users (%d tombstoned), %d channels (%d tombstoned)",
		s.Lines, s.Bytes, s.Users, s.TombstonedUsers, s.Channels, s.TombstonedChannels)
}

func (ap *ApiProvider) observeUsersEstate(users []slack.User, complete bool, src estate.Source) estate.ObserveResult {
	st := ap.est()
	if st == nil || st.ReadOnly() {
		return estate.ObserveResult{}
	}
	res, err := st.ObserveUsers(users, complete, src, time.Now())
	if err != nil {
		log.Printf("Estate: observe users: %v", err)
	}
	if res.AbsenceAborted {
		log.Printf("Estate: user absence pass aborted — %d proposed absent, treating the listing as degraded", res.ProposedAbsent)
	}
	return res
}

func (ap *ApiProvider) observeChannelsEstate(channels []slack.Channel, complete bool, src estate.Source) estate.ObserveResult {
	st := ap.est()
	if st == nil || st.ReadOnly() {
		return estate.ObserveResult{}
	}
	res, err := st.ObserveChannels(channels, complete, src, time.Now())
	if err != nil {
		log.Printf("Estate: observe channels: %v", err)
	}
	if res.AbsenceAborted {
		log.Printf("Estate: channel absence pass aborted — %d proposed absent, treating the listing as degraded", res.ProposedAbsent)
	}
	return res
}

// reconcileEstateTombstones mirrors the fold's tombstones into the live
// maps, so an entity a sweep just tombstoned stops resolving immediately
// instead of at the next boot's fold-merged load.
func (ap *ApiProvider) reconcileEstateTombstones() {
	st := ap.est()
	if st == nil {
		return
	}

	users := st.Users()
	ap.usersMutex.Lock()
	for id, rec := range users {
		if rec.Gone == nil {
			continue
		}
		if u, ok := ap.users[id]; ok && !u.Deleted {
			u.Deleted = true
			ap.users[id] = u
		}
	}
	ap.usersMutex.Unlock()

	channels := st.Channels()
	ap.channelsMutex.Lock()
	for id, rec := range channels {
		if rec.Gone != nil {
			delete(ap.channels, id)
		}
	}
	ap.channelsMutex.Unlock()
}

// closeChannelEnumerationEstate runs the absence pass over a completed
// enumeration's seen set.
func (ap *ApiProvider) closeChannelEnumerationEstate(seen map[string]bool, src estate.Source, startedAt time.Time) estate.ObserveResult {
	st := ap.est()
	if st == nil || st.ReadOnly() {
		return estate.ObserveResult{}
	}
	res, err := st.CloseChannelEnumeration(seen, src, startedAt, time.Now())
	if err != nil {
		log.Printf("Estate: close channel enumeration: %v", err)
	}
	if res.AbsenceAborted {
		log.Printf("Estate: channel absence pass aborted — %d proposed absent, treating the listing as degraded", res.ProposedAbsent)
	}
	return res
}

func (ap *ApiProvider) recordEstateSweep(rep estate.SweepReport) {
	st := ap.est()
	if st == nil || st.ReadOnly() {
		return
	}
	if err := st.RecordSweep(rep, time.Now()); err != nil {
		log.Printf("Estate: record sweep: %v", err)
	}
}

// startEstateSweepScheduler self-schedules the sweep. There is no env var
// and no CLI flag: the estate maintains itself or it is not agent-first.
// The stop channel exists for a future lifecycle pass; like the cache
// flusher's, nothing calls it today.
func (ap *ApiProvider) startEstateSweepScheduler(ctx context.Context) {
	st := ap.est()
	if st == nil || st.ReadOnly() {
		return
	}
	interval := ap.estateSweepInterval
	if interval <= 0 {
		interval = defaultEstateSweepInterval
	}
	stop := make(chan struct{})
	ap.estateSweepStop = stop

	go Guard("estate-sweep-scheduler", func() {
		select {
		case <-time.After(estateSweepBootDelay):
		case <-stop:
			return
		}
		// The boot backfill is already walking conversations.list, and two
		// concurrent walkers starve each other under its rate limit (~20
		// requests/min) — observed live as paired 30s backoffs on a
		// 3,000-conversation workspace. Wait for it, bounded so a backfill
		// that dies never silences the sweep forever.
		ap.waitForBackfill(stop, estateSweepBackfillWait)
		for {
			if time.Since(st.LastFullSweep()) >= interval {
				if err := ap.RunEstateSweep(ctx); err != nil {
					log.Printf("Estate sweep: %v", err)
				}
			}
			select {
			case <-time.After(estateSweepCheckInterval):
			case <-stop:
				return
			}
		}
	})
}

// waitForBackfill blocks until the boot channel backfill has completed, the
// bound elapses, or stop closes.
func (ap *ApiProvider) waitForBackfill(stop chan struct{}, bound time.Duration) {
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		ap.backfillMutex.Lock()
		done := ap.backfillDone
		ap.backfillMutex.Unlock()
		if done {
			return
		}
		select {
		case <-time.After(5 * time.Second):
		case <-stop:
			return
		}
	}
}

// backfillIfStale runs the boot channel walk only when the estate cannot
// prove a complete enumeration within the sweep interval. Skipping still
// marks the backfill done so the sweep scheduler proceeds immediately, and
// an explicit RefreshChannelCache bypasses this by respawning
// backgroundBackfill directly. Without a ledger the walk runs every boot,
// as it always has.
func (ap *ApiProvider) backfillIfStale(ctx context.Context) {
	if fresh, age := ap.channelEnumerationFresh(); fresh {
		ap.backfillMutex.Lock()
		ap.backfillDone = true
		ap.backfillMutex.Unlock()
		log.Printf("Channel walk skipped: estate enumerated the workspace %s ago", age.Round(time.Minute))
		return
	}
	ap.backgroundBackfill(ctx)
}

// channelEnumerationFresh reports whether the estate holds a complete
// channel enumeration younger than the sweep interval, and its age.
func (ap *ApiProvider) channelEnumerationFresh() (bool, time.Duration) {
	st := ap.est()
	if st == nil {
		return false, 0
	}
	interval := ap.estateSweepInterval
	if interval <= 0 {
		interval = defaultEstateSweepInterval
	}
	last := st.LastChannelSweep()
	if last.IsZero() {
		return false, 0
	}
	age := time.Since(last)
	return age < interval, age
}

// RunEstateSweep performs one synchronous full estate sweep: users.list,
// the membership walk, and the full channel walk, each merged into the
// provider maps and observed into the ledger. A class whose enumeration
// fails is reported incomplete and asserts no absences. Exported so tests
// and a future explicit-refresh path can sweep without the scheduler.
func (ap *ApiProvider) RunEstateSweep(ctx context.Context) error {
	st := ap.est()
	if st == nil {
		return fmt.Errorf("estate ledger unavailable")
	}
	if st.ReadOnly() {
		return estate.ErrReadOnly
	}
	if ap.client == nil {
		return fmt.Errorf("estate sweep before boot")
	}

	start := time.Now()
	var rep estate.SweepReport

	// Users: slack-go paginates users.list internally, so one call is the
	// full directory — deactivated users included, with deleted=true.
	users, err := ap.client.GetUsersContext(ctx, slack.GetUsersOptionLimit(1000))
	if err != nil {
		log.Printf("Estate sweep: users.list failed: %v", err)
	} else {
		ap.usersMutex.Lock()
		for _, u := range users {
			ap.users[u.ID] = u
		}
		ap.usersMutex.Unlock()

		res := ap.observeUsersEstate(users, true, estate.SourceSweep)
		rep.Users = estate.ClassReport{Complete: true, Count: len(users), AbsenceAborted: res.AbsenceAborted}
		rep.Appended += res.Appended
	}

	// Channels: skip the walk when another observer — usually the boot
	// backfill, which records its completion as a channels-class sweep
	// event — enumerated them within the sweep interval. The full walk is
	// the sweep's dominant API cost, and re-walking a fresh enumeration
	// buys nothing.
	interval := ap.estateSweepInterval
	if interval <= 0 {
		interval = defaultEstateSweepInterval
	}
	if last := st.LastChannelSweep(); !last.IsZero() && time.Since(last) < interval {
		rep.Membership = estate.ClassReport{Skipped: true}
		rep.Channels = estate.ClassReport{Skipped: true, ArchivedIncluded: true}
		ap.markDirty()
		rep.Duration = time.Since(start)
		ap.recordEstateSweep(rep)
		if !rep.Users.Complete {
			return fmt.Errorf("estate sweep incomplete: users enumeration failed")
		}
		log.Printf("Estate sweep complete: %d users, channels skipped (enumerated %s ago), %d events appended in %s",
			rep.Users.Count, time.Since(last).Round(time.Second), rep.Appended, rep.Duration.Round(time.Millisecond))
		return nil
	}

	// Membership: users.conversations is the authority on isMember;
	// conversations.list's is_member rides along but the walk is what the
	// ADR names.
	memberIDs, membersComplete := ap.fetchMemberChannelIDs(ctx)
	rep.Membership = estate.ClassReport{Complete: membersComplete, Count: len(memberIDs)}

	// Channels: the full walk, archived included, observed page-wise so an
	// interrupted sweep's knowledge is durable and a resumed one appends
	// nothing for what it re-sees. When the membership walk failed,
	// mergeChannels keeps the map's IsMember rather than clobbering
	// member-loaded truth with a possibly-stale flag.
	var sweepMembers map[string]bool
	if membersComplete {
		sweepMembers = memberIDs
	}
	walk := ap.fetchAllChannels(ctx, func(page []slack.Channel) {
		merged := ap.mergeChannels(page, sweepMembers)
		res := ap.observeChannelsEstate(merged, false, estate.SourceSweep)
		rep.Appended += res.Appended
	})

	rep.Channels = estate.ClassReport{Complete: walk.complete, Count: len(walk.seen), ArchivedIncluded: true}
	if walk.complete {
		res := ap.closeChannelEnumerationEstate(walk.seen, estate.SourceSweep, walk.startedAt)
		rep.Channels.AbsenceAborted = res.AbsenceAborted
		rep.Appended += res.Appended
	}

	ap.reconcileEstateTombstones()
	ap.markDirty()
	rep.Duration = time.Since(start)
	ap.recordEstateSweep(rep)

	if !rep.Users.Complete || !rep.Channels.Complete {
		return fmt.Errorf("estate sweep incomplete: users=%v channels=%v", rep.Users.Complete, rep.Channels.Complete)
	}
	log.Printf("Estate sweep complete: %d users, %d channels, %d events appended in %s",
		rep.Users.Count, rep.Channels.Count, rep.Appended, rep.Duration.Round(time.Millisecond))
	return nil
}

// WalkProgress is the live state of a channel enumeration in flight.
type WalkProgress struct {
	Active  bool
	Seen    int
	Started time.Time
}

// EstateCoverageInfo is everything coverage reporting needs to state what
// the estate knows and what it is doing about the rest: per-class
// enumeration watermarks, live fold counts, and walk progress. Repeated
// queries during a walk show Seen advancing, which is the honest substitute
// for a bare "not swept yet".
type EstateCoverageInfo struct {
	Available     bool
	LastFullSweep time.Time
	UserSweep     time.Time
	ChannelSweep  time.Time
	Users         int
	Channels      int
	ChannelWalk   WalkProgress
}

// EstateCoverage assembles the coverage picture. Zero-valued (Available
// false) when the ledger is unavailable.
func (ap *ApiProvider) EstateCoverage() EstateCoverageInfo {
	st := ap.est()
	if st == nil {
		return EstateCoverageInfo{}
	}
	stats := st.Stats()
	ap.walkMu.Lock()
	walk := ap.channelWalk
	ap.walkMu.Unlock()
	return EstateCoverageInfo{
		Available:     true,
		LastFullSweep: st.LastFullSweep(),
		UserSweep:     st.LastUserSweep(),
		ChannelSweep:  st.LastChannelSweep(),
		Users:         stats.Users,
		Channels:      stats.Channels,
		ChannelWalk:   walk,
	}
}

// EstateUsers returns the fold's user records — tombstoned included, which
// is the point: dated absence a listing can serve instead of a silent gap.
// Nil when the ledger is unavailable.
func (ap *ApiProvider) EstateUsers() map[string]estate.UserRecord {
	st := ap.est()
	if st == nil {
		return nil
	}
	return st.Users()
}

// EstateChannels returns the fold's channel records, tombstoned included.
// Nil when the ledger is unavailable.
func (ap *ApiProvider) EstateChannels() map[string]estate.ChannelRecord {
	st := ap.est()
	if st == nil {
		return nil
	}
	return st.Channels()
}

// EstateUser returns one fold record by ID without copying the whole fold.
func (ap *ApiProvider) EstateUser(id string) (estate.UserRecord, bool) {
	st := ap.est()
	if st == nil {
		return estate.UserRecord{}, false
	}
	return st.User(id)
}

// EstateChannel returns one fold record by ID.
func (ap *ApiProvider) EstateChannel(id string) (estate.ChannelRecord, bool) {
	st := ap.est()
	if st == nil {
		return estate.ChannelRecord{}, false
	}
	return st.Channel(id)
}

// EstateLastFullSweep reports when the estate last had a complete picture.
// Zero means absence cannot be asserted — either no full sweep has
// completed or the ledger is unavailable — and coverage reporting must say
// which state it is in rather than implying an empty estate.
func (ap *ApiProvider) EstateLastFullSweep() time.Time {
	st := ap.est()
	if st == nil {
		return time.Time{}
	}
	return st.LastFullSweep()
}

// fetchMemberChannelIDs walks users.conversations and returns the set of
// conversation IDs the authed user belongs to, with the same pacing
// loadMemberChannels uses.
func (ap *ApiProvider) fetchMemberChannelIDs(ctx context.Context) (map[string]bool, bool) {
	ids := make(map[string]bool)
	cursor := ""
	for {
		channels, nextCursor, err := ap.client.GetConversationsForUser(&slack.GetConversationsForUserParameters{
			Cursor:          cursor,
			Limit:           100,
			Types:           []string{"public_channel", "private_channel", "mpim", "im"},
			ExcludeArchived: true,
		})
		if err != nil {
			if rateLimitErr, ok := err.(*slack.RateLimitedError); ok {
				log.Printf("Membership walk rate limited, waiting %v", rateLimitErr.RetryAfter)
				time.Sleep(rateLimitErr.RetryAfter)
				continue
			}
			log.Printf("Membership walk failed: %v", err)
			return ids, false
		}
		for _, ch := range channels {
			ids[ch.ID] = true
		}
		if nextCursor == "" {
			return ids, true
		}
		cursor = nextCursor
		time.Sleep(500 * time.Millisecond)
	}
}
