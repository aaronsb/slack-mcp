// Package estate persists the shape of a Slack workspace over time: which
// users and channels exist, what they are called, and — when they stop
// existing — a dated tombstone rather than a silent gap. ADR-007 is the
// contract; ADR-006 supplies the mechanism it builds on.
//
// The store is an append-only JSONL ledger per workspace plus an in-memory
// fold built by replaying it at Open. Events are appended on change only, so
// a quiet workspace appends nothing and the file stays readable as a change
// log. The estate file is never rewritten: the only write besides appends is
// truncating a torn trailing line left by an interrupted process.
//
// The ledger lives in its own directory, away from the disposable caches,
// for the same reason the watermark does: clearing caches must not take
// accumulated history along. Unlike the watermark it is opened once and
// owned for the process lifetime — the O_APPEND handle and the fold are one
// unit, and an open-per-call pattern would rebuild the fold on every write.
//
// Time is always a parameter. Nothing in this package calls time.Now, so
// every behavior is testable against fixed clocks.
package estate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// Source identifies which observer produced an event. Only completed
// enumerations (the sweep) may assert absence; the source field keeps the
// ledger readable about who saw what.
type Source string

const (
	SourceSweep   Source = "sweep"
	SourceTraffic Source = "traffic"
	SourceBoot    Source = "boot"
)

// Tombstone reasons, matching Slack's two disappearance modes: a user
// enumerated with deleted=true is positively observed gone; an entity
// missing from a completed enumeration that previously contained it is
// absent — hard-deleted, or visibility lost.
const (
	ReasonDeactivated = "deactivated"
	ReasonAbsent      = "absent"
)

// Store is one workspace's estate: the append handle and the fold.
type Store struct {
	path string
	file *os.File

	// writeMu serializes the whole observe unit — diff, append, fold
	// mutation — as one critical section. Two concurrent observers diffing
	// against the same fold state would otherwise double-append the same
	// change. foldMu guards fold reads against the mutation step; lock
	// order is writeMu then foldMu, and readers take only foldMu.
	writeMu sync.Mutex
	foldMu  sync.RWMutex

	fold fold

	lines int64
	bytes int64
}

