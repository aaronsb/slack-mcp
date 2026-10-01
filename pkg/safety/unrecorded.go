package safety

import (
	"sync"
	"time"
)

// unrecordedHold is the in-process lock a block that could not be written
// to the quarantine file leaves behind. The block's strike and quarantine
// are not in the file, so the file alone would let the next call through;
// the hold refuses every say and mark-read in this process instead.
//
// It lifts through ADR-013's clearing path, observed on the next check: an
// operator approval of the lift request issued with it (slack-mcp
// approve), or a clear of strikes appended to the quarantine file after the
// position where it engaged (slack-mcp quarantine clear strikes, from any
// process). Position, not time, orders the two, so no clock can release it
// early; a rebuild of the file's fold since then leaves only the approval
// and a restart. It is in memory, so a restart also lifts it.
type unrecordedHold struct {
	mu      sync.Mutex
	held    bool
	engaged Position
	liftID  string
}

// unorderable is the engage position of a hold taken while the quarantine
// file could not be read. No state has its epoch, so only an approval or a
// restart lifts the hold.
var unorderable = Position{Epoch: -1}

// HoldUnrecorded engages the hold after a block failed to record. liftID
// names the lift request issued for it, or is empty when none could be.
func (w *Workspace) HoldUnrecorded(liftID string) {
	st := w.Quarantine.State()
	pos := st.Position
	if st.Err != nil {
		// No readable position to order a clear after: the fold rebuilt
		// on recovery would number old clears past it.
		pos = unorderable
	}
	w.unrecorded.mu.Lock()
	defer w.unrecorded.mu.Unlock()
	w.unrecorded.held, w.unrecorded.engaged, w.unrecorded.liftID = true, pos, liftID
}

// RetryUnrecordedLift names a lift request issued for an engaged hold
// after the first attempt failed or its request ended unapproved.
func (w *Workspace) RetryUnrecordedLift(liftID string) {
	w.unrecorded.mu.Lock()
	defer w.unrecorded.mu.Unlock()
	if w.unrecorded.held && liftID != "" {
		w.unrecorded.liftID = liftID
	}
}

// UnrecordedHeld reports whether the hold is engaged, with the lift
// request's ID.
func (w *Workspace) UnrecordedHeld(now time.Time) (bool, string) {
	w.unrecorded.mu.Lock()
	defer w.unrecorded.mu.Unlock()
	if !w.unrecorded.held {
		return false, ""
	}
	release := w.Quarantine.State().ClearedStrikesAfter(w.unrecorded.engaged)
	if id := w.unrecorded.liftID; id != "" && !release {
		r, ok := w.Pending.Lookup(id, now)
		release = ok && r.Status == StatusConsumed
	}
	if release {
		w.unrecorded.held, w.unrecorded.engaged, w.unrecorded.liftID = false, Position{}, ""
		return false, ""
	}
	return true, w.unrecorded.liftID
}
