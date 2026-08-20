package estate

import (
	"time"

	"github.com/slack-go/slack"
)

// ObserveResult reports what one observation did to the ledger.
type ObserveResult struct {
	// Appended is how many events the observation wrote. An unchanged
	// directory appends zero — ADR-006's append-on-change rule.
	Appended int
	// AbsenceAborted is set when the mass-tombstone guard refused the
	// absence pass. Positively-observed changes were still appended.
	AbsenceAborted bool
	// ProposedAbsent is how many entities the absence pass would have
	// tombstoned, whether or not it ran.
	ProposedAbsent int
}

// ClassReport is one entity class's slice of a sweep: whether its
// enumeration ran to completion, and what the absence pass did. Skipped
// marks a class the sweep deliberately did not enumerate because another
// observer completed it recently — distinct from a failed enumeration, and
// it leaves the class's watermark where that observer set it.
type ClassReport struct {
	Complete         bool
	Count            int
	ArchivedIncluded bool
	AbsenceAborted   bool
	Skipped          bool
}

// SweepReport closes a sweep pass. Recording it appends the sweep event the
// coverage fields read; a class reported complete advances that class's
// watermark, and absence becomes assertable once both classes have one.
type SweepReport struct {
	Users      ClassReport
	Channels   ClassReport
	Membership ClassReport
	Appended   int
	Duration   time.Duration
}

// ObserveUsers diffs an observed set of users against the fold and appends
// the changes. complete asserts that the set is a full directory
// enumeration: only then may entities missing from it be tombstoned as
// absent, because partial sight cannot prove absence. A user positively
// observed with deleted=true is tombstoned as deactivated from any source.
func (s *Store) ObserveUsers(users []slack.User, complete bool, src Source, now time.Time) (ObserveResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var events []event
	seen := make(map[string]bool, len(users))

	for i := range users {
		u := &users[i]
		if u.ID == "" {
			continue
		}
		seen[u.ID] = true
		props := ProjectUser(*u)
		rec := s.fold.users[u.ID]

		switch {
		case rec == nil:
			events = append(events, event{
				V: schemaVersion, At: now, Src: src, Kind: kindFirstSeen,
				Entity: entityUser, ID: u.ID,
				Rec: mustRaw(props), SlackUpdated: int64(u.Updated),
			})
			if props.Deleted {
				events = append(events, event{
					V: schemaVersion, At: now, Src: src, Kind: kindTombstone,
					Entity: entityUser, ID: u.ID,
					Reason: ReasonDeactivated, NotBefore: now,
					Last: mustRaw(props),
				})
			}

		case rec.Gone != nil && !props.Deleted:
			// Re-entry: reactivated, or visibility restored. A fresh
			// first-seen opens a new existence interval; the fold keeps the
			// old one.
			events = append(events, event{
				V: schemaVersion, At: now, Src: src, Kind: kindFirstSeen,
				Entity: entityUser, ID: u.ID,
				Rec: mustRaw(props), SlackUpdated: int64(u.Updated),
			})

		case rec.Gone == nil && props.Deleted:
			events = append(events, event{
				V: schemaVersion, At: now, Src: src, Kind: kindTombstone,
				Entity: entityUser, ID: u.ID,
				Reason: ReasonDeactivated, NotBefore: confirmedBound(rec.LastConfirmed, rec.LastChanged),
				Last: mustRaw(props),
			})

		default:
			if changed := diffUser(rec.Props, props); len(changed) > 0 {
				events = append(events, event{
					V: schemaVersion, At: now, Src: src, Kind: kindChanged,
					Entity: entityUser, ID: u.ID,
					Changed: changed, Rec: mustRaw(props),
					NotBefore:    confirmedBound(rec.LastConfirmed, rec.LastChanged),
					SlackUpdated: int64(u.Updated),
				})
			}
		}
	}

	res := ObserveResult{}
	if complete {
		var proposed []*UserRecord
		live := 0
		for id, rec := range s.fold.users {
			if rec.Gone != nil {
				continue
			}
			live++
			if !seen[id] {
				proposed = append(proposed, rec)
			}
		}
		res.ProposedAbsent = len(proposed)
		if absenceAborts(len(proposed), live) {
			res.AbsenceAborted = true
		} else {
			for _, rec := range proposed {
				events = append(events, event{
					V: schemaVersion, At: now, Src: src, Kind: kindTombstone,
					Entity: entityUser, ID: rec.ID,
					Reason: ReasonAbsent, NotBefore: confirmedBound(rec.LastConfirmed, rec.LastChanged),
					Last: mustRaw(rec.Props),
				})
			}
		}
	}

	if err := s.appendAndApply(events); err != nil {
		return res, err
	}
	res.Appended = len(events)

	s.foldMu.Lock()
	for id := range seen {
		if rec := s.fold.users[id]; rec != nil {
			rec.LastConfirmed = now
		}
	}
	s.foldMu.Unlock()
	return res, nil
}