// Open replays the workspace's estate ledger into a fold and returns the
// store with the file held open for appends. A missing file is an empty
// estate. A torn trailing line is skipped and truncated away; a damaged
// interior line is an error, because only the tail can legally be torn.
func Open(teamID string) (*Store, error) {
	if teamID == "" {
		return nil, fmt.Errorf("estate: empty team ID")
	}

	dir := filepath.Join(paths.DataDir(), "ledger", pathElement(teamID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("estate: create dir: %w", err)
	}

	path := filepath.Join(dir, "estate.jsonl")
	s := &Store{path: path, fold: newFold()}

	rep, err := replayFile(path, time.Time{}, &s.fold)
	if err != nil {
		return nil, err
	}
	if rep.torn {
		if err := os.Truncate(path, rep.tornOffset); err != nil {
			return nil, fmt.Errorf("estate: truncate torn tail: %w", err)
		}
	}
	s.lines = rep.lines
	s.bytes = rep.bytes

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("estate: open ledger: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("estate: chmod ledger: %w", err)
	}
	s.file = f
	return s, nil
}

// Close releases the append handle. The fold needs no flush: every appended
// event is already durable, and the next Open rebuilds from the file.
func (s *Store) Close() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Path returns the ledger file's location.
func (s *Store) Path() string { return s.path }

// Users returns a snapshot copy of every user record, tombstoned included.
func (s *Store) Users() map[string]UserRecord {
	s.foldMu.RLock()
	defer s.foldMu.RUnlock()
	out := make(map[string]UserRecord, len(s.fold.users))
	for id, r := range s.fold.users {
		out[id] = r.clone()
	}
	return out
}

// Channels returns a snapshot copy of every channel record, tombstoned
// included.
func (s *Store) Channels() map[string]ChannelRecord {
	s.foldMu.RLock()
	defer s.foldMu.RUnlock()
	out := make(map[string]ChannelRecord, len(s.fold.channels))
	for id, r := range s.fold.channels {
		out[id] = r.clone()
	}
	return out
}

// User returns one user record by ID.
func (s *Store) User(id string) (UserRecord, bool) {
	s.foldMu.RLock()
	defer s.foldMu.RUnlock()
	r, ok := s.fold.users[id]
	if !ok {
		return UserRecord{}, false
	}
	return r.clone(), true
}

// Channel returns one channel record by ID.
func (s *Store) Channel(id string) (ChannelRecord, bool) {
	s.foldMu.RLock()
	defer s.foldMu.RUnlock()
	r, ok := s.fold.channels[id]
	if !ok {
		return ChannelRecord{}, false
	}
	return r.clone(), true
}

// LastFullSweep reports when the estate last had a complete picture: the
// older of the two per-class sweep watermarks, or zero if either class has
// never been completely enumerated. Coverage reporting reads this; a zero
// value means absence cannot be asserted.
func (s *Store) LastFullSweep() time.Time {
	s.foldMu.RLock()
	defer s.foldMu.RUnlock()
	u, c := s.fold.userSweep, s.fold.channelSweep
	if u.IsZero() || c.IsZero() {
		return time.Time{}
	}
	if u.Before(c) {
		return u
	}
	return c
}

// LastUserSweep reports when users were last completely enumerated, zero if
// never. Schedulers read the per-class watermarks to skip an enumeration
// another observer performed recently.
func (s *Store) LastUserSweep() time.Time {
	s.foldMu.RLock()
	defer s.foldMu.RUnlock()
	return s.fold.userSweep
}

// LastChannelSweep reports when channels were last completely enumerated,
// zero if never.
func (s *Store) LastChannelSweep() time.Time {
	s.foldMu.RLock()
	defer s.foldMu.RUnlock()
	return s.fold.channelSweep
}

// Stats reports the boot-time growth gauge: ADR-007 bounds the estate
// structurally rather than by cap, and these numbers are how that bet is
// watched.
type Stats struct {
	Lines              int64
	Bytes              int64
	Users              int
	Channels           int
	TombstonedUsers    int
	TombstonedChannels int
}

func (s *Store) Stats() Stats {
	s.foldMu.RLock()
	defer s.foldMu.RUnlock()
	st := Stats{
		Lines:    s.lines,
		Bytes:    s.bytes,
		Users:    len(s.fold.users),
		Channels: len(s.fold.channels),
	}
	for _, r := range s.fold.users {
		if r.Gone != nil {
			st.TombstonedUsers++
		}
	}
	for _, r := range s.fold.channels {
		if r.Gone != nil {
			st.TombstonedChannels++
		}
	}
	return st
}

// Fold is a point-in-time reconstruction returned by FoldAsOf.
type Fold struct {
	Users    map[string]UserRecord
	Channels map[string]ChannelRecord
	AsOf     time.Time
	// LastFullSweepBefore is the newest complete sweep at or before AsOf.
	// Every consumer of a reconstruction must confront the observation lag:
	// "as of T" means "as observed by events at or before T", never "as
	// Slack stood at T".
	LastFullSweepBefore time.Time
}

// FoldAsOf rebuilds the estate as observed at or before t by replaying the
// ledger with a cutoff. It reads the file independently of the live fold,
// so it can run beside concurrent appends; an in-flight trailing line reads
// as torn and is skipped, exactly as at boot.
func (s *Store) FoldAsOf(t time.Time) (*Fold, error) {
	f := newFold()
	if _, err := replayFile(s.path, t, &f); err != nil {
		return nil, err
	}
	out := &Fold{
		Users:    make(map[string]UserRecord, len(f.users)),
		Channels: make(map[string]ChannelRecord, len(f.channels)),
		AsOf:     t,
	}
	for id, r := range f.users {
		out.Users[id] = r.clone()
	}
	for id, r := range f.channels {
		out.Channels[id] = r.clone()
	}
	if !f.userSweep.IsZero() && !f.channelSweep.IsZero() {
		if f.userSweep.Before(f.channelSweep) {
			out.LastFullSweepBefore = f.userSweep
		} else {
			out.LastFullSweepBefore = f.channelSweep
		}
	}
	return out, nil
}

// pathElement and sanitize are copied from pkg/watermark, which keeps them
// unexported; the duplication is deliberate while ADR-007's stage 1 touches
// no existing package. Same contract: filesystem-safe, injective via the
// digest suffix, readable via the sanitized prefix.
func pathElement(name string) string {
	sum := sha256.Sum256([]byte(name))
	return sanitize(name) + "-" + hex.EncodeToString(sum[:4])
}

func sanitize(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "unnamed"
	}
	if len(cleaned) > 48 {
		cleaned = cleaned[:48]
	}
	return cleaned
}
