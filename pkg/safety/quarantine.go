package safety

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Quarantine entry kinds.
const (
	kindBlock = "block"
	kindClear = "clear"
)

// Who appended a clear.
const (
	ByCLI      = "cli"
	ByApproval = "approval"
	// ByWeb is the local clearing page (ADR-013, #132).
	ByWeb = "web"
)

// Reason is what the quarantine file keeps about a block: when, where it
// was going, and the scanner's class and field. The matched value is
// never recorded, so it is never here.
type Reason struct {
	Time        time.Time
	Destination string
	Class       string
	// Location: text, reaction, upload-title, file-name, or file-bytes;
	// File is the 1-based position of the file for file locations.
	Location string
	File     int
}

// quarantineLine is one line of the quarantine file.
type quarantineLine struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`

	// Block.
	Posture     Posture      `json:"posture,omitempty"`
	Quarantined *bool        `json:"quarantined,omitempty"`
	Strike      int          `json:"strike,omitempty"`
	Lock        bool         `json:"lock,omitempty"`
	Destination *Destination `json:"destination,omitempty"`
	Keys        []Key        `json:"keys,omitempty"`
	Call        *CallRecord  `json:"call,omitempty"`
	Match       *MatchRecord `json:"match,omitempty"`

	// Clear.
	Target    *Key   `json:"target,omitempty"`
	By        string `json:"by,omitempty"`
	PendingID string `json:"pending_id,omitempty"`
}

// QuarantineStore is one workspace's quarantine file and its fold.
type QuarantineStore struct {
	mu      sync.Mutex
	j       *journal
	posture Posture
	self    string // the account's own user ID, never a quarantine key

	people       map[string]Key
	convs        map[string]Key
	strikes      int
	lockRecorded bool
	// reasons holds the latest block's reason per key in force (by
	// reasonKey); strikeReasons the reasons of the blocks counted since
	// the last clear of strikes, oldest first.
	reasons       map[string]Reason
	strikeReasons []Reason
	// clearedAt is the time of the latest clear of each key (by
	// reasonKey), strikes included.
	clearedAt map[string]time.Time
	// epoch counts rebuilds of a fold that had read a line (a replaced,
	// shrunk, or edited file read again from offset 0); a line number means
	// something only within one epoch. A fold that had read nothing (no
	// file yet) keeps its epoch: every line is after position 0 in either
	// file. strikesClearLine is the line of the latest clear of strikes,
	// or 0.
	epoch            int
	readLine         bool
	strikesClearLine int
}

func newQuarantineStore(path string, p Posture, self string) *QuarantineStore {
	s := &QuarantineStore{j: newJournal(path), posture: p, self: self}
	s.reset()
	return s
}

func (s *QuarantineStore) reset() {
	s.people = map[string]Key{}
	s.convs = map[string]Key{}
	s.strikes = 0
	s.lockRecorded = false
	s.reasons = map[string]Reason{}
	s.strikeReasons = nil
	s.clearedAt = map[string]time.Time{}
	if s.readLine {
		s.epoch++
	}
	s.readLine = false
	s.strikesClearLine = 0
}

func (s *QuarantineStore) apply(raw []byte) error {
	s.readLine = true
	var l quarantineLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return err
	}
	switch l.Kind {
	case kindBlock:
		if l.Destination == nil {
			return errors.New("block without destination")
		}
		s.strikes++
		if l.Lock {
			s.lockRecorded = true
		}
		r := Reason{Time: l.Time, Destination: l.Destination.Name}
		if l.Match != nil {
			r.Class, r.Location, r.File = l.Match.Class, l.Match.Location, l.Match.File
		}
		s.strikeReasons = append(s.strikeReasons, r)
		if l.Quarantined != nil && *l.Quarantined {
			for _, k := range l.Keys {
				switch k.Kind {
				case KeyPerson:
					s.people[k.ID] = k
				case KeyConversation:
					s.convs[k.ID] = k
				default:
					continue
				}
				s.reasons[reasonKey(k)] = r
			}
		}
	case kindClear:
		if l.Target == nil {
			return errors.New("clear without target")
		}
		if rk := reasonKey(*l.Target); l.Time.After(s.clearedAt[rk]) {
			s.clearedAt[rk] = l.Time
		}
		switch l.Target.Kind {
		case KeyStrikes:
			s.strikes = 0
			s.lockRecorded = false
			s.strikeReasons = nil
			s.strikesClearLine = s.j.lines
		case KeyPerson:
			delete(s.people, l.Target.ID)
			delete(s.reasons, reasonKey(*l.Target))
		case KeyConversation:
			delete(s.convs, l.Target.ID)
			delete(s.reasons, reasonKey(*l.Target))
		default:
			return fmt.Errorf("clear of unknown kind %q", l.Target.Kind)
		}
	default:
		return fmt.Errorf("unknown kind %q", l.Kind)
	}
	return nil
}

func reasonKey(k Key) string { return string(k.Kind) + "\x00" + k.ID }

func (s *QuarantineStore) refreshLocked() {
	s.j.refresh(s.reset, s.apply)
}

// QuarantineState is one read of the quarantine file: what a gated call or
// a banner works from. A call reads it once.
type QuarantineState struct {
	Posture Posture
	// People and Conversations in force, sorted by name.
	People        []Key
	Conversations []Key
	// Strikes since the last clear of strikes; Limit is the posture's.
	Strikes int
	Limit   int
	// Err is set when the file exists and cannot be read. The state then
	// fails closed: LockEngaged is true.
	Err error
	// Malformed lists the 1-based line numbers skipped as unparseable.
	Malformed []int
	// Position is where this read ended; StrikesClearLine is the line of
	// the latest clear of strikes in the same epoch, or 0.
	Position         Position
	StrikesClearLine int
	// StrikeReasons are the blocks counted in Strikes, oldest first.
	StrikeReasons []Reason

	reasons      map[string]Reason
	clearedAt    map[string]time.Time
	people       map[string]Key
	convs        map[string]Key
	lockRecorded bool
}

// Position is a place in the quarantine file: the number of complete lines
// read, within one epoch of the fold. Comparing positions orders writes by
// where they landed in the file, not by any clock.
type Position struct {
	Epoch int
	Line  int
}

// ClearedStrikesAfter reports whether a clear of strikes was appended after
// p. A rebuild since p (the file replaced, shrunk, or edited) makes p
// meaningless, so the answer is then false.
func (q QuarantineState) ClearedStrikesAfter(p Position) bool {
	return q.Err == nil && q.Position.Epoch == p.Epoch && q.StrikesClearLine > p.Line
}

// State reads the file on from its last offset and returns the state.
func (s *QuarantineStore) State() QuarantineState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.snapshot()
}

func (s *QuarantineStore) snapshot() QuarantineState {
	st := QuarantineState{
		Posture:          s.posture,
		Strikes:          s.strikes,
		Limit:            StrikeLimit(s.posture),
		Err:              s.j.err,
		Malformed:        s.j.malformedLines(),
		Position:         Position{Epoch: s.epoch, Line: s.j.lines},
		StrikesClearLine: s.strikesClearLine,
		StrikeReasons:    append([]Reason(nil), s.strikeReasons...),
		reasons:          make(map[string]Reason, len(s.reasons)),
		clearedAt:        make(map[string]time.Time, len(s.clearedAt)),
		people:           make(map[string]Key, len(s.people)),
		convs:            make(map[string]Key, len(s.convs)),
		lockRecorded:     s.lockRecorded,
	}
	for rk, r := range s.reasons {
		st.reasons[rk] = r
	}
	for rk, at := range s.clearedAt {
		st.clearedAt[rk] = at
	}
	for id, k := range s.people {
		st.people[id] = k
		st.People = append(st.People, k)
	}
	for id, k := range s.convs {
		st.convs[id] = k
		st.Conversations = append(st.Conversations, k)
	}
	sortKeys(st.People)
	sortKeys(st.Conversations)
	return st
}

func sortKeys(ks []Key) {
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].Name != ks[j].Name {
			return ks[i].Name < ks[j].Name
		}
		return ks[i].ID < ks[j].ID
	})
}

// LockEngaged reports whether every say and mark-read is refused: the
// strikes reached the posture's limit, a block recorded that it engaged the
// lock (so relaxing the posture does not lift a lock), or the file could not
// be read.
func (q QuarantineState) LockEngaged() bool {
	return q.Err != nil || q.lockRecorded || q.Strikes >= q.Limit
}

// ReasonFor returns the latest block's reason for a person or conversation
// in force.
func (q QuarantineState) ReasonFor(k Key) (Reason, bool) {
	r, ok := q.reasons[reasonKey(k)]
	return r, ok
}

// ClearedSince reports whether k (a person, a conversation, or
// StrikesKey) was cleared after t, by any route.
func (q QuarantineState) ClearedSince(k Key, t time.Time) bool {
	return q.clearedAt[reasonKey(k)].After(t)
}

// StrikeCount returns the strikes since the last clear of strikes.
func (q QuarantineState) StrikeCount() int { return q.Strikes }

// IsQuarantined reports whether a person (user ID) or a conversation
// (conversation ID) is quarantined, with the key as recorded.
func (q QuarantineState) IsQuarantined(id string) (Key, bool) {
	if k, ok := q.people[id]; ok {
		return k, true
	}
	k, ok := q.convs[id]
	return k, ok
}

// Check returns the keys that close d to writes: its conversation, and for
// a DM or group DM each member who is quarantined. The self-DM is never
// quarantined. Empty means open.
func (q QuarantineState) Check(d Destination) []Key {
	if d.Kind == DestSelf {
		return nil
	}
	var out []Key
	if d.ConversationID != "" {
		if k, ok := q.convs[d.ConversationID]; ok {
			out = append(out, k)
		}
	}
	for _, m := range d.Members {
		if k, ok := q.people[m.ID]; ok {
			out = append(out, k)
		}
	}
	return out
}

// Any reports whether there is anything for the banner to say.
func (q QuarantineState) Any() bool {
	return q.Err != nil || len(q.people) > 0 || len(q.convs) > 0 || q.Strikes > 0 || q.lockRecorded || len(q.Malformed) > 0
}

// Block is a scanner block to record.
type Block struct {
	Time        time.Time
	Destination Destination
	Call        CallRecord
	Match       MatchRecord
}

// BlockOutcome is what the posture made of a block.
type BlockOutcome struct {
	Posture Posture
	// Strike is this block's strike number; Limit the posture's.
	Strike int
	Limit  int
	// Quarantined: the destination's keys were closed (Keys). A notice is
	// the caller's to post, except to an external destination or a person
	// with no DM.
	Quarantined bool
	Keys        []Key
	// Warned: soft posture, first block, nothing quarantined.
	Warned bool
	// LockEngaged: this block reached the strike limit.
	LockEngaged bool
}

// ErrUnreadable is returned by a write when the file cannot be read, so the
// posture cannot be applied. The caller still refuses the call.
var ErrUnreadable = errors.New("quarantine state cannot be read")

// RecordBlock appends a block and applies the posture (ADR-014):
//
//	strict: every block quarantines its destination; the 2nd engages the lock.
//	soft:   the 1st block warns; the 2nd and 3rd quarantine; the 3rd engages
//	        the lock.
//
// The self-DM is never quarantined; a block there still counts a strike.
func (s *QuarantineStore) RecordBlock(b Block) (BlockOutcome, error) {
	if b.Time.IsZero() {
		b.Time = time.Now()
	}
	d := b.Destination
	if d.Kind != DestSelf && !isSelfDM(d, s.self) && len(d.QuarantineKeys(s.self)) == 0 {
		return BlockOutcome{}, errors.New("block on a destination with no conversation ID and no member")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var out BlockOutcome
	err := s.j.withLock(func() error {
		s.refreshLocked()
		if s.j.err != nil {
			return fmt.Errorf("%w: %v", ErrUnreadable, s.j.err)
		}
		out = decideBlock(s.posture, s.strikes+1, d, s.self)
		q := out.Quarantined
		call, match := b.Call, b.Match
		line := quarantineLine{
			Time:        b.Time.UTC(),
			Kind:        kindBlock,
			Posture:     s.posture,
			Quarantined: &q,
			Strike:      out.Strike,
			Lock:        out.LockEngaged,
			Destination: &d,
			Keys:        out.Keys,
			Call:        &call,
			Match:       &match,
		}
		raw, err := json.Marshal(line)
		if err != nil {
			return err
		}
		if err := s.j.appendLine(raw); err != nil {
			return err
		}
		s.refreshLocked()
		return nil
	})
	return out, err
}

// decideBlock is the posture table.
func decideBlock(p Posture, strike int, d Destination, self string) BlockOutcome {
	out := BlockOutcome{Posture: p, Strike: strike, Limit: StrikeLimit(p)}
	selfDM := d.Kind == DestSelf || isSelfDM(d, self)
	if !selfDM && (p == Strict || strike >= 2) {
		out.Keys = d.QuarantineKeys(self)
		out.Quarantined = len(out.Keys) > 0
	}
	out.Warned = !out.Quarantined && !selfDM
	out.LockEngaged = strike >= out.Limit
	return out
}

// Clear appends a clear of target: a person, a conversation, or
// StrikesKey, and reports whether anything was in force. A person or
// conversation not in force writes nothing. A clear of strikes is always
// written: a server holding writes after a block it could not record
// releases on seeing one. by is ByCLI, ByWeb, or ByApproval; pendingID names the
// lift request an approval answered.
func (s *QuarantineStore) Clear(target Key, by, pendingID string, now time.Time) (bool, error) {
	switch target.Kind {
	case KeyPerson, KeyConversation:
		if target.ID == "" {
			return false, errors.New("clear needs an ID")
		}
	case KeyStrikes:
	default:
		return false, fmt.Errorf("cannot clear a %q", target.Kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cleared := false
	err := s.j.withLock(func() error {
		s.refreshLocked()
		if s.j.err != nil {
			return fmt.Errorf("%w: %v", ErrUnreadable, s.j.err)
		}
		switch target.Kind {
		case KeyStrikes:
			cleared = s.strikes > 0 || s.lockRecorded
		case KeyPerson:
			_, cleared = s.people[target.ID]
		case KeyConversation:
			_, cleared = s.convs[target.ID]
		}
		if !cleared && target.Kind != KeyStrikes {
			return nil
		}
		t := target
		raw, err := json.Marshal(quarantineLine{Time: now.UTC(), Kind: kindClear, Target: &t, By: by, PendingID: pendingID})
		if err != nil {
			return err
		}
		if err := s.j.appendLine(raw); err != nil {
			return err
		}
		s.refreshLocked()
		return nil
	})
	return cleared, err
}

// isSelfDM reports a DM whose only member is the account itself.
func isSelfDM(d Destination, self string) bool {
	if d.Kind != DestDM || self == "" || len(d.Members) == 0 {
		return false
	}
	for _, m := range d.Members {
		if m.ID != self {
			return false
		}
	}
	return true
}
