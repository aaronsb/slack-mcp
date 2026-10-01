package safety

import (
	"fmt"
	"time"
)

// Place is a point in the quarantine file that any process can check: the
// number of complete lines read, and the SHA-256 of the file's bytes
// through the last of them. A place compares only while the file still
// holds those same bytes; then lines order entries by where they landed.
// A wall clock would not do: two processes, or one clock stepped back, can
// order a clear before the request it came after. ADR-013 orders the
// unrecorded-block hold the same way, within one process (Position).
type Place struct {
	Line   int    `json:"line"`
	Prefix string `json:"prefix"`
}

// Place is where this read of the quarantine file ended.
func (q QuarantineState) Place() Place { return q.place }

// ClearedSince reports whether k (a person, a conversation, or StrikesKey)
// was cleared after p, and whether p can be compared at all: false when
// the file was edited or replaced below p, or cannot be read.
func (q QuarantineState) ClearedSince(k Key, p Place) (cleared, ordered bool) {
	if q.Err != nil || p.Prefix == "" || p.Line < 0 || p.Line >= len(q.prefixes) || q.prefixes[p.Line] != p.Prefix {
		return false, false
	}
	return q.clearedLine[reasonKey(k)] > p.Line, true
}

// IssueLift issues a lift request (gate case 3), or returns the pending one
// for the same call. A pending lift is reused only while every key it
// lifts is uncleared since it was issued: after a clear, a block that
// closed the key again is one the request never saw, and approving it
// would lift nothing (Approve skips such keys). That request is
// superseded, its ID never issued again, and a new one carries the
// current place.
func (w *Workspace) IssueLift(req Request, now time.Time) (Request, bool, error) {
	st := w.Quarantine.State()
	if st.Err == nil {
		p := st.Place()
		req.At = &p
	}
	return w.Pending.create(req, now, func(old Request) bool { return staleKeys(st, old.Lift, old.At) != nil })
}

// staleKeys returns the keys of a lift issued at at that are not the
// lift's to clear: cleared since it was issued, or not comparable to it
// (no place, or a file that no longer holds its bytes).
func staleKeys(st QuarantineState, keys []Key, at *Place) []Key {
	var stale []Key
	for _, k := range keys {
		if at == nil {
			stale = append(stale, k)
			continue
		}
		if cleared, ordered := st.ClearedSince(k, *at); cleared || !ordered {
			stale = append(stale, k)
		}
	}
	return stale
}

// ClearLift applies an approved lift issued at at: under one hold of the
// lock, on a fresh read, it skips the keys staleKeys names and clears the
// rest. A clear and a new block landing between a separate check and the
// writes would otherwise be wiped. skipped is what it left alone.
func (s *QuarantineStore) ClearLift(keys []Key, at *Place, by, pendingID string, now time.Time) (skipped []Key, err error) {
	for _, k := range keys {
		if err := checkClearable(k); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.j.withLock(func() error {
		s.refreshLocked()
		if s.j.err != nil {
			return fmt.Errorf("%w: %v", ErrUnreadable, s.j.err)
		}
		skipped = staleKeys(s.snapshot(), keys, at)
		for _, k := range keys {
			if containsKey(skipped, k) {
				continue
			}
			if _, err := s.clearLocked(k, by, pendingID, now); err != nil {
				return err
			}
		}
		return nil
	})
	return skipped, err
}
