package provider

// The channel walk survives restarts. A reconnect can kill the server at
// any moment without warning, and a walk that restarts from page one never
// finishes on a workspace whose walk takes longer than the gap between
// reconnects. The walk therefore checkpoints its cursor and seen set to a
// state file per page; the next boot resumes from the cursor with the seen
// set intact, and the absence pass runs against the union. An invalid or
// expired cursor — or a checkpoint older than an hour — restarts clean.
//
// Soundness: a single-process walk is already temporally smeared over its
// own duration, so a resumed walk claims nothing weaker, and the
// mass-tombstone guard backstops any stitch the cursor hides.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	walkStateFile    = "walk-state.json"
	walkResumeAgeMax = time.Hour
)

type walkState struct {
	Cursor    string    `json:"cursor"`
	StartedAt time.Time `json:"startedAt"`
	// SavedAt is when this checkpoint was written. Age-out keys on it —
	// keying on StartedAt would measure cumulative time since the walk's
	// first attempt, so any walk not finished within the cap would restart
	// from page one forever: the exact failure resumability exists to fix.
	SavedAt time.Time `json:"savedAt"`
	Seen    []string  `json:"seen"`
}

// walkStatePath returns where this workspace's walk checkpoint lives, or ""
// when there is no writable estate — readers and ledger-less servers walk
// without checkpointing, as before.
func (ap *ApiProvider) walkStatePath() string {
	st := ap.est()
	if st == nil || st.ReadOnly() {
		return ""
	}
	return filepath.Join(filepath.Dir(st.Path()), walkStateFile)
}

// loadWalkState returns a resumable checkpoint, or ok=false when none
// exists, it is too old, or it cannot be read.
func (ap *ApiProvider) loadWalkState() (cursor string, startedAt time.Time, seen map[string]bool, ok bool) {
	path := ap.walkStatePath()
	if path == "" {
		return "", time.Time{}, nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, nil, false
	}
	var st walkState
	if err := json.Unmarshal(data, &st); err != nil || st.Cursor == "" {
		return "", time.Time{}, nil, false
	}
	savedAt := st.SavedAt
	if savedAt.IsZero() {
		savedAt = st.StartedAt
	}
	if time.Since(savedAt) > walkResumeAgeMax {
		ap.clearWalkState()
		return "", time.Time{}, nil, false
	}
	seen = make(map[string]bool, len(st.Seen))
	for _, id := range st.Seen {
		seen[id] = true
	}
	return st.Cursor, st.StartedAt, seen, true
}

// saveWalkState checkpoints the walk after a page. Best-effort: a failed
// checkpoint costs a restart-from-scratch later, never correctness.
func (ap *ApiProvider) saveWalkState(cursor string, startedAt time.Time, seen map[string]bool) {
	path := ap.walkStatePath()
	if path == "" || cursor == "" {
		return
	}
	st := walkState{Cursor: cursor, StartedAt: startedAt, SavedAt: time.Now(), Seen: make([]string, 0, len(seen))}
	for id := range seen {
		st.Seen = append(st.Seen, id)
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("Walk checkpoint failed: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("Walk checkpoint failed: %v", err)
	}
}

func (ap *ApiProvider) clearWalkState() {
	if path := ap.walkStatePath(); path != "" {
		_ = os.Remove(path)
	}
}
