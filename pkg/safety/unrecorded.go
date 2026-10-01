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
// It lifts through ADR-013's clearing path: an operator approval of the lift
// request issued with it (slack-mcp approve), observed on the next check.
// When no lift request could be issued either, it lifts only when the
// server restarts.
type unrecordedHold struct {
	mu     sync.Mutex
	held   bool
	liftID string
}

// HoldUnrecorded engages the hold after a block failed to record. liftID
// names the lift request issued for it, or is empty when none could be.
func (w *Workspace) HoldUnrecorded(liftID string) {
	w.unrecorded.mu.Lock()
	defer w.unrecorded.mu.Unlock()
	w.unrecorded.held = true
	if liftID != "" {
		w.unrecorded.liftID = liftID
	}
}

// UnrecordedHeld reports whether the hold is engaged, with the lift
// request's ID. An approved lift request releases it.
func (w *Workspace) UnrecordedHeld(now time.Time) (bool, string) {
	w.unrecorded.mu.Lock()
	defer w.unrecorded.mu.Unlock()
	if !w.unrecorded.held {
		return false, ""
	}
	if id := w.unrecorded.liftID; id != "" {
		if r, ok := w.Pending.Lookup(id, now); ok && r.Status == StatusConsumed {
			w.unrecorded.held, w.unrecorded.liftID = false, ""
			return false, ""
		}
	}
	return true, w.unrecorded.liftID
}
