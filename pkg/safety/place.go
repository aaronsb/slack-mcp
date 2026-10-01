package safety

import "time"

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
	if q.Err != nil || p.Prefix == "" || p.Line >= len(q.prefixes) || q.prefixes[p.Line] != p.Prefix {
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
	return w.Pending.create(req, now, func(old Request) bool { return liftStale(st, old) != nil })
}

// liftStale returns the keys of a lift request that are not the request's
// to clear: cleared since it was issued, or not comparable to it.
func liftStale(st QuarantineState, r Request) []Key {
	var stale []Key
	for _, k := range r.Lift {
		if r.At == nil {
			stale = append(stale, k)
			continue
		}
		if cleared, ordered := st.ClearedSince(k, *r.At); cleared || !ordered {
			stale = append(stale, k)
		}
	}
	return stale
}
