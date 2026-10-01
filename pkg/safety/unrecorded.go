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
// approve), or a clear of strikes written after it engaged (slack-mcp
// quarantine clear strikes, from any process). It is in memory, so a
// restart also lifts it.
type unrecordedHold struct {
	mu      sync.Mutex
	held    bool
	engaged time.Time
	liftID  string
}

// HoldUnrecorded engages the hold after a block failed to record at now.
// liftID names the lift request issued for it, or is empty when none
// could be.
func (w *Workspace) HoldUnrecorded(liftID string, now time.Time) {
	w.unrecorded.mu.Lock()
	defer w.unrecorded.mu.Unlock()
	w.unrecorded.held, w.unrecorded.engaged, w.unrecorded.liftID = true, now, liftID
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
	release := w.Quarantine.State().StrikesCleared.After(w.unrecorded.engaged)
	if id := w.unrecorded.liftID; id != "" && !release {
		r, ok := w.Pending.Lookup(id, now)
		release = ok && r.Status == StatusConsumed
	}
	if release {
		w.unrecorded.held, w.unrecorded.engaged, w.unrecorded.liftID = false, time.Time{}, ""
		return false, ""
	}
	return true, w.unrecorded.liftID
}
