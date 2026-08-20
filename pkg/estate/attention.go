package estate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/paths"
	"github.com/gofrs/flock"
)

// The attention ledger is the estate's decaying sibling (ADR-006 mechanics,
// ADR-008 contract): encounter events observed on the render paths, one per
// user per conversation per bucket, no message text ever. Where the estate
// file is never rewritten, the attention file is compacted at every writer
// boot — events outside the ninety-day window drop, and a fifty-thousand
// event backstop bounds the file regardless. Resolution is asymmetric by
// decision, not accident: the session's own user buckets by hour, everyone
// else by day, because hour-level strips on colleagues is movement tracking
// while "active in #eng Tuesday" is what the joins need.
const (
	attentionWindow   = 90 * 24 * time.Hour
	attentionBackstop = 50_000
)

// Encounter is one bucketed observation: this user was seen in this
// conversation in this bucket. Hour is nil for day-resolution encounters.
type Encounter struct {
	User string
	Conv string
	Day  string // "2006-01-02", from the message's own timestamp, UTC
	Hour *int   // 0-23, set only for the session's own user
}

type attnEvent struct {
	V    int       `json:"v"`
	At   time.Time `json:"at"`
	Src  Source    `json:"src"`
	Kind string    `json:"kind"`
	User string    `json:"user"`
	Conv string    `json:"conv"`
	Day  string    `json:"day"`
	Hour *int      `json:"hour,omitempty"`
}

// AttentionStore holds one workspace-and-scope attention ledger: the append
// handle, the dedup set, and the encounter fold. Writer election works as
// in the estate store; a read-only opener serves the boot-time fold and
// drops observations.
type AttentionStore struct {
	path     string
	file     *os.File
	lock     *flock.Flock
	readOnly bool

	mu sync.RWMutex // guards seen, byUser, events; writes also serialize on it

	seen   map[string]struct{}
	byUser map[string]map[string][]Encounter // user -> conv -> encounters, append order
	events int64
}

