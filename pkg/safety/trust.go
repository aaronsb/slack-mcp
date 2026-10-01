package safety

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// TrustSource is how a trust entry was added.
type TrustSource string

const (
	SourceCLI         TrustSource = "cli"
	SourceElicitation TrustSource = "elicitation"
)

// Trust entry kinds.
const (
	kindAdd    = "add"
	kindRemove = "remove"
	kindUse    = "use"
)

// trustLine is one line of the trust file.
type trustLine struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	Key  Key       `json:"key"`

	// Add.
	DestKind  DestKind    `json:"dest_kind,omitempty"`
	Cases     []Case      `json:"cases,omitempty"`
	Source    TrustSource `json:"source,omitempty"`
	Posture   Posture     `json:"posture,omitempty"`
	PendingID string      `json:"pending_id,omitempty"`
	Expires   *time.Time  `json:"expires,omitempty"`
	Parties   []string    `json:"parties,omitempty"`

	// Use: the destination trust let through, and its name then.
	Destination     string `json:"destination,omitempty"`
	DestinationName string `json:"destination_name,omitempty"`
}

// TrustEntry is one add, as folded.
type TrustEntry struct {
	Key       Key
	DestKind  DestKind
	Cases     []Case
	Source    TrustSource
	Posture   Posture
	PendingID string
	Added     time.Time
	Expires   *time.Time
	// Parties are the external parties recorded for a conversation's case
	// 1: team IDs for a channel, user IDs for a DM or group DM.
	Parties []string
}

// Covers reports whether the entry trusts case c. Case 3 is never trusted.
func (e TrustEntry) Covers(c Case) bool {
	return c != CaseLift && hasCase(e.Cases, c)
}

// Expired reports whether the entry's --for duration has run out.
func (e TrustEntry) Expired(now time.Time) bool {
	return e.Expires != nil && !now.Before(*e.Expires)
}

type trustSlot struct {
	last     *TrustEntry // the latest add since the last remove, any source
	lastCLI  *TrustEntry // the latest CLI add since the last remove
	lastUsed time.Time
}

// TrustStore is one workspace's trust file and its fold.
type TrustStore struct {
	mu      sync.Mutex
	j       *journal
	posture Posture
	slots   map[string]*trustSlot
}

func newTrustStore(path string, p Posture) *TrustStore {
	s := &TrustStore{j: newJournal(path), posture: p}
	s.reset()
	return s
}

func (s *TrustStore) reset() { s.slots = map[string]*trustSlot{} }

func slotKey(k Key) string { return string(k.Kind) + ":" + k.ID }

func (s *TrustStore) apply(raw []byte) error {
	var l trustLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return err
	}
	if (l.Key.Kind != KeyPerson && l.Key.Kind != KeyConversation) || l.Key.ID == "" {
		return errors.New("trust line without a valid key")
	}
	sk := slotKey(l.Key)
	slot := s.slots[sk]
	if slot == nil {
		slot = &trustSlot{}
		s.slots[sk] = slot
	}
	switch l.Kind {
	case kindAdd:
		if l.Source != SourceCLI && l.Source != SourceElicitation {
			return fmt.Errorf("unknown source %q", l.Source)
		}
		e := &TrustEntry{
			Key: l.Key, DestKind: l.DestKind, Cases: normCases(l.Cases), Source: l.Source,
			Posture: l.Posture, PendingID: l.PendingID, Added: l.Time, Expires: l.Expires,
			Parties: append([]string(nil), l.Parties...),
		}
		slot.last = e
		if e.Source == SourceCLI {
			slot.lastCLI = e
		}
	case kindRemove:
		slot.last, slot.lastCLI = nil, nil
	case kindUse:
		slot.lastUsed = l.Time
	default:
		return fmt.Errorf("unknown kind %q", l.Kind)
	}
	return nil
}

// TrustState is one read of the trust file.
type TrustState struct {
	Posture Posture
	// Err is set when the file exists and cannot be read: nothing is
	// trusted, and no lock follows, since that can only gate more.
	Err       error
	Malformed []int
	slots     map[string]trustSlot
}

// State reads the file on from its last offset and returns the state.
func (s *TrustStore) State() TrustState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.j.refresh(s.reset, s.apply)
	st := TrustState{Posture: s.posture, Err: s.j.err, Malformed: s.j.malformedLines(), slots: map[string]trustSlot{}}
	for k, v := range s.slots {
		st.slots[k] = *v
	}
	return st
}

