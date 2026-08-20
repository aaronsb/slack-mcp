package estate

import (
	"encoding/json"
	"time"
)

// Tombstone dates an entity's exit from the estate. The true change lies in
// the interval (NotBefore, At]: At is when we observed the exit, NotBefore
// is when the prior state was last positively confirmed. Answers derived
// from a tombstone report that interval, never a point.
type Tombstone struct {
	At        time.Time
	Reason    string
	NotBefore time.Time
}

// Interval is one closed existence span, kept when an entity re-enters the
// estate after a tombstone.
type Interval struct {
	From time.Time
	To   time.Time
}

// UserRecord is the fold's state for one user.
type UserRecord struct {
	ID    string
	Props UserProps
	// FirstSeen opens the current existence interval; earlier intervals
	// closed by tombstones live in Prior.
	FirstSeen   time.Time
	LastChanged time.Time
	// LastConfirmed is when an observation last positively included this
	// entity. It is an in-memory scalar, never an event — ADR-006's rule
	// that an observation matching the fold writes nothing — so it resets
	// to zero at boot.
	LastConfirmed time.Time
	Gone          *Tombstone
	Prior         []Interval
}

func (r *UserRecord) clone() UserRecord {
	out := *r
	if r.Gone != nil {
		g := *r.Gone
		out.Gone = &g
	}
	if r.Prior != nil {
		out.Prior = append([]Interval(nil), r.Prior...)
	}
	return out
}

// ChannelRecord is the fold's state for one channel.
type ChannelRecord struct {
	ID            string
	Props         ChannelProps
	FirstSeen     time.Time
	LastChanged   time.Time
	LastConfirmed time.Time
	Gone          *Tombstone
	Prior         []Interval
}

func (r *ChannelRecord) clone() ChannelRecord {
	out := *r
	if r.Gone != nil {
		g := *r.Gone
		out.Gone = &g
	}
	if r.Prior != nil {
		out.Prior = append([]Interval(nil), r.Prior...)
	}
	return out
}

// fold is the in-memory current state: built by replay at Open, mutated by
// apply on every append thereafter, so it is never stale relative to the
// running process and the ledger is never re-read during a session.
type fold struct {
	users    map[string]*UserRecord
	channels map[string]*ChannelRecord

	// Per-class sweep watermarks, advanced by sweep events whose class
	// reported complete. They survive restarts because sweep events are
	// ledger lines like any other.
	userSweep    time.Time
	channelSweep time.Time
}

func newFold() fold {
	return fold{
		users:    make(map[string]*UserRecord),
		channels: make(map[string]*ChannelRecord),
	}
}

// apply folds one event in. It is the single mutation path for both boot
// replay and live appends, which is what guarantees that a replayed fold
// equals the live one. Unknown kinds are ignored. The caller holds
// exclusive access (boot) or foldMu (live).
func (f *fold) apply(e *event) {
	switch e.Kind {
	case kindFirstSeen, kindChanged, kindTombstone:
		switch e.Entity {
		case entityUser:
			f.applyUser(e)
		case entityChannel:
			f.applyChannel(e)
		}
	case kindSweep:
		// A skipped class never advances its watermark: the sweep did not
		// enumerate it, and claiming it did would be the false assertion
		// ADR-004 prohibits.
		if e.Users != nil && e.Users.Complete && !e.Users.Skipped {
			f.userSweep = e.At
		}
		if e.Channels != nil && e.Channels.Complete && !e.Channels.Skipped {
			f.channelSweep = e.At
		}
	}
}

func (f *fold) applyUser(e *event) {
	rec := f.users[e.ID]

	switch e.Kind {
	case kindFirstSeen:
		var props UserProps
		if e.Rec != nil {
			_ = json.Unmarshal(e.Rec, &props)
		}
		if rec == nil {
			f.users[e.ID] = &UserRecord{ID: e.ID, Props: props, FirstSeen: e.At, LastChanged: e.At}
			return
		}
		if rec.Gone != nil {
			rec.Prior = append(rec.Prior, Interval{From: rec.FirstSeen, To: rec.Gone.At})
			rec.Gone = nil
			rec.FirstSeen = e.At
		}
		rec.Props = props
		rec.LastChanged = e.At

	case kindChanged:
		var props UserProps
		if e.Rec != nil {
			_ = json.Unmarshal(e.Rec, &props)
		}
		if rec == nil {
			// A change for an unknown entity can only mean lost history (a
			// truncated tail before this line's first-seen). Admit it as
			// first sight rather than dropping the observation.
			f.users[e.ID] = &UserRecord{ID: e.ID, Props: props, FirstSeen: e.At, LastChanged: e.At}
			return
		}
		rec.Props = props
		rec.LastChanged = e.At

	case kindTombstone:
		var last UserProps
		hasLast := e.Last != nil
		if hasLast {
			_ = json.Unmarshal(e.Last, &last)
		}
		if rec == nil {
			rec = &UserRecord{ID: e.ID, FirstSeen: e.At, LastChanged: e.At}
			f.users[e.ID] = rec
		}
		if hasLast {
			rec.Props = last
		}
		rec.Gone = &Tombstone{At: e.At, Reason: e.Reason, NotBefore: e.NotBefore}
	}
}

func (f *fold) applyChannel(e *event) {
	rec := f.channels[e.ID]

	switch e.Kind {
	case kindFirstSeen:
		var props ChannelProps
		if e.Rec != nil {
			_ = json.Unmarshal(e.Rec, &props)
		}
		if rec == nil {
			f.channels[e.ID] = &ChannelRecord{ID: e.ID, Props: props, FirstSeen: e.At, LastChanged: e.At}
			return
		}
		if rec.Gone != nil {
			rec.Prior = append(rec.Prior, Interval{From: rec.FirstSeen, To: rec.Gone.At})
			rec.Gone = nil
			rec.FirstSeen = e.At
		}
		rec.Props = props
		rec.LastChanged = e.At

	case kindChanged:
		var props ChannelProps
		if e.Rec != nil {
			_ = json.Unmarshal(e.Rec, &props)
		}
		if rec == nil {
			f.channels[e.ID] = &ChannelRecord{ID: e.ID, Props: props, FirstSeen: e.At, LastChanged: e.At}
			return
		}
		rec.Props = props
		rec.LastChanged = e.At

	case kindTombstone:
		var last ChannelProps
		hasLast := e.Last != nil
		if hasLast {
			_ = json.Unmarshal(e.Last, &last)
		}
		if rec == nil {
			rec = &ChannelRecord{ID: e.ID, FirstSeen: e.At, LastChanged: e.At}
			f.channels[e.ID] = rec
		}
		if hasLast {
			rec.Props = last
		}
		rec.Gone = &Tombstone{At: e.At, Reason: e.Reason, NotBefore: e.NotBefore}
	}
}