// OpenAttention replays the attention ledger for one workspace and scope,
// compacting it if the writer election is won: events older than the
// ninety-day window at `now` drop, and the newest fifty thousand survive
// the backstop. Scope separates observers — two operators' agents see
// different traffic and must not mix ledgers.
func OpenAttention(teamID, scope string, now time.Time) (*AttentionStore, error) {
	if teamID == "" || scope == "" {
		return nil, fmt.Errorf("estate: attention needs a team ID and a scope")
	}

	dir := filepath.Join(paths.DataDir(), "ledger", pathElement(teamID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("estate: create dir: %w", err)
	}
	path := filepath.Join(dir, "attention-"+pathElement(scope)+".jsonl")

	s := &AttentionStore{
		path:   path,
		seen:   map[string]struct{}{},
		byUser: map[string]map[string][]Encounter{},
	}

	s.lock = flock.New(filepath.Join(dir, "attention-"+pathElement(scope)+".lock"))
	won, lockErr := s.lock.TryLock()
	if lockErr != nil || !won {
		if s.lock != nil && won {
			_ = s.lock.Unlock()
		}
		s.lock = nil
		s.readOnly = true
	}

	kept, dropped, err := s.replay(now)
	if err != nil {
		s.closeLock()
		return nil, err
	}

	if s.readOnly {
		return s, nil
	}

	// Boot-pass compaction: rewrite only when something actually dropped
	// (expired, over backstop, or a torn tail), so a quiet boot does not
	// churn the file.
	if dropped {
		if err := s.rewrite(kept); err != nil {
			s.closeLock()
			return nil, err
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.closeLock()
		return nil, fmt.Errorf("estate: open attention ledger: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		s.closeLock()
		return nil, fmt.Errorf("estate: chmod attention ledger: %w", err)
	}
	s.file = f
	return s, nil
}

// replay loads the file, folding only events inside the window and under
// the backstop. It returns the kept lines verbatim, in file order, and
// whether the rewrite pass has anything to drop.
func (s *AttentionStore) replay(now time.Time) ([][]byte, bool, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("estate: read attention ledger: %w", err)
	}

	cutoff := now.Add(-attentionWindow).UTC().Format("2006-01-02")
	var kept [][]byte
	var folds []attnEvent
	dropped := false

	for len(data) > 0 {
		var line []byte
		nl := bytes.IndexByte(data, '\n')
		if nl >= 0 {
			line = data[:nl]
			data = data[nl+1:]
		} else {
			line = data
			data = nil
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e attnEvent
		if err := json.Unmarshal(line, &e); err != nil {
			if len(data) == 0 {
				// Torn tail: the rewrite drops it.
				dropped = true
				break
			}
			return nil, false, fmt.Errorf("estate: damaged attention line: %w", err)
		}
		if e.V > schemaVersion || e.Kind != "encounter" {
			// Unknown events survive compaction unfolded and byte-for-byte,
			// so a v2 writer's extra fields are not stripped by a v1
			// compaction pass.
			kept = append(kept, line)
			continue
		}
		if e.Day < cutoff {
			dropped = true
			continue
		}
		kept = append(kept, line)
		folds = append(folds, e)
	}

	if over := len(kept) - attentionBackstop; over > 0 {
		kept = kept[over:]
		dropped = true
		if fo := len(folds) - attentionBackstop; fo > 0 {
			folds = folds[fo:]
		}
	}

	for i := range folds {
		s.fold(&folds[i])
	}
	return kept, dropped, nil
}

// fold applies one encounter to the in-memory state. Idempotent: a
// duplicate line in the file — or a re-observed bucket — folds to nothing,
// so counts never inflate.
func (s *AttentionStore) fold(e *attnEvent) {
	k := encounterKey(e.User, e.Conv, e.Day, e.Hour)
	if _, dup := s.seen[k]; dup {
		return
	}
	s.seen[k] = struct{}{}
	convs, ok := s.byUser[e.User]
	if !ok {
		convs = map[string][]Encounter{}
		s.byUser[e.User] = convs
	}
	convs[e.Conv] = append(convs[e.Conv], Encounter{User: e.User, Conv: e.Conv, Day: e.Day, Hour: e.Hour})
	s.events++
}

func encounterKey(user, conv, day string, hour *int) string {
	k := user + "\x00" + conv + "\x00" + day
	if hour != nil {
		k = fmt.Sprintf("%s\x00%02d", k, *hour)
	}
	return k
}

// rewrite replaces the file with the kept lines, atomically via a temp
// file in the same directory.
func (s *AttentionStore) rewrite(kept [][]byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".attention-*")
	if err != nil {
		return fmt.Errorf("estate: compact attention: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("estate: compact attention: %w", err)
	}
	var buf bytes.Buffer
	for _, line := range kept {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("estate: compact attention: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("estate: compact attention: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("estate: compact attention: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("estate: compact attention: %w", err)
	}
	return nil
}

// ReadOnly reports whether this store lost the writer election.
func (s *AttentionStore) ReadOnly() bool { return s.readOnly }

func (s *AttentionStore) closeLock() {
	if s.lock != nil {
		_ = s.lock.Unlock()
		s.lock = nil
	}
}

// Close releases the append handle and the lock.
func (s *AttentionStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.closeLock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Path returns the attention ledger file's location.
func (s *AttentionStore) Path() string { return s.path }

// Observe appends the encounters that are new: not seen in their bucket,
// and inside the retention window at `at`. It returns how many were
// appended. Repeats fold to nothing, which is what bounds the volume —
// reading the same channel forty times a day writes one line per active
// participant per bucket.
func (s *AttentionStore) Observe(encounters []Encounter, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.readOnly {
		return 0, ErrReadOnly
	}
	if s.file == nil {
		return 0, fmt.Errorf("estate: attention store is closed")
	}

	cutoff := at.Add(-attentionWindow).UTC().Format("2006-01-02")
	var buf bytes.Buffer
	var fresh []attnEvent
	batch := map[string]struct{}{}

	for _, enc := range encounters {
		if enc.User == "" || enc.Conv == "" || enc.Day == "" || enc.Day < cutoff {
			continue
		}
		k := encounterKey(enc.User, enc.Conv, enc.Day, enc.Hour)
		if _, dup := s.seen[k]; dup {
			continue
		}
		if _, dup := batch[k]; dup {
			continue
		}
		batch[k] = struct{}{}
		e := attnEvent{
			V: schemaVersion, At: at.UTC(), Src: SourceTraffic, Kind: "encounter",
			User: enc.User, Conv: enc.Conv, Day: enc.Day, Hour: enc.Hour,
		}
		line, err := json.Marshal(&e)
		if err != nil {
			return 0, fmt.Errorf("estate: marshal encounter: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		fresh = append(fresh, e)
	}

	if len(fresh) == 0 {
		return 0, nil
	}
	// The seen-set commits only after the write lands: a failed append
	// leaves the buckets unrecorded and recordable, not silently burned.
	if _, err := s.file.Write(buf.Bytes()); err != nil {
		return 0, fmt.Errorf("estate: append encounters: %w", err)
	}
	// The lines are in the file, so the fold applies even when the sync
	// fails — otherwise the fold runs behind its own ledger until reboot.
	for i := range fresh {
		s.fold(&fresh[i])
	}
	if err := s.file.Sync(); err != nil {
		return len(fresh), fmt.Errorf("estate: sync attention: %w", err)
	}
	return len(fresh), nil
}

// Encounters returns a snapshot copy of one user's encounters by
// conversation, days sorted ascending.
func (s *AttentionStore) Encounters(user string) map[string][]Encounter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	convs, ok := s.byUser[user]
	if !ok {
		return nil
	}
	out := make(map[string][]Encounter, len(convs))
	for conv, encs := range convs {
		cp := make([]Encounter, len(encs))
		copy(cp, encs)
		sort.Slice(cp, func(i, j int) bool {
			if cp[i].Day != cp[j].Day {
				return cp[i].Day < cp[j].Day
			}
			hi, hj := -1, -1
			if cp[i].Hour != nil {
				hi = *cp[i].Hour
			}
			if cp[j].Hour != nil {
				hj = *cp[j].Hour
			}
			return hi < hj
		})
		out[conv] = cp
	}
	return out
}

// ByConversation returns an atomic snapshot of the whole activity plane
// inverted: conversation -> day -> distinct users seen that day. This is
// the join surface the initiatives and convergence views read.
func (s *AttentionStore) ByConversation() map[string]map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]map[string]map[string]struct{}{}
	for user, byConv := range s.byUser {
		for conv, encs := range byConv {
			days, ok := seen[conv]
			if !ok {
				days = map[string]map[string]struct{}{}
				seen[conv] = days
			}
			for _, e := range encs {
				users, ok := days[e.Day]
				if !ok {
					users = map[string]struct{}{}
					days[e.Day] = users
				}
				users[user] = struct{}{}
			}
		}
	}
	out := make(map[string]map[string][]string, len(seen))
	for conv, days := range seen {
		od := make(map[string][]string, len(days))
		for day, users := range days {
			list := make([]string, 0, len(users))
			for u := range users {
				list = append(list, u)
			}
			sort.Strings(list)
			od[day] = list
		}
		out[conv] = od
	}
	return out
}

// AttentionStats is the coverage gauge for the activity plane: how much has
// been observed, over what span.
type AttentionStats struct {
	Events   int64
	Users    int
	Convs    int
	FirstDay string
	LastDay  string
}

func (s *AttentionStore) Stats() AttentionStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := AttentionStats{Events: s.events, Users: len(s.byUser)}
	convs := map[string]struct{}{}
	for _, byConv := range s.byUser {
		for conv, encs := range byConv {
			convs[conv] = struct{}{}
			for _, e := range encs {
				if st.FirstDay == "" || e.Day < st.FirstDay {
					st.FirstDay = e.Day
				}
				if e.Day > st.LastDay {
					st.LastDay = e.Day
				}
			}
		}
	}
	st.Convs = len(convs)
	return st
}
