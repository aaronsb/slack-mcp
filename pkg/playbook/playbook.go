// Package playbook stores named batch sequences an agent saves for reuse.
//
// A playbook is a standing routine — a morning sweep, a weekly estate
// review — held as tool names and parameters only, never results. It lives
// beside the watermark store because it is the same class of state:
// non-disposable, owned by the agent, invisible to Slack. See ADR-010.
package playbook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// Command is one step of a playbook: a tool name and its parameters.
type Command struct {
	Tool   string                 `json:"tool"`
	Params map[string]interface{} `json:"params,omitempty"`
}

// Entry is one named playbook. SavedAt tracks the last save; LastRun and
// Runs track use, so a listing can show which routines are alive.
type Entry struct {
	Commands []Command  `json:"commands"`
	SavedAt  time.Time  `json:"saved_at"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	Runs     int        `json:"runs,omitempty"`
}

// Summary is one playbook's listing row.
type Summary struct {
	Name     string
	Commands int
	SavedAt  time.Time
	LastRun  *time.Time
	Runs     int
}

// Store holds one workspace's playbooks. It is safe for concurrent use.
// Every mutation persists before returning.
type Store struct {
	path string

	mu      sync.Mutex
	entries map[string]Entry
}

// Open loads the playbooks for a workspace, creating an empty store if none
// exist.
func Open(workspace string) (*Store, error) {
	if workspace == "" {
		return nil, errors.New("playbook: workspace is required")
	}

	dir := filepath.Join(paths.DataDir(), "watermarks", pathElement(workspace))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("playbook: create %s: %w", dir, err)
	}

	s := &Store{
		path:    filepath.Join(dir, "playbooks.json"),
		entries: make(map[string]Entry),
	}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("playbook: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &s.entries); err != nil {
		return nil, fmt.Errorf("playbook: parse %s: %w", s.path, err)
	}
	if s.entries == nil {
		s.entries = make(map[string]Entry)
	}
	return s, nil
}

// Save stores a playbook under name, replacing the whole command array.
// Run statistics survive a re-save: the name is the routine's identity, and
// editing its steps does not make it a new routine.
func (s *Store) Save(name string, cmds []Command, now time.Time) error {
	if name == "" {
		return errors.New("playbook: name is required")
	}
	if len(cmds) == 0 {
		return errors.New("playbook: at least one command is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	e := s.entries[name]
	e.Commands = cmds
	e.SavedAt = now
	s.entries[name] = e
	return s.save()
}

// Get returns a playbook's commands.
func (s *Store) Get(name string) ([]Command, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[name]
	if !ok {
		return nil, false
	}
	return append([]Command(nil), e.Commands...), true
}

// Delete removes a playbook, reporting whether it existed.
func (s *Store) Delete(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[name]; !ok {
		return false, nil
	}
	delete(s.entries, name)
	return true, s.save()
}

// MarkRun records that a playbook was executed.
func (s *Store) MarkRun(name string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[name]
	if !ok {
		return fmt.Errorf("playbook: no playbook named %q", name)
	}
	t := now
	e.LastRun = &t
	e.Runs++
	s.entries[name] = e
	return s.save()
}

// List returns every playbook's summary, sorted by name.
func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Summary, 0, len(s.entries))
	for name, e := range s.entries {
		out = append(out, Summary{
			Name:     name,
			Commands: len(e.Commands),
			SavedAt:  e.SavedAt,
			LastRun:  e.LastRun,
			Runs:     e.Runs,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Path returns the file backing this store.
func (s *Store) Path() string { return s.path }

// save writes the store to disk, replacing the file atomically so a crash
// mid-write cannot truncate a saved routine. Callers hold s.mu.
func (s *Store) save() error {
	data, err := json.Marshal(s.entries)
	if err != nil {
		return fmt.Errorf("playbook: marshal: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "playbooks.tmp.*")
	if err != nil {
		return fmt.Errorf("playbook: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("playbook: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("playbook: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("playbook: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("playbook: rename into %s: %w", s.path, err)
	}
	return nil
}

// pathElement and sanitize are copied from pkg/watermark, which documents
// why the digest suffix is needed; the copy keeps this package free of a
// cross-package refactor, matching pkg/estate's choice.

func pathElement(name string) string {
	sum := sha256.Sum256([]byte(name))
	return sanitize(name) + "-" + hex.EncodeToString(sum[:4])
}

func sanitize(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "unnamed"
	}
	if len(cleaned) > 48 {
		cleaned = cleaned[:48]
	}
	return cleaned
}
