package cache

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// Store manages JSON cache files in XDG data directory with atomic writes.
// It handles serialization, periodic flushing, and TTL-based staleness.
type Store struct {
	dir       string
	mu        sync.RWMutex
	dirty     bool
	flushStop chan struct{}
}

// NewStore creates a cache store using XDG data directory.
// It ensures the directory exists and starts a periodic flush goroutine.
func NewStore() (*Store, error) {
	dir := paths.DataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("cache: create dir %s: %w", dir, err)
	}

	s := &Store{
		dir:       dir,
		flushStop: make(chan struct{}),
	}
	return s, nil
}

// Dir returns the cache directory path.
func (s *Store) Dir() string {
	return s.dir
}

// StartPeriodicFlush begins flushing dirty data every interval.
// Call Stop() to terminate the goroutine.
func (s *Store) StartPeriodicFlush(interval time.Duration, flushFn func() error) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.mu.RLock()
				dirty := s.dirty
				s.mu.RUnlock()
				if dirty {
					if err := flushFn(); err != nil {
						log.Printf("cache: periodic flush failed: %v", err)
					} else {
						s.mu.Lock()
						s.dirty = false
						s.mu.Unlock()
					}
				}
			case <-s.flushStop:
				return
			}
		}
	}()
}

// Stop terminates the periodic flush goroutine.
func (s *Store) Stop() {
	close(s.flushStop)
}

// MarkDirty flags the cache as needing a flush.
func (s *Store) MarkDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// Load reads and unmarshals a cache file. Returns os.ErrNotExist if missing.
func (s *Store) Load(filename string, dest interface{}) error {
	path := filepath.Join(s.dir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

// Save atomically writes data to a cache file using temp+rename.
func (s *Store) Save(filename string, data interface{}) error {
	path := filepath.Join(s.dir, filename)

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("cache: marshal %s: %w", filename, err)
	}

	// Write to temp file in same directory (same filesystem for rename)
	tmp, err := os.CreateTemp(s.dir, filename+".tmp.*")
	if err != nil {
		return fmt.Errorf("cache: create temp for %s: %w", filename, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(jsonData); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("cache: write temp for %s: %w", filename, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("cache: close temp for %s: %w", filename, err)
	}

	// Atomic rename
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("cache: rename %s: %w", filename, err)
	}

	return nil
}

// Exists checks if a cache file exists.
func (s *Store) Exists(filename string) bool {
	path := filepath.Join(s.dir, filename)
	_, err := os.Stat(path)
	return err == nil
}

// Age returns how old a cache file is. Returns 0 if file doesn't exist.
func (s *Store) Age(filename string) time.Duration {
	path := filepath.Join(s.dir, filename)
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return time.Since(info.ModTime())
}

// Remove deletes a cache file.
func (s *Store) Remove(filename string) error {
	path := filepath.Join(s.dir, filename)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// MigrateFromCWD moves old CWD-based cache files to XDG data dir.
// Silently skips files that don't exist or already migrated.
func (s *Store) MigrateFromCWD(oldFiles map[string]string) {
	for oldName, newName := range oldFiles {
		oldPath := oldName
		newPath := filepath.Join(s.dir, newName)

		// Skip if new file already exists
		if _, err := os.Stat(newPath); err == nil {
			continue
		}

		// Try to read old file
		data, err := os.ReadFile(oldPath)
		if err != nil {
			continue
		}

		// Write to new location atomically
		if err := s.Save(newName, json.RawMessage(data)); err != nil {
			log.Printf("cache: migrate %s -> %s failed: %v", oldName, newName, err)
			continue
		}

		log.Printf("cache: migrated %s -> %s", oldName, newPath)
	}
}