// effective returns the entry in force for a key: in strict the latest CLI
// add (an elicitation add is ignored and replaces nothing), in soft the
// latest add. An expired entry is not in force.
func (t TrustState) effective(k Key, now time.Time) (TrustEntry, bool) {
	if t.Err != nil {
		return TrustEntry{}, false
	}
	slot, ok := t.slots[slotKey(k)]
	if !ok {
		return TrustEntry{}, false
	}
	e := slot.last
	if t.Posture != Soft {
		e = slot.lastCLI
	}
	if e == nil || e.Expired(now) {
		return TrustEntry{}, false
	}
	return *e, true
}

// TrustQuery is what the gate asks of the trust list for one case.
type TrustQuery struct {
	Destination Destination
	Case        Case
	// For CaseExternal: the external parties the gate found now (team IDs
	// for a channel, external member user IDs for a group DM, the other
	// member for a DM), and whether it could enumerate them at all.
	Parties      []string
	PartiesKnown bool
	// For CaseExternal on a group DM: members the gate found internal.
	// Internal stands in for trust in case 1 only.
	Internal map[string]bool
}

// Trusted reports whether trust lets q's case past the gate, and the entry
// that did. A call hitting both cases asks once per case and passes only
// when both are trusted.
//
//   - A person entry covers their DM. A group DM is covered when every
//     other member is trusted for the case, or, for case 1 only, internal.
//   - A conversation entry covers that conversation. For case 1 it applies
//     only when the gate enumerated the parties, found at least one, and
//     every one is among those recorded at add time.
func (t TrustState) Trusted(q TrustQuery, now time.Time) (TrustEntry, bool) {
	if q.Case != CaseExternal && q.Case != CaseCrossConversation {
		return TrustEntry{}, false
	}
	d := q.Destination
	if d.ConversationID != "" {
		if e, ok := t.effective(Conversation(d.ConversationID, ""), now); ok && e.Covers(q.Case) {
			if q.Case != CaseExternal || partiesWithin(q, e.Parties) {
				return e, true
			}
		}
	}
	switch d.Kind {
	case DestDM, DestPerson:
		if len(d.Members) != 1 {
			return TrustEntry{}, false
		}
		if e, ok := t.effective(Person(d.Members[0].ID, ""), now); ok && e.Covers(q.Case) {
			return e, true
		}
	case DestGroupDM:
		if len(d.Members) == 0 {
			return TrustEntry{}, false
		}
		var used *TrustEntry
		for _, m := range d.Members {
			if e, ok := t.effective(Person(m.ID, ""), now); ok && e.Covers(q.Case) {
				if used == nil {
					e := e
					used = &e
				}
				continue
			}
			if q.Case == CaseExternal && q.Internal[m.ID] {
				continue
			}
			return TrustEntry{}, false
		}
		if used != nil {
			return *used, true
		}
	}
	return TrustEntry{}, false
}

func partiesWithin(q TrustQuery, recorded []string) bool {
	if !q.PartiesKnown || len(q.Parties) == 0 {
		return false
	}
	set := make(map[string]bool, len(recorded))
	for _, p := range recorded {
		set[p] = true
	}
	for _, p := range q.Parties {
		if !set[p] {
			return false
		}
	}
	return true
}

// Trust listing states.
const (
	TrustInEffect    = "in effect"
	TrustExpired     = "expired"
	TrustQuarantined = "quarantined"
	TrustIgnored     = "ignored in strict"
)

// TrustListing is one entry as `slack-mcp trust list` shows it.
type TrustListing struct {
	Entry    TrustEntry
	State    string
	LastUsed time.Time
}

// List returns every entry in force or shadowed, with its state under the
// posture. In strict an elicitation entry is listed as ignored, beside the
// earlier CLI entry that stays in effect.
func (t TrustState) List(now time.Time, q QuarantineState) []TrustListing {
	var out []TrustListing
	for _, slot := range t.slots {
		var shown []*TrustEntry
		if slot.last != nil {
			shown = append(shown, slot.last)
		}
		if t.Posture != Soft && slot.lastCLI != nil && slot.lastCLI != slot.last {
			shown = append(shown, slot.lastCLI)
		}
		for _, e := range shown {
			state := TrustInEffect
			switch {
			case t.Posture != Soft && e.Source == SourceElicitation:
				state = TrustIgnored
			case e.Expired(now):
				state = TrustExpired
			default:
				if _, quarantined := q.IsQuarantined(e.Key.ID); quarantined {
					state = TrustQuarantined
				}
			}
			out = append(out, TrustListing{Entry: *e, State: state, LastUsed: slot.lastUsed})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Entry.Key.Name != out[j].Entry.Key.Name {
			return out[i].Entry.Key.Name < out[j].Entry.Key.Name
		}
		return out[i].Entry.Added.Before(out[j].Entry.Added)
	})
	return out
}