// ObserveChannels is ObserveUsers for channels. Channels have no
// deactivated state: an archive is a changed event (archived channels stay
// enumerable and unarchivable), and only absence from a completed
// enumeration tombstones — covering hard deletion and lost visibility
// alike, which is what reason "absent" claims and no more.
func (s *Store) ObserveChannels(channels []slack.Channel, complete bool, src Source, now time.Time) (ObserveResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var events []event
	seen := make(map[string]bool, len(channels))

	for i := range channels {
		c := &channels[i]
		if c.ID == "" {
			continue
		}
		seen[c.ID] = true
		props := ProjectChannel(*c)
		rec := s.fold.channels[c.ID]

		switch {
		case rec == nil:
			events = append(events, event{
				V: schemaVersion, At: now, Src: src, Kind: kindFirstSeen,
				Entity: entityChannel, ID: c.ID,
				Rec: mustRaw(props),
			})

		case rec.Gone != nil:
			events = append(events, event{
				V: schemaVersion, At: now, Src: src, Kind: kindFirstSeen,
				Entity: entityChannel, ID: c.ID,
				Rec: mustRaw(props),
			})

		default:
			if changed := diffChannel(rec.Props, props); len(changed) > 0 {
				events = append(events, event{
					V: schemaVersion, At: now, Src: src, Kind: kindChanged,
					Entity: entityChannel, ID: c.ID,
					Changed: changed, Rec: mustRaw(props),
					NotBefore: confirmedBound(rec.LastConfirmed, rec.LastChanged),
				})
			}
		}
	}

	res := ObserveResult{}
	if complete {
		var proposed []*ChannelRecord
		live := 0
		for id, rec := range s.fold.channels {
			if rec.Gone != nil {
				continue
			}
			live++
			if !seen[id] {
				proposed = append(proposed, rec)
			}
		}
		res.ProposedAbsent = len(proposed)
		if absenceAborts(len(proposed), live) {
			res.AbsenceAborted = true
		} else {
			for _, rec := range proposed {
				events = append(events, event{
					V: schemaVersion, At: now, Src: src, Kind: kindTombstone,
					Entity: entityChannel, ID: rec.ID,
					Reason: ReasonAbsent, NotBefore: confirmedBound(rec.LastConfirmed, rec.LastChanged),
					Last: mustRaw(rec.Props),
				})
			}
		}
	}

	if err := s.appendAndApply(events); err != nil {
		return res, err
	}
	res.Appended = len(events)

	s.foldMu.Lock()
	for id := range seen {
		if rec := s.fold.channels[id]; rec != nil {
			rec.LastConfirmed = now
		}
	}
	s.foldMu.Unlock()
	return res, nil
}

// RecordSweep appends the sweep event that closes a pass and advances the
// per-class watermarks for the classes that completed.
func (s *Store) RecordSweep(rep SweepReport, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	appended := rep.Appended
	durationMs := rep.Duration.Milliseconds()
	e := event{
		V: schemaVersion, At: now, Src: SourceSweep, Kind: kindSweep,
		Users:      &classReport{Complete: rep.Users.Complete, Count: rep.Users.Count, AbsenceAborted: rep.Users.AbsenceAborted, Skipped: rep.Users.Skipped},
		Channels:   &classReport{Complete: rep.Channels.Complete, Count: rep.Channels.Count, ArchivedIncluded: rep.Channels.ArchivedIncluded, AbsenceAborted: rep.Channels.AbsenceAborted, Skipped: rep.Channels.Skipped},
		Membership: &classReport{Complete: rep.Membership.Complete, Count: rep.Membership.Count},
		Appended:   &appended,
		DurationMs: &durationMs,
	}
	return s.appendAndApply([]event{e})
}

// absenceAborts is the mass-tombstone guard: a completed-looking
// enumeration proposing to tombstone more than twenty percent of the live
// fold is treated as a degraded listing, not a mass exodus, and the absence
// pass is refused. The product rests on endpoints whose failure mode is a
// silently shortened result, and a false tombstone lands in a file that is
// never pruned. A single absence always records, so small workspaces still
// tombstone their real departures.
func absenceAborts(proposed, live int) bool {
	return proposed > 1 && proposed*5 > live
}

// confirmedBound picks the notBefore for a change or tombstone: the moment
// the prior state was last positively confirmed, falling back to the last
// recorded change when no confirmation has happened this process.
func confirmedBound(lastConfirmed, lastChanged time.Time) time.Time {
	if !lastConfirmed.IsZero() {
		return lastConfirmed
	}
	return lastChanged
}
