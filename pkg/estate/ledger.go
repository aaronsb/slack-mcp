package estate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Event kinds. ADR-007 defines exactly four; replay ignores kinds it does
// not know, so a newer binary's events do not break an older reader.
const (
	kindFirstSeen = "first-seen"
	kindChanged   = "changed"
	kindTombstone = "tombstone"
	kindSweep     = "sweep"

	entityUser    = "user"
	entityChannel = "channel"

	schemaVersion = 1
)

// event is one ledger line. Rec and Last hold the projected record for the
// event's entity type and are decoded per Entity on replay. Every change
// event carries the full record after the change, not a diff: a skipped
// torn line then loses one observation instead of corrupting every
// subsequent state, and duplicate events from interleaved writers fold to
// the same result.
type event struct {
	V      int       `json:"v"`
	At     time.Time `json:"at"`
	Src    Source    `json:"src"`
	Kind   string    `json:"kind"`
	Entity string    `json:"entity,omitempty"`
	ID     string    `json:"id,omitempty"`

	Changed      []string        `json:"changed,omitempty"`
	Rec          json.RawMessage `json:"rec,omitempty"`
	NotBefore    time.Time       `json:"notBefore,omitzero"`
	SlackUpdated int64           `json:"slackUpdated,omitempty"`

	Reason string          `json:"reason,omitempty"`
	Last   json.RawMessage `json:"last,omitempty"`

	Users      *classReport `json:"users,omitempty"`
	Channels   *classReport `json:"channels,omitempty"`
	Membership *classReport `json:"membership,omitempty"`
	Appended   *int         `json:"appended,omitempty"`
	DurationMs *int64       `json:"durationMs,omitempty"`
}

type classReport struct {
	Complete         bool `json:"complete"`
	Count            int  `json:"count,omitempty"`
	ArchivedIncluded bool `json:"archivedIncluded,omitempty"`
	AbsenceAborted   bool `json:"absenceAborted,omitempty"`
	Skipped          bool `json:"skipped,omitempty"`
}

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// The projections are plain structs of strings and bools; a marshal
		// failure is a programming error, not an input condition.
		panic(fmt.Sprintf("estate: marshal projection: %v", err))
	}
	return b
}

// appendAndApply writes the events as one buffer, syncs once, then folds
// them in. One write bounds a crash to a single torn point; one fsync per
// batch bounds the loss window to one observation. Events are folded only
// after they are durable, so the fold never runs ahead of the ledger.
func (s *Store) appendAndApply(events []event) error {
	if len(events) == 0 {
		return nil
	}
	if s.file == nil {
		return fmt.Errorf("estate: store is closed")
	}

	var buf bytes.Buffer
	for i := range events {
		line, err := json.Marshal(&events[i])
		if err != nil {
			return fmt.Errorf("estate: marshal event: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	n, err := s.file.Write(buf.Bytes())
	if err != nil {
		return fmt.Errorf("estate: append: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("estate: sync: %w", err)
	}

	s.foldMu.Lock()
	for i := range events {
		s.fold.apply(&events[i])
	}
	s.lines += int64(len(events))
	s.bytes += int64(n)
	s.foldMu.Unlock()
	return nil
}

type replayReport struct {
	lines      int64
	bytes      int64
	torn       bool
	tornOffset int64
}

// replayFile folds every event in the file at path, skipping events after
// cutoff when cutoff is nonzero. A parse failure on the final line is a
// torn tail — reported with the offset to truncate at, never an error. A
// parse failure anywhere else is corruption and errors, because appends are
// the only writer and only the tail can legally be incomplete.
func replayFile(path string, cutoff time.Time, f *fold) (replayReport, error) {
	var rep replayReport

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rep, nil
	}
	if err != nil {
		return rep, fmt.Errorf("estate: read ledger: %w", err)
	}

	offset := int64(0)
	for len(data) > 0 {
		var line []byte
		nl := bytes.IndexByte(data, '\n')
		terminated := nl >= 0
		if terminated {
			line = data[:nl]
			data = data[nl+1:]
		} else {
			line = data
			data = nil
		}
		lineLen := int64(len(line))
		if terminated {
			lineLen++
		}

		if len(bytes.TrimSpace(line)) == 0 {
			offset += lineLen
			continue
		}

		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			if len(data) == 0 {
				rep.torn = true
				rep.tornOffset = offset
				return rep, nil
			}
			return rep, fmt.Errorf("estate: damaged ledger line at byte %d: %w", offset, err)
		}

		if cutoff.IsZero() || !e.At.After(cutoff) {
			f.apply(&e)
		}
		rep.lines++
		rep.bytes += lineLen
		offset += lineLen
	}
	return rep, nil
}