// Entries reports whether any add for k is recorded since its last remove,
// whatever the posture: what `trust remove` has to remove.
func (t TrustState) Entries(k Key) bool {
	slot, ok := t.slots[slotKey(k)]
	return ok && (slot.last != nil || slot.lastCLI != nil)
}

// InEffect reports whether an entry for k is in force under the state's
// posture now: what `quarantine clear` says applies again.
func (t TrustState) InEffect(k Key, now time.Time) bool {
	_, ok := t.effective(k, now)
	return ok
}

// TrustAdd is an entry to add. The posture recorded is the store's, never
// the caller's.
type TrustAdd struct {
	Time     time.Time
	Key      Key
	DestKind DestKind
	Cases    []Case
	Source   TrustSource
	// PendingID is the request an elicitation answered.
	PendingID string
	Expires   *time.Time
	Parties   []string
}

// ErrStrictElicitationTrust refuses trust added by elicitation in strict.
var ErrStrictElicitationTrust = errors.New("strict posture: trust is added only through the CLI")

// Add appends an add. A later add for the same key replaces the earlier
// one's cases and expiry.
func (s *TrustStore) Add(a TrustAdd) error {
	if (a.Key.Kind != KeyPerson && a.Key.Kind != KeyConversation) || a.Key.ID == "" {
		return errors.New("trust needs a person or conversation ID")
	}
	cases := normCases(a.Cases)
	if len(cases) == 0 {
		return errors.New("trust needs at least one case")
	}
	for _, c := range cases {
		if c != CaseExternal && c != CaseCrossConversation {
			return fmt.Errorf("case %q cannot be trusted", c)
		}
	}
	switch a.Source {
	case SourceCLI:
	case SourceElicitation:
		if s.posture != Soft {
			return ErrStrictElicitationTrust
		}
	default:
		return fmt.Errorf("unknown trust source %q", a.Source)
	}
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	line := trustLine{
		Time: a.Time.UTC(), Kind: kindAdd, Key: a.Key, DestKind: a.DestKind, Cases: cases,
		Source: a.Source, Posture: s.posture, PendingID: a.PendingID, Expires: a.Expires,
		Parties: normStrings(a.Parties),
	}
	return s.write(line)
}

// Remove ends every case for k. It reports false, writing nothing, when
// nothing is recorded for k.
func (s *TrustStore) Remove(k Key, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	err := s.j.withLock(func() error {
		s.j.refresh(s.reset, s.apply)
		if s.j.err != nil {
			return fmt.Errorf("trust list cannot be read: %w", s.j.err)
		}
		slot := s.slots[slotKey(k)]
		if slot == nil || (slot.last == nil && slot.lastCLI == nil) {
			return nil
		}
		removed = true
		raw, err := json.Marshal(trustLine{Time: now.UTC(), Kind: kindRemove, Key: k})
		if err != nil {
			return err
		}
		return s.j.appendLine(raw)
	})
	return removed, err
}

// TrustUse is a send trust let through.
type TrustUse struct {
	Time        time.Time
	Entry       TrustEntry
	Destination Destination
	Cases       []Case
}

// RecordUse appends a use entry: the destination and the cases trust let
// through, nothing of the content. Uses change no trust state.
func (s *TrustStore) RecordUse(u TrustUse) error {
	if u.Time.IsZero() {
		u.Time = time.Now()
	}
	return s.write(trustLine{
		Time: u.Time.UTC(), Kind: kindUse, Key: u.Entry.Key, Cases: normCases(u.Cases),
		Source: u.Entry.Source, Destination: u.Destination.ID(), DestinationName: u.Destination.Name,
	})
}

func (s *TrustStore) write(line trustLine) error {
	raw, err := json.Marshal(line)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.j.withLock(func() error {
		if err := s.j.appendLine(raw); err != nil {
			return err
		}
		s.j.refresh(s.reset, s.apply)
		return nil
	})
}

func normStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
