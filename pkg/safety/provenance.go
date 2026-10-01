package safety

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Provenance is what download records about a file it wrote into the
// exchange directory (ADR-013, gate case 2): the bytes' SHA-256, the name
// it was saved as, the Slack file ID, and every conversation Slack reported
// the file as shared in. It lives in the data directory, out of reach of
// say files=.
type Provenance struct {
	Time          time.Time `json:"time"`
	SHA256        string    `json:"sha256"`
	Name          string    `json:"name"`
	FileID        string    `json:"file_id"`
	Conversations []string  `json:"conversations"`
}

// ProvenanceStore is one workspace's provenance file and its fold: for each
// content hash, the conversations every download of it was shared in.
type ProvenanceStore struct {
	mu     sync.Mutex
	j      *journal
	byHash map[string]map[string]bool
}

func newProvenanceStore(path string) *ProvenanceStore {
	s := &ProvenanceStore{j: newJournal(path)}
	s.reset()
	return s
}

func (s *ProvenanceStore) reset() { s.byHash = map[string]map[string]bool{} }

func (s *ProvenanceStore) apply(raw []byte) error {
	var p Provenance
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.SHA256 == "" {
		return errors.New("provenance line without a hash")
	}
	set := s.byHash[p.SHA256]
	if set == nil {
		set = map[string]bool{}
		s.byHash[p.SHA256] = set
	}
	for _, c := range p.Conversations {
		set[c] = true
	}
	return nil
}

// Record appends a download's provenance.
func (s *ProvenanceStore) Record(p Provenance) error {
	if p.SHA256 == "" {
		return errors.New("provenance needs a hash")
	}
	if p.Time.IsZero() {
		p.Time = time.Now()
	}
	p.Time = p.Time.UTC()
	p.Conversations = normStrings(p.Conversations)
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.j.withLock(func() error {
		if err := s.j.appendLine(raw); err != nil {
			return err
		}
		s.j.refresh(s.reset, s.apply)
		return nil
	})
}

// Lookup returns the conversations a file with this hash was shared in,
// sorted, and whether any download recorded it. It is an error when the
// file exists and cannot be read, or when the hash has no record and a
// line was skipped as malformed; the gate treats either as a move from an
// unknown conversation.
func (s *ProvenanceStore) Lookup(sha string) ([]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.j.refresh(s.reset, s.apply)
	if s.j.err != nil {
		return nil, false, fmt.Errorf("provenance cannot be read: %w", s.j.err)
	}
	set, ok := s.byHash[sha]
	if !ok {
		if bad := s.j.malformedLines(); len(bad) > 0 {
			// A skipped line may be this file's record.
			return nil, false, fmt.Errorf("provenance has %d unreadable line(s); a record may be among them", len(bad))
		}
		return nil, false, nil
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	return normStrings(out), true, nil
}
