// Package watermark records what an agent has already been shown.
//
// This is deliberately not Slack's read marker. Slack's `last_read` belongs to
// the person, is shared with their official client, and moves when they read
// something there. A watermark belongs to an agent, moves only when that agent
// acknowledges what it saw, and is invisible to the human. Keeping the two
// apart is what lets a monitor look repeatedly without disturbing anyone, and
// what lets change detection keep working after the human has caught up in
// their own client. See ADR-003.
//
// Change is measured against a conversation's `latest` timestamp — the newest
// message — and never against `last_read`, which can legitimately sit ahead of
// `latest`.
//
// Unlike the caches in pkg/cache, this state is not disposable: losing it makes
// an agent re-show everything it had already reported. It lives in its own
// directory so that clearing caches cannot take it along.
package watermark

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// Conversation is what an agent has been shown in one channel, DM, or group DM.
type Conversation struct {
	// LastShownTS is the newest message timestamp the agent has acknowledged.
	LastShownTS string `json:"last_shown_ts"`
	// LastSeenAt is when that acknowledgement happened, in wall-clock time. It
	// exists for pruning and for explaining a watermark to a human; nothing
	// compares against it.
	LastSeenAt time.Time `json:"last_seen_at"`
}

// Thread is what an agent has been shown in one thread. Threads are tracked
// separately because conversations.history does not return replies, so a
// thread's movement is invisible in its channel's `latest`.
type Thread struct {
	Channel string `json:"channel"`
	// ThreadTS identifies the thread by its root message.
	ThreadTS string `json:"thread_ts"`
	// LastReplySeen is the newest reply timestamp the agent has acknowledged.
	LastReplySeen string    `json:"last_reply_seen"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

type state struct {
	Conversations map[string]Conversation `json:"conversations"`
	Threads       map[string]Thread       `json:"threads"`
}

// Store holds one agent's watermarks for one workspace. It is safe for
// concurrent use.
type Store struct {
	path string

	mu    sync.RWMutex
	state state
}

// Open loads the watermarks for a workspace and scope, creating an empty store
// if none exist. Scope separates agents that should not share a position;
// callers with no such need pass DefaultScope.
func Open(workspace, scope string) (*Store, error) {
	if workspace == "" {
		return nil, errors.New("watermark: workspace is required")
	}
	if scope == "" {
		scope = DefaultScope
	}

	dir := filepath.Join(paths.DataDir(), "watermarks", sanitize(workspace))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("watermark: create %s: %w", dir, err)
	}

	s := &Store{
		path: filepath.Join(dir, sanitize(scope)+".json"),
		state: state{
			Conversations: make(map[string]Conversation),
			Threads:       make(map[string]Thread),
		},
	}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("watermark: read %s: %w", s.path, err)
	}

	var loaded state
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, fmt.Errorf("watermark: parse %s: %w", s.path, err)
	}
	if loaded.Conversations != nil {
		s.state.Conversations = loaded.Conversations
	}
	if loaded.Threads != nil {
		s.state.Threads = loaded.Threads
	}
	return s, nil
}

// DefaultScope is the scope used when a caller does not separate agents.
const DefaultScope = "default"

// Known reports whether this conversation has ever been acknowledged. A caller
// distinguishing "new activity" from "never looked" needs this, because
// Changed reports true for both.
func (s *Store) Known(conversationID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.state.Conversations[conversationID]
	return ok
}

// Changed reports whether a conversation has moved past what the agent was
// shown. latest is the conversation's newest message timestamp, from
// client.counts — never its last_read.
//
// A conversation with no watermark counts as changed: nothing has been shown,
// so everything in it is unseen. Suppressing that on a first run is a policy
// decision for the caller, which is why Known exists.
func (s *Store) Changed(conversationID, latest string) bool {
	if latest == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	c, ok := s.state.Conversations[conversationID]
	if !ok {
		return true
	}
	return after(latest, c.LastShownTS)
}

// ThreadChanged reports whether a thread has replies past what the agent was
// shown. latestReply comes from the thread root's latest_reply.
func (s *Store) ThreadChanged(channel, threadTS, latestReply string) bool {
	if latestReply == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.state.Threads[threadKey(channel, threadTS)]
	if !ok {
		return true
	}
	return after(latestReply, t.LastReplySeen)
}

// Ack records that the agent has been shown a conversation up to ts. It never
// moves a watermark backwards, so acknowledging a stale read cannot re-surface
// messages already reported.
func (s *Store) Ack(conversationID, ts string, now time.Time) {
	if conversationID == "" || ts == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	c := s.state.Conversations[conversationID]
	if after(ts, c.LastShownTS) {
		c.LastShownTS = ts
	}
	c.LastSeenAt = now
	s.state.Conversations[conversationID] = c
}

// AckThread records that the agent has been shown a thread up to latestReply.
func (s *Store) AckThread(channel, threadTS, latestReply string, now time.Time) {
	if channel == "" || threadTS == "" || latestReply == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := threadKey(channel, threadTS)
	t := s.state.Threads[key]
	t.Channel = channel
	t.ThreadTS = threadTS
	if after(latestReply, t.LastReplySeen) {
		t.LastReplySeen = latestReply
	}
	t.LastSeenAt = now
	s.state.Threads[key] = t
}

// Conversation returns the watermark for one conversation.
func (s *Store) Conversation(conversationID string) (Conversation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.state.Conversations[conversationID]
	return c, ok
}

// TrackedThreads returns threads acknowledged within maxAge, newest first.
// A tick polls each of these with conversations.replies, so the bound is what
// keeps that cost from growing without limit. A non-positive maxAge returns
// every tracked thread.
func (s *Store) TrackedThreads(maxAge time.Duration, now time.Time) []Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Thread, 0, len(s.state.Threads))
	for _, t := range s.state.Threads {
		if maxAge > 0 && now.Sub(t.LastSeenAt) > maxAge {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out
}

// Prune drops conversations and threads untouched for longer than maxAge,
// reporting how many entries it removed.
func (s *Store) Prune(maxAge time.Duration, now time.Time) int {
	if maxAge <= 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for id, c := range s.state.Conversations {
		if now.Sub(c.LastSeenAt) > maxAge {
			delete(s.state.Conversations, id)
			removed++
		}
	}
	for key, t := range s.state.Threads {
		if now.Sub(t.LastSeenAt) > maxAge {
			delete(s.state.Threads, key)
			removed++
		}
	}
	return removed
}

// Save writes the watermarks to disk, replacing the file atomically so a crash
// mid-write cannot leave an agent with a truncated position.
func (s *Store) Save() error {
	s.mu.RLock()
	data, err := json.Marshal(s.state)
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("watermark: marshal: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "watermarks.tmp.*")
	if err != nil {
		return fmt.Errorf("watermark: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("watermark: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("watermark: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("watermark: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("watermark: rename into %s: %w", s.path, err)
	}
	return nil
}

// Path returns the file backing this store.
func (s *Store) Path() string { return s.path }

func threadKey(channel, threadTS string) string { return channel + "\x00" + threadTS }

// after reports whether Slack timestamp a is newer than b. Slack timestamps are
// "seconds.microseconds" strings; they are compared numerically rather than
// lexicographically so that a change in the width of the seconds field cannot
// silently invert the ordering.
func after(a, b string) bool {
	if b == "" {
		return a != ""
	}
	as, af := splitTS(a)
	bs, bf := splitTS(b)
	if as != bs {
		return as > bs
	}
	return af > bf
}

func splitTS(ts string) (sec, frac int64) {
	s, f, _ := strings.Cut(ts, ".")
	sec, _ = strconv.ParseInt(s, 10, 64)

	// Pad or trim to a fixed width so "5" and "500000" compare as intended.
	if len(f) > 6 {
		f = f[:6]
	}
	for len(f) < 6 {
		f += "0"
	}
	frac, _ = strconv.ParseInt(f, 10, 64)
	return sec, frac
}

// sanitize keeps a workspace or scope name usable as a single path element.
func sanitize(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
	cleaned = strings.Trim(cleaned, ".")
	if cleaned == "" {
		return "unnamed"
	}
	return cleaned
}
